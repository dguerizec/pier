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

	"github.com/dguerizec/pier/internal/headscale"
	"github.com/dguerizec/pier/internal/infra"
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

//go:embed web/favicon.png
var webFaviconPNG []byte

func newServeCmd() *cobra.Command {
	var (
		bind          string
		port          int
		corsOrigins   []string
		dashboardFQDN string
	)
	cmd := &cobra.Command{
		Use:     "serve",
		Aliases: []string{"web"},
		Short:   "Serve the pier HTTP surface (dashboard at /, REST API at /api/v1/)",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(cmd, bind, port, corsOrigins, dashboardFQDN)
		},
	}
	f := cmd.Flags()
	f.StringVar(&bind, "bind", "", "interface to bind on (default: 127.0.0.1 + pier network gateway + AnswerIP in server mode)")
	f.IntVar(&port, "port", 60080, "TCP port to listen on")
	f.StringSliceVar(&corsOrigins, "cors-origin", []string{"*"}, "comma-separated CORS origins for /api/v1/* (default: any)")
	f.StringVar(&dashboardFQDN, "dashboard-fqdn", "", "override the persisted dashboard FQDN for this run (default: cfg.dashboard_fqdn or pier.<TLD>)")

	cmd.AddCommand(
		newServeInstallCmd(),
		newServeUninstallCmd(),
		newServeUpgradeCmd(),
	)
	return cmd
}

// runServe is the foreground daemon body. Extracted so cobra wiring
// stays a thin dispatch layer and so SIGUSR2 re-exec lands back in
// the same code path on restart.
func runServe(cmd *cobra.Command, bind string, port int, corsOrigins []string, dashboardFQDN string) error {
	paths, err := infra.DefaultPaths()
	if err != nil {
		return err
	}
	cfg, err := infra.LoadConfig(paths)
	if err != nil {
		return err
	}

	// CLI flag overrides the persisted FQDN for this run only — useful
	// for ad-hoc tests on a different hostname without rewriting cfg.
	// Doesn't persist; the next pier serve start reads cfg again.
	if dashboardFQDN != "" {
		cfg.DashboardFQDN = dashboardFQDN
	}

	out := cmd.OutOrStdout()

	// SIGINT / SIGTERM = stop. SIGUSR2 = swap binary in place; handled
	// separately below so it doesn't cancel the request-serving context.
	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	hub := newEventHub(paths, cfg)
	hub.start(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", serveAsset("text/html; charset=utf-8", webIndexHTML))
	mux.HandleFunc("GET /app.css", serveAsset("text/css; charset=utf-8", webAppCSS))
	mux.HandleFunc("GET /app.js", serveAsset("application/javascript; charset=utf-8", webAppJS))
	mux.HandleFunc("GET /favicon.png", serveAsset("image/png", webFaviconPNG))
	(&apiHandler{paths: paths, cfg: cfg, hub: hub}).register(mux)

	handler := withCORS(mux, corsOrigins)

	// bridgeGateway is needed both for resolveBinds (so traefik can
	// reach pier serve over the bridge) and registerDashboardRoute
	// (upstream IP). Empty when docker is unavailable / pre-install /
	// BYO mode.
	bridgeGateway, _ := discoverBridgeGatewayIP(infra.NetworkName)

	listeners, addrs, inherited, err := openListeners(bind, port, cfg, bridgeGateway, out, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	if len(listeners) == 0 {
		return errors.New("no listener could be opened")
	}

	recordName, recordRegistered := registerDashboardRecord(cfg, primaryReachableBind(addrsToHosts(addrs)), out)
	routeName, routeRegistered := registerDashboardRoute(paths, cfg, bridgeGateway, addrsToHosts(addrs), port, out)

	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}

	if recordRegistered {
		fmt.Fprintf(out, "→ http://%s:%d\n", recordName, port)
	}
	if routeRegistered {
		fmt.Fprintf(out, "→ http://%s (via traefik)\n", routeName)
	}
	if inherited {
		fmt.Fprintln(out, "  (inherited listeners from previous binary)")
	}
	fmt.Fprintln(out, "  ctrl-c to stop · SIGUSR2 to swap binary")

	if err := writePidfile(paths); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "! pidfile: %v (upgrade signal won't work)\n", err)
	}
	defer removePidfile(paths)

	upgradeCh := make(chan os.Signal, 1)
	signal.Notify(upgradeCh, syscall.SIGUSR2)
	defer signal.Stop(upgradeCh)

	go func() {
		select {
		case <-ctx.Done():
			if recordRegistered {
				if removed, err := headscale.Remove(cfg.HeadscaleRecordsPath, recordName); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "! headscale record cleanup %s: %v\n", recordName, err)
				} else if removed {
					fmt.Fprintf(out, "✓ headscale record removed: %s\n", recordName)
				}
			}
			if routeRegistered {
				if removed, err := infra.RemoveDashboardRoute(dashboardRouteDir(cfg, paths)); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "! traefik route cleanup: %v\n", err)
				} else if removed {
					fmt.Fprintf(out, "✓ traefik route removed: %s\n", routeName)
				}
			}
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
		case <-upgradeCh:
			fmt.Fprintln(out, "↻ SIGUSR2 received — re-execing on new binary")
			// We deliberately don't srv.Shutdown() before exec: the
			// listener fds must stay open so the child inherits bound
			// sockets. The execve replaces this process image
			// entirely; in-flight HTTP requests die. SSE clients
			// reconnect within seconds, so the visible gap is small.
			// Records and traefik routes are NOT torn down — the new
			// image rewrites them on start (registerDashboardRecord
			// and WriteDashboardRoute are idempotent).
			if err := reexecSelf(listeners, addrs); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "! re-exec failed: %v\n", err)
				stop() // fall back to clean shutdown → systemd Restart=on-failure picks up the new binary
			}
		}
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
}

