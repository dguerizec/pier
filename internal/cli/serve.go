package cli

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
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

			effectiveBind := bind
			if effectiveBind == "" {
				effectiveBind = autoBind(cfg)
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

			out := cmd.OutOrStdout()
			recordName, recordRegistered := registerDashboardRecord(cfg, effectiveBind, out)

			addr := net.JoinHostPort(effectiveBind, fmt.Sprintf("%d", port))
			srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}

			fmt.Fprintf(out, "→ http://%s\n", addr)
			if recordRegistered {
				fmt.Fprintf(out, "→ http://%s:%d\n", recordName, port)
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
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(shutdownCtx)
			}()

			err = srv.ListenAndServe()
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		},
	}
	f := cmd.Flags()
	f.StringVar(&bind, "bind", "", "interface to bind on (default: tailnet IP in server+records mode, else 127.0.0.1)")
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

// autoBind picks the right interface when --bind isn't given. In a
// server+records install pier already knows the tailnet-reachable IP
// (AnswerIP), so binding on it makes the dashboard + API reachable from
// peers without extra config. Anything else falls back to localhost so a
// laptop install doesn't accidentally expose itself.
func autoBind(cfg *infra.Config) string {
	if cfg.Mode == infra.ModeServer && cfg.HeadscaleRecordsPath != "" && cfg.EffectiveAnswerIP() != "" {
		return cfg.EffectiveAnswerIP()
	}
	return "127.0.0.1"
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
