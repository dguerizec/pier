//go:build !linux

package infra

import "errors"

// configureHostDNS is a stub on non-Linux platforms; macOS support lands in v0.2.
func configureHostDNS(tld, dnsIP string) error {
	return errors.New("infra: host DNS configuration only supported on Linux for MVP (use --manual-dns elsewhere)")
}

func unconfigureHostDNS() error { return nil }

func manualDNSInstructions(tld, dnsIP string) string {
	return "macOS/Windows host DNS instructions: see DESIGN §5.7 (post-MVP)."
}
