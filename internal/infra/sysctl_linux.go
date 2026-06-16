//go:build linux

package infra

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
)

const sysctlDropinPath = "/etc/sysctl.d/99-pier.conf"

// renderNonlocalBindSysctl returns the body pier drops into /etc/sysctl.d/.
// The setting allows bind() to succeed against IPs that aren't (yet)
// assigned to any host interface — exactly the case at boot when docker
// starts pier-traefik before tailscaled has put the tailnet IP on
// tailscale0. docker-proxy's bind() then returns EADDRNOTAVAIL, docker
// silently swallows the error, and the container ends up with a ghost
// port mapping (visible in `docker inspect`, absent from `docker port`).
// Standard pattern used by keepalived / HAProxy for VIPs.
func renderNonlocalBindSysctl() []byte {
	return []byte(`# Written by pier — see https://github.com/dguerizec/pier
# Allows bind() to non-local IPs so docker-proxy can bind the
# tailscale IP at boot before tailscaled has assigned it.
net.ipv4.ip_nonlocal_bind = 1
net.ipv6.ip_nonlocal_bind = 1
`)
}

// configureNonlocalBind drops the sysctl conf when bindIP is a specific
// non-loopback address. No-op for loopback / wildcard binds where the
// workaround isn't needed (the kernel never rejects bind() on those).
// Bool reports whether the file was actually written.
func configureNonlocalBind(bindIP string) (bool, error) {
	if !needsNonlocalBindForIP(bindIP) {
		return false, nil
	}
	body := renderNonlocalBindSysctl()

	if existing, err := os.ReadFile(sysctlDropinPath); err == nil && bytes.Equal(existing, body) {
		return false, nil
	}

	tmp, err := os.CreateTemp("", "pier-sysctl-*.conf")
	if err != nil {
		return false, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return false, err
	}
	tmp.Close()

	if err := runSudo("install", "-m", "0644", "-D", tmpPath, sysctlDropinPath); err != nil {
		return false, fmt.Errorf("install sysctl drop-in (you may need to enter your password): %w", err)
	}
	if err := runSudo("sysctl", "-p", sysctlDropinPath); err != nil {
		return false, fmt.Errorf("apply sysctl: %w", err)
	}
	return true, nil
}

// unconfigureNonlocalBind removes the drop-in. We do not force the
// kernel value back to 0 — the user may run other software (keepalived,
// HAProxy, wireguard) that depends on the same setting. `sysctl --system`
// re-reads every drop-in so any other source still wins; if nothing
// else sets it, the kernel default (0) takes effect on next boot.
func unconfigureNonlocalBind() (bool, error) {
	if _, err := os.Stat(sysctlDropinPath); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err := runSudo("rm", "-f", sysctlDropinPath); err != nil {
		return false, err
	}
	if err := runSudo("sysctl", "--system"); err != nil {
		return false, err
	}
	return true, nil
}

// needsNonlocalBindRewrite is the doctor counterpart: true when bindIP
// requires the workaround but the drop-in is missing or stale.
func needsNonlocalBindRewrite(bindIP string) bool {
	if !needsNonlocalBindForIP(bindIP) {
		return false
	}
	body, err := os.ReadFile(sysctlDropinPath)
	if err != nil {
		return true
	}
	return !bytes.Equal(body, renderNonlocalBindSysctl())
}

// checkNonlocalBind reports drop-in presence + content match. Skipped
// (Pass with detail) for loopback / wildcard binds.
func checkNonlocalBind(bindIP string) Check {
	if !needsNonlocalBindForIP(bindIP) {
		return Check{
			Name:   "kernel allows bind to " + bindIPLabel(bindIP),
			Status: StatusPass,
			Detail: "not applicable (loopback or wildcard bind)",
		}
	}
	body, err := os.ReadFile(sysctlDropinPath)
	if errors.Is(err, os.ErrNotExist) {
		return Check{
			Name:    "kernel allows bind to " + bindIP,
			Status:  StatusFail,
			Detail:  sysctlDropinPath + " missing — docker may lose its port mapping at boot when tailscale isn't yet up",
			FixHint: "pier doctor --fix  (re-runs the sudo install step)",
		}
	}
	if err != nil {
		return Check{Name: "kernel allows bind to " + bindIP, Status: StatusWarn, Detail: err.Error()}
	}
	if !bytes.Equal(body, renderNonlocalBindSysctl()) {
		return Check{
			Name:    "kernel allows bind to " + bindIP,
			Status:  StatusFail,
			Detail:  sysctlDropinPath + " stale",
			FixHint: "pier doctor --fix",
		}
	}
	return Check{Name: "kernel allows bind to " + bindIP, Status: StatusPass}
}

// needsNonlocalBindForIP returns true for specific non-loopback IPs.
// Wildcard binds (0.0.0.0 / ::) always succeed regardless, and loopback
// is always assigned, so neither needs the workaround.
func needsNonlocalBindForIP(bindIP string) bool {
	ip := net.ParseIP(bindIP)
	if ip == nil {
		return false
	}
	return !ip.IsLoopback() && !ip.IsUnspecified()
}

func bindIPLabel(bindIP string) string {
	if bindIP == "" {
		return "host interface"
	}
	return bindIP
}