// openListeners returns the set of bound sockets the serve runs on,
// along with their host:port addresses. On a re-exec PIER_LISTENER_FDS
// is set: we adopt the inherited fds and skip net.Listen entirely.
// Otherwise resolveBinds picks the bind set and we open one socket
// per address.
func openListeners(bind string, port int, cfg *infra.Config, bridgeGateway string, out, errOut io.Writer) ([]net.Listener, []string, bool, error) {
	if listeners, addrs, err := inheritedListeners(); err != nil {
		return nil, nil, false, err
	} else if len(listeners) > 0 {
		for _, a := range addrs {
			fmt.Fprintf(out, "→ http://%s (inherited)\n", a)
		}
		return listeners, addrs, true, nil
	}

	bindAddrs := resolveBinds(bind, cfg, bridgeGateway)
	if len(bindAddrs) == 0 {
		return nil, nil, false, errors.New("no bind address available; pass --bind explicitly")
	}
	listeners := make([]net.Listener, 0, len(bindAddrs))
	addrs := make([]string, 0, len(bindAddrs))
	for _, addr := range bindAddrs {
		full := net.JoinHostPort(addr, fmt.Sprintf("%d", port))
		ln, err := net.Listen("tcp", full)
		if err != nil {
			fmt.Fprintf(errOut, "! listen %s: %v (skipped)\n", full, err)
			continue
		}
		listeners = append(listeners, ln)
		addrs = append(addrs, full)
		fmt.Fprintf(out, "→ http://%s\n", full)
	}
	return listeners, addrs, false, nil
}

