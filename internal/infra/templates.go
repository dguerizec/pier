package infra

import (
	"bytes"
	"fmt"
	"text/template"
)

// renderTraefikStatic produces the traefik static config that pier ships.
//
// The docker provider watches the `pier` network so compose-adapter
// workloads register via labels. The file provider watches a directory the
// process-adapter writes router definitions into. No dashboard, no TLS.
func renderTraefikStatic() ([]byte, error) {
	const tmpl = `# managed by pier
entryPoints:
  web:
    address: ":80"

providers:
  docker:
    exposedByDefault: false
    network: pier
    endpoint: "unix:///var/run/docker.sock"
  file:
    directory: "/etc/traefik/dynamic"
    watch: true

api:
  dashboard: false

log:
  level: INFO
accessLog: {}
`
	return []byte(tmpl), nil
}

// renderDnsmasqConfig wires *.<tld> to bindIP via dnsmasq.
func renderDnsmasqConfig(tld, bindIP string) ([]byte, error) {
	t := template.Must(template.New("dnsmasq").Parse(`# managed by pier
port=53
listen-address={{.IP}}
bind-interfaces
no-resolv
no-hosts
log-queries
log-facility=-
address=/{{.TLD}}/{{.IP}}
`))
	var buf bytes.Buffer
	if err := t.Execute(&buf, struct{ TLD, IP string }{tld, bindIP}); err != nil {
		return nil, fmt.Errorf("render dnsmasq.conf: %w", err)
	}
	return buf.Bytes(), nil
}

// renderResolvedDropin produces the systemd-resolved drop-in routing the
// .<tld> domain to dnsmasq.
func renderResolvedDropin(tld, dnsIP string) []byte {
	return fmt.Appendf(nil,
		"# managed by pier\n[Resolve]\nDNS=%s\nDomains=~%s\n",
		dnsIP, tld,
	)
}
