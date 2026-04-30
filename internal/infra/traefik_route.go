package infra

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DashboardRouteFile is the file-provider entry pier owns for the
// dashboard / API. Lives under <Paths.TraefikDynamic>; pier serve
// writes it on startup and removes it on shutdown.
const DashboardRouteFile = "pier-dashboard.yml"

// WriteDashboardRoute (re)writes the traefik file-provider yaml that
// routes `host.<tld>` to a service running on the host. host is the
// sub-domain label (e.g. "pier"); upstream is the URL traefik proxies
// to from inside its container, typically
// `http://host.docker.internal:<port>` for a host-loopback `pier serve`.
//
// Returns the absolute path of the written file so the caller can hand
// it back to RemoveDashboardRoute on shutdown. No-op-friendly: writes
// are atomic via a sibling .tmp + rename so traefik's filesystem watch
// never sees a half-written file.
func WriteDashboardRoute(paths *Paths, host, tld, upstream string) (string, error) {
	if tld == "" {
		return "", errors.New("traefik route: tld is required")
	}
	if host == "" {
		return "", errors.New("traefik route: host is required")
	}
	if upstream == "" {
		return "", errors.New("traefik route: upstream URL is required")
	}
	if err := os.MkdirAll(paths.TraefikDynamic, 0o755); err != nil {
		return "", err
	}
	body := renderDashboardRoute(host, tld, upstream)
	path := filepath.Join(paths.TraefikDynamic, DashboardRouteFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return path, nil
}

// RemoveDashboardRoute deletes the file-provider entry. Missing file
// is not an error — pier serve may run without ever having written one
// (no TLD configured, conflict with another writer, etc.).
func RemoveDashboardRoute(paths *Paths) error {
	path := filepath.Join(paths.TraefikDynamic, DashboardRouteFile)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// renderDashboardRoute builds the static traefik dynamic yaml. We use
// fixed router/service names (`pier-dashboard`) because there is at
// most one of these per host; if a future feature needs a second route
// it can land in its own file with its own names.
func renderDashboardRoute(host, tld, upstream string) string {
	fqdn := host + "." + tld
	var b strings.Builder
	fmt.Fprintln(&b, "# managed by pier; rewritten by `pier serve` on every start, removed on shutdown")
	fmt.Fprintln(&b, "http:")
	fmt.Fprintln(&b, "  routers:")
	fmt.Fprintln(&b, "    pier-dashboard:")
	fmt.Fprintf(&b, "      rule: \"Host(`%s`)\"\n", fqdn)
	fmt.Fprintln(&b, "      entryPoints:")
	fmt.Fprintln(&b, "        - web")
	fmt.Fprintln(&b, "      service: pier-dashboard")
	fmt.Fprintln(&b, "  services:")
	fmt.Fprintln(&b, "    pier-dashboard:")
	fmt.Fprintln(&b, "      loadBalancer:")
	fmt.Fprintln(&b, "        servers:")
	fmt.Fprintf(&b, "          - url: %q\n", upstream)
	return b.String()
}