// addrsToHosts strips the :port suffix from each entry. The host-only
// form is what registerDashboardRecord and registerDashboardRoute
// expect (they pick a single primary IP from the list).
func addrsToHosts(addrs []string) []string {
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		host, _, err := net.SplitHostPort(a)
		if err != nil {
			out = append(out, a)
			continue
		}
		out = append(out, host)
	}
	return out
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
//   - AnswerIP (in server mode): the externally-reachable IP that DNS
//     hands out for *.<tld>. BYO traefik on a foreign docker network
//     cannot reach pier serve via 127.0.0.1 or the bridge, so the
//     dashboard route at http://pier.<tld> needs a bind on AnswerIP.
//     Records-mode peers also dial pier.<tld> directly. 0.0.0.0 is
//     skipped because dnsmasq's "AnswerIP unset" path leaves it as
//     the wildcard listener which we already get via 127.0.0.1.
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
	if cfg.Mode == infra.ModeServer {
		if tn := cfg.EffectiveAnswerIP(); tn != "" && tn != "0.0.0.0" {
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

// registerDashboardRecord adds an A record for the dashboard FQDN so
// peers can reach the dashboard at a friendly URL instead of an IP.
// Only fires when the user opted in to a dashboard FQDN backed by
// headscale's extra_records (the records adapter): cfg.DashboardFQDN
// is set AND cfg.HeadscaleRecordsPath is set. The split-DNS wildcard
// path (DashboardFQDN empty or pointing under cfg.TLD) needs no
// record — pier-dnsmasq answers `pier.<tld>` automatically.
//
// Skipped when bind is loopback (the address wouldn't be reachable
// anyway) or when the record already exists for someone else
// (ErrConflict).
func registerDashboardRecord(cfg *infra.Config, bind string, out io.Writer) (string, bool) {
	if cfg.DashboardFQDN == "" || cfg.HeadscaleRecordsPath == "" {
		return "", false
	}
	if bind == "" || bind == "127.0.0.1" || bind == "0.0.0.0" {
		return "", false
	}
	name := cfg.DashboardFQDN
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

// registerDashboardRoute writes a traefik file-provider entry so the
// dashboard FQDN lands on the running serve. The FQDN is taken from
// cfg.EffectiveDashboardFQDN(): explicit cfg.DashboardFQDN when set
// (typically `pier.<base_domain>` after `pier serve install` opt-in),
// otherwise the implicit `pier.<TLD>` covered by the split-DNS
// wildcard.
//
// Two modes, picked by which directory traefik watches:
//
//   - pier-managed (cfg.ExternalTraefikDynamicDir == ""): drop the
//     yaml in paths.TraefikDynamic. Upstream is the pier docker
//     network's bridge gateway — pier serve listens on it (see
//     resolveBinds), and traefik reaches it over the same network.
//   - BYO (cfg.ExternalTraefikDynamicDir != ""): drop the yaml in
//     the user's traefik dir. Upstream is the first peer-reachable
//     bind (typically the tailnet/LAN IP), falling back to the bridge
//     gateway when the external traefik happens to share pier's net.
//
// Skips silently and surfaces a warning when no usable upstream IP
// can be picked (e.g. --bind 127.0.0.1 with no bridge), since writing
// an unreachable route would just produce 502s at request time.
//
// Returns (fqdn, true) when a file was written so shutdown can remove
// it; (_, false) when we skipped (nothing to clean up).
func registerDashboardRoute(paths *infra.Paths, cfg *infra.Config, bridgeGateway string, bindAddrs []string, port int, out io.Writer) (string, bool) {
	fqdn := cfg.EffectiveDashboardFQDN()
	if fqdn == "" {
		return "", false
	}
	dir := dashboardRouteDir(cfg, paths)
	if _, err := os.Stat(dir); err != nil {
		return "", false
	}

	upstreamIP := dashboardUpstreamIP(cfg, bridgeGateway, bindAddrs)
	if upstreamIP == "" {
		fmt.Fprintln(out, "! traefik route skipped: no externally-reachable upstream IP among binds")
		return "", false
	}

	upstream := fmt.Sprintf("http://%s:%d", upstreamIP, port)
	if _, err := infra.WriteDashboardRoute(dir, fqdn, upstream); err != nil {
		fmt.Fprintf(out, "! traefik route %s: %v\n", fqdn, err)
		return "", false
	}
	fmt.Fprintf(out, "✓ traefik route: %s → %s (in %s)\n", fqdn, upstream, dir)
	return fqdn, true
}

// dashboardRouteDir returns the file-provider directory pier serve
// should write pier-dashboard.yml to. BYO override wins when set.
func dashboardRouteDir(cfg *infra.Config, paths *infra.Paths) string {
	if cfg.ExternalTraefikDynamicDir != "" {
		return cfg.ExternalTraefikDynamicDir
	}
	return paths.TraefikDynamic
}

// dashboardUpstreamIP picks the IP advertised in the route file's
// loadbalancer URL. The pick must be reachable from inside the
// traefik instance that watches the dir; the heuristic differs
// between pier-managed and BYO modes.
func dashboardUpstreamIP(cfg *infra.Config, bridgeGateway string, bindAddrs []string) string {
	if cfg.ExternalTraefikDynamicDir != "" {
		// BYO: prefer a peer-reachable IP (tailnet/LAN), since the
		// external traefik usually isn't on the pier docker network.
		// Fall back to the bridge gateway only when the user wired
		// their traefik onto NetworkName too.
		if ip := primaryReachableBind(bindAddrs); ip != "" {
			return ip
		}
		if bridgeGateway != "" && slices.Contains(bindAddrs, bridgeGateway) {
			return bridgeGateway
		}
		return ""
	}
	// pier-managed: must reach pier serve over the pier bridge. Skip
	// when --bind 127.0.0.1 dropped the gateway from the bind set.
	if bridgeGateway != "" && slices.Contains(bindAddrs, bridgeGateway) {
		return bridgeGateway
	}
	return ""
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
