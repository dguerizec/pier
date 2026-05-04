package cli

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/LeoPartt/pier/internal/headscale"
	"github.com/LeoPartt/pier/internal/infra"
)

// SPA assets — three flat files served as-is. No build step, no Go
// templating: the page is data-driven via /api/v1/* + SSE.
//
//go:embed web/index.html
var webIndexHTML []byte

//go:embed web/app.css
var webAppCSS []byte

//go:embed web/app.js
var webAppJS []byte

func newServeCmd() *cobra.Command {
	var (
		bind        string
		port        int
		corsOrigins []string
	)
	cmd := &cobra.Command{
		Use:     "serve",
		Aliases: []string{"web"},
		Short:   "Serve the pier HTTP surface (dashboard at /, REST API at /api/v1/)",
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := infra.DefaultPaths()
			if err != nil {
				return err
			}
			cfg, err := infra.LoadConfig(paths)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			// Discover the pier docker network gateway once; both
			// resolveBinds (to widen the listen set so traefik can reach
			// us) and registerDashboardRoute (to know the upstream IP)
			// need it. Empty when docker is unavailable, the network
			// hasn't been created yet, or pier is BYO-traefik.
			bridgeGateway, _ := discoverBridgeGatewayIP(infra.NetworkName)
			bindAddrs := resolveBinds(bind, cfg, bridgeGateway)
			if len(bindAddrs) == 0 {
				return errors.New("no bind address available; pass --bind explicitly")
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			hub := newEventHub(paths, cfg)
			hub.start(ctx)

			mux := http.NewServeMux()
			mux.HandleFunc("GET /{$}", serveAsset("text/html; charset=utf-8", webIndexHTML))
			mux.HandleFunc("GET /app.css", serveAsset("text/css; charset=utf-8", webAppCSS))
			mux.HandleFunc("GET /app.js", serveAsset("application/javascript; charset=utf-8", webAppJS))
			(&apiHandler{paths: paths, cfg: cfg, hub: hub}).register(mux)

			handler := withCORS(mux, corsOrigins)

			recordName, recordRegistered := registerDashboardRecord(cfg, primaryReachableBind(bindAddrs), out)
			routeName, routeRegistered := registerDashboardRoute(paths, cfg, bridgeGateway, bindAddrs, port, out)

			srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}

			listeners := make([]net.Listener, 0, len(bindAddrs))
			for _, addr := range bindAddrs {
				full := net.JoinHostPort(addr, fmt.Sprintf("%d", port))
				ln, err := net.Listen("tcp", full)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "! listen %s: %v (skipped)\n", full, err)
					continue
				}
				listeners = append(listeners, ln)
				fmt.Fprintf(out, "→ http://%s\n", full)
			}
			if len(listeners) == 0 {
				return errors.New("no listener could be opened")
			}
			if recordRegistered {
				fmt.Fprintf(out, "→ http://%s:%d\n", recordName, port)
			}
			if routeRegistered {
				fmt.Fprintf(out, "→ http://%s (via traefik)\n", routeName)
			}
			fmt.Fprintln(out, "  ctrl-c to stop")

			go func() {
				<-ctx.Done()
				if recordRegistered {
					if removed, err := headscale.Remove(cfg.HeadscaleRecordsPath, recordName); err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "! headscale record cleanup %s: %v\n", recordName, err)
					} else if removed {
						fmt.Fprintf(out, "✓ headscale record removed: %s\n", recordName)
					}
				}
				if routeRegistered {
					if err := infra.RemoveDashboardRoute(paths.TraefikDynamic); err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "! traefik route cleanup: %v\n", err)
					} else {
						fmt.Fprintf(out, "✓ traefik route removed: %s\n", routeName)
					}
				}
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(shutdownCtx)
			}()

			errCh := make(chan error, len(listeners))
			for _, ln := range listeners {
				go func(l net.Listener) {
					errCh <- srv.Serve(l)
				}(ln)
			}
			// Block until any listener errors out (typically all of them
			// once Shutdown has fired). ErrServerClosed is the clean exit.
			for i := 0; i < len(listeners); i++ {
				if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
					return err
				}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&bind, "bind", "", "interface to bind on (default: 127.0.0.1 + pier network gateway + tailnet IP in server+records mode)")
	f.IntVar(&port, "port", 60080, "TCP port to listen on")
	f.StringSliceVar(&corsOrigins, "cors-origin", []string{"*"}, "comma-separated CORS origins for /api/v1/* (default: any)")
	return cmd
}

// serveAsset writes a fixed byte slice with no caching. Disabling the cache
// keeps a `pier upgrade` immediately visible — the SPA is small enough that
// a re-fetch per page load is invisible.
func serveAsset(contentType string, body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(body)
	}
}

// resolveBinds builds the list of addresses pier serve listens on. An
// explicit --bind short-circuits to a single address (advanced users
// asked for a specific interface). When --bind is empty we layer:
//
//   - 127.0.0.1: the human-typed `localhost:60080` always works.
//   - bridgeGateway (when non-empty): the pier docker network's gateway
//     IP. Lets the pier-managed traefik container reach pier serve over
//     the bridge for the http://pier.<tld> route. Empty in BYO mode or
//     when the network hasn't been created yet (pre-install).
//   - tailnet IP: in server+records installs the daemon should be
//     reachable from peers, mirroring the pre-existing autoBind behaviour.
//
// Duplicates are dropped so a tailnet-IP install doesn't open the same
// port twice when the bridge gateway happens to coincide.
func resolveBinds(explicit string, cfg *infra.Config, bridgeGateway string) []string {
	if explicit != "" {
		return []string{explicit}
	}
	addrs := []string{"127.0.0.1"}
	if bridgeGateway != "" {
		addrs = appendUnique(addrs, bridgeGateway)
	}
	if cfg.Mode == infra.ModeServer && cfg.HeadscaleRecordsPath != "" {
		if tn := cfg.EffectiveAnswerIP(); tn != "" {
			addrs = appendUnique(addrs, tn)
		}
	}
	return addrs
}

