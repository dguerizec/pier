//go:build !linux

package infra

import "errors"

// configureHostDNS is a stub on non-Linux platforms; automatic host DNS setup
// is Linux-only today.
func configureHostDNS(tld, dnsIP string) (bool, error) {
	return false, errors.New("infra: host DNS configuration only supported on Linux for MVP (use --manual-dns elsewhere)")
}

func unconfigureHostDNS() (bool, error) { return false, nil }

func manualDNSInstructions(tld, dnsIP string) string {
	return "macOS/Windows host DNS setup is manual today; configure a resolver for ." + tld + " pointing at " + dnsIP + "."
}

func checkResolvedDropin(tld string) Check {
	return Check{
		Name:   "systemd-resolved drop-in",
		Status: StatusWarn,
		Detail: "skipped: only applicable on Linux",
	}
}

func needsResolvedRewrite(tld, bindIP string) bool { return false }
