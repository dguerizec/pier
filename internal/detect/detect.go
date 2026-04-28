// Package detect inspects the host to surface infrastructure pier can
// integrate with: tailscale, existing reverse proxies, headscale. The
// install wizard uses this to propose sensible defaults; commands that
// need exact knowledge still take explicit flags.
package detect

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Environment captures everything the install wizard cares about.
type Environment struct {
	Tailscale TailscaleInfo
	Traefik   TraefikInfo
	Headscale HeadscaleInfo
}

// TailscaleInfo summarizes the local tailscale state.
type TailscaleInfo struct {
	Active bool
	IPv4   string // e.g. 100.64.0.10
	Tailnet string
}

// TraefikInfo describes a candidate existing traefik container pier could
// register on. Only populated when exactly one likely candidate is found
// to avoid making implicit choices on multi-traefik hosts.
type TraefikInfo struct {
	Found     bool
	Container string
	Network   string
}

// HeadscaleInfo locates a running headscale instance and where its config
// lives, so install can offer to patch it.
type HeadscaleInfo struct {
	Found        bool
	Container    string // container name when running in docker
	ConfigPath   string // path on the host (resolved through bind mounts when applicable)
}

// Run probes everything. Best-effort: every sub-detector is independent and
// tolerant of failures.
func Run() Environment {
	return Environment{
		Tailscale: detectTailscale(),
		Traefik:   detectTraefik(),
		Headscale: detectHeadscale(),
	}
}

// ----- tailscale -----

func detectTailscale() TailscaleInfo {
	if _, err := exec.LookPath("tailscale"); err != nil {
		return TailscaleInfo{}
	}
	out, err := exec.Command("tailscale", "status", "--json").Output()
	if err != nil {
		return TailscaleInfo{}
	}
	var s struct {
		Self struct {
			TailscaleIPs []string `json:"TailscaleIPs"`
		} `json:"Self"`
		MagicDNSSuffix string `json:"MagicDNSSuffix"`
		CurrentTailnet struct {
			Name string `json:"Name"`
		} `json:"CurrentTailnet"`
	}
	if err := json.Unmarshal(out, &s); err != nil {
		return TailscaleInfo{}
	}
	info := TailscaleInfo{Active: true}
	for _, ip := range s.Self.TailscaleIPs {
		if !strings.Contains(ip, ":") {
			info.IPv4 = ip
			break
		}
	}
	if s.CurrentTailnet.Name != "" {
		info.Tailnet = s.CurrentTailnet.Name
	} else {
		info.Tailnet = strings.TrimSuffix(s.MagicDNSSuffix, ".")
	}
	return info
}

// ----- traefik -----

// detectTraefik finds an existing traefik container, distinct from the one
// pier may have spun up itself (`pier-traefik`). When more than one
// candidate exists we return Found=false so the wizard asks the user.
func detectTraefik() TraefikInfo {
	out, err := exec.Command("docker", "ps",
		"--filter", "ancestor=traefik",
		"--format", "{{.Names}}").Output()
	if err != nil {
		return TraefikInfo{}
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		n := strings.TrimSpace(line)
		if n == "" || n == "pier-traefik" {
			continue
		}
		names = append(names, n)
	}
	if len(names) != 1 {
		return TraefikInfo{}
	}

	// Resolve the most likely network to register workloads on (skip
	// docker-default ones).
	netOut, err := exec.Command("docker", "inspect", "--format",
		"{{range $k, $_ := .NetworkSettings.Networks}}{{$k}} {{end}}", names[0]).Output()
	if err != nil {
		return TraefikInfo{Found: true, Container: names[0]}
	}
	var network string
	for _, n := range strings.Fields(string(netOut)) {
		switch n {
		case "bridge", "host", "none":
			continue
		}
		network = n
		break
	}
	return TraefikInfo{Found: true, Container: names[0], Network: network}
}

// ----- headscale -----

// detectHeadscale locates a headscale container and resolves its config file
// on the host filesystem (via the container's bind mounts).
func detectHeadscale() HeadscaleInfo {
	out, err := exec.Command("docker", "ps",
		"--filter", "ancestor=headscale/headscale",
		"--format", "{{.Names}}").Output()
	if err != nil {
		return HeadscaleInfo{}
	}
	name := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if name == "" {
		return HeadscaleInfo{}
	}

	// Look for /etc/headscale/config.yaml mounted from the host.
	mountsOut, err := exec.Command("docker", "inspect", "--format",
		`{{range .Mounts}}{{.Source}} -> {{.Destination}}{{"\n"}}{{end}}`, name).Output()
	if err != nil {
		return HeadscaleInfo{Found: true, Container: name}
	}
	configPath := ""
	for _, line := range strings.Split(string(mountsOut), "\n") {
		parts := strings.SplitN(line, " -> ", 2)
		if len(parts) != 2 {
			continue
		}
		host, dest := parts[0], parts[1]
		if dest == "/etc/headscale" {
			configPath = host + "/config.yaml"
			break
		}
		if dest == "/etc/headscale/config.yaml" {
			configPath = host
			break
		}
	}
	return HeadscaleInfo{Found: true, Container: name, ConfigPath: configPath}
}

// Summary returns a human-readable one-line summary per detected component.
func (e Environment) Summary() []string {
	var lines []string
	if e.Tailscale.Active {
		lines = append(lines, fmt.Sprintf("✓ tailscale: %s on %s", e.Tailscale.IPv4, emptyAs(e.Tailscale.Tailnet, "?")))
	}
	if e.Traefik.Found {
		lines = append(lines, fmt.Sprintf("✓ existing traefik: container=%s network=%s", e.Traefik.Container, emptyAs(e.Traefik.Network, "?")))
	}
	if e.Headscale.Found {
		extra := ""
		if e.Headscale.ConfigPath != "" {
			extra = " config=" + e.Headscale.ConfigPath
		}
		lines = append(lines, fmt.Sprintf("✓ headscale: container=%s%s", e.Headscale.Container, extra))
	}
	if len(lines) == 0 {
		lines = append(lines, "no tailscale, traefik or headscale detected — defaulting to local-mode pier")
	}
	return lines
}

func emptyAs(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