// primaryReachableBind picks the bind address used for the headscale
// auto-record. We need an address tailnet peers can resolve; loopback
// and RFC1918 IPs are local-only. Returns "" when the list has no
// peer-reachable entry, which keeps registerDashboardRecord a no-op
// (avoids surfacing an unroutable IP to headscale.Add).
func primaryReachableBind(addrs []string) string {
	for _, a := range addrs {
		if a == "127.0.0.1" || a == "0.0.0.0" || strings.HasPrefix(a, "172.") || strings.HasPrefix(a, "10.") || strings.HasPrefix(a, "192.168.") {
			continue
		}
		return a
	}
	return ""
}

func appendUnique(xs []string, x string) []string {
	for _, e := range xs {
		if e == x {
			return xs
		}
	}
	return append(xs, x)
}

// discoverBridgeGatewayIP shells out to `docker network inspect` and
// returns the gateway IP of the named user-defined bridge. We don't
// want a stable docker SDK dependency for one read; the binary is
// ~always around in pier's runtime.
func discoverBridgeGatewayIP(network string) (string, error) {
	cmd := exec.Command("docker", "network", "inspect", network, "--format", "{{(index .IPAM.Config 0).Gateway}}")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// registerDashboardRecord adds a `pier.<tld>` A record so peers can reach
// the dashboard at a friendly URL instead of an IP. No-op outside records
// mode, when bind is loopback (the address wouldn't be reachable anyway),
// or when the record already exists for someone else (ErrConflict).
func registerDashboardRecord(cfg *infra.Config, bind string, out io.Writer) (string, bool) {
	if cfg.HeadscaleRecordsPath == "" || cfg.TLD == "" {
		return "", false
	}
	if bind == "" || bind == "127.0.0.1" || bind == "0.0.0.0" {
		return "", false
	}
	name := "pier." + cfg.TLD
	added, err := headscale.Add(cfg.HeadscaleRecordsPath, name, bind)
	if errors.Is(err, headscale.ErrConflict) {
		fmt.Fprintf(out, "! headscale: %s already mapped elsewhere; skipping auto-record\n", name)
		return "", false
	}
	if err != nil {
		fmt.Fprintf(out, "! headscale auto-record %s: %v\n", name, err)
		return "", false
	}
	if added {
		fmt.Fprintf(out, "✓ headscale record: %s → %s\n", name, bind)
	}
	// Even when the record already existed (added=false, no conflict),
	// we still own the cleanup — return registered=true so shutdown
	// removes it. Rationale: a previous serve run left the record
	// behind on a crash; the running daemon should still tidy up.
	return name, true
}

// registerDashboardRoute writes a traefik file-provider entry so
// `http://pier.<tld>` lands on the running serve. Upstream is the pier
// network's bridge gateway IP — pier serve listens on it (see
// resolveBinds), and that IP is reachable from inside traefik because
// traefik runs on the same network.
//
// Skips silently when:
//   - no TLD configured;
//   - pier is in BYO-traefik mode (cfg.ExternalTraefik set): the
//     pier-managed dynamic dir isn't read by the user's traefik, so
//     writing there would be a lie. External-dir support lands in a
//     follow-up commit;
//   - the bridge gateway isn't in bindAddrs (e.g. user passed
//     --bind 127.0.0.1): traefik would 502 because pier serve doesn't
//     listen on the gateway IP;
//   - the bridge gateway can't be discovered (docker down, network
//     not created yet);
//   - paths.TraefikDynamic does not exist on disk.
//
// Returns (fqdn, true) when a file was written so shutdown can remove
// it; (_, false) when we skipped (nothing to clean up).
func registerDashboardRoute(paths *infra.Paths, cfg *infra.Config, bridgeGateway string, bindAddrs []string, port int, out io.Writer) (string, bool) {
	if cfg.TLD == "" {
		return "", false
	}
	if cfg.ExternalTraefik != "" {
		return "", false
	}
	if bridgeGateway == "" {
		return "", false
	}
	if !slices.Contains(bindAddrs, bridgeGateway) {
		fmt.Fprintf(out, "! traefik route skipped: pier serve is not bound on %s (would 502)\n", bridgeGateway)
		return "", false
	}
	if _, err := os.Stat(paths.TraefikDynamic); err != nil {
		return "", false
	}

	upstream := fmt.Sprintf("http://%s:%d", bridgeGateway, port)
	host := "pier"
	fqdn := host + "." + cfg.TLD

	if _, err := infra.WriteDashboardRoute(paths.TraefikDynamic, host, cfg.TLD, upstream); err != nil {
		fmt.Fprintf(out, "! traefik route %s: %v\n", fqdn, err)
		return "", false
	}
	fmt.Fprintf(out, "✓ traefik route: %s → %s\n", fqdn, upstream)
	return fqdn, true
}

// withCORS injects Access-Control-* headers on /api/v1/* responses and
// short-circuits OPTIONS preflights. Origins is a static allowlist; "*"
// (the MVP default) means any. The dashboard at / is same-origin and
// doesn't need CORS, so we scope the middleware to /api/.
func withCORS(next http.Handler, origins []string) http.Handler {
	wildcard := len(origins) == 1 && origins[0] == "*"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		switch {
		case wildcard:
			w.Header().Set("Access-Control-Allow-Origin", "*")
		case origin != "" && slices.Contains(origins, origin):
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
