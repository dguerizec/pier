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

// renderDnsmasqConfig wires *.<tld> to answerIP via dnsmasq, listening on
// listenIP. listenIP and answerIP coincide in local mode; server mode
// listens on 0.0.0.0 (or a specific tailnet IP) and answers with the IP
// peers can reach.
func renderDnsmasqConfig(tld, listenIP, answerIP string) ([]byte, error) {
	t := template.Must(template.New("dnsmasq").Parse(`# managed by pier
port=53
listen-address={{.Listen}}
{{- if ne .Listen "0.0.0.0"}}
bind-interfaces
{{- end}}
no-resolv
no-hosts
log-queries
log-facility=-
address=/{{.TLD}}/{{.Answer}}
`))
	var buf bytes.Buffer
	if err := t.Execute(&buf, struct{ TLD, Listen, Answer string }{tld, listenIP, answerIP}); err != nil {
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
