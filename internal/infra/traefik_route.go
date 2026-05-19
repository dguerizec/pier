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
// routes fqdn to a service running on the host. fqdn is the full
// hostname (e.g. "pier.test" or "pier.nebula"); upstream is the URL
// traefik proxies to from inside its container. dir is the
// file-provider directory traefik watches — pier-managed
// (paths.TraefikDynamic) by default, or an external directory in
// BYO-traefik mode.
//
// Returns the absolute path of the written file so the caller can hand
// it back to RemoveDashboardRoute on shutdown. Writes are atomic via a
// sibling .tmp + rename so traefik's filesystem watch never sees a
// half-written file.
func WriteDashboardRoute(dir, fqdn, upstream string) (string, error) {
	if fqdn == "" {
		return "", errors.New("traefik route: fqdn is required")
	}
	if upstream == "" {
		return "", errors.New("traefik route: upstream URL is required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	body := renderDashboardRoute(fqdn, upstream)
	path := filepath.Join(dir, DashboardRouteFile)
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

// RemoveDashboardRoute deletes the file-provider entry from dir.
// Missing file is not an error — pier serve may run without ever
// having written one (no TLD configured, conflict with another
// writer, etc.).
func RemoveDashboardRoute(dir string) (bool, error) {
	path := filepath.Join(dir, DashboardRouteFile)
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// renderDashboardRoute builds the static traefik dynamic yaml. We use
// fixed router/service names (`pier-dashboard`) because there is at
// most one of these per host; if a future feature needs a second route
// it can land in its own file with its own names.
func renderDashboardRoute(fqdn, upstream string) string {
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
