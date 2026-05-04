// Package detect inspects the host to surface infrastructure pier can
// integrate with: tailscale, existing reverse proxies, headscale. The
// install wizard uses this to propose sensible defaults; commands that
// need exact knowledge still take explicit flags.
package detect

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"gopkg.in/yaml.v3"
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

// TraefikInfo describes a candidate existing traefik instance pier could
// register on. Only populated when exactly one likely candidate is found
// to avoid making implicit choices on multi-traefik hosts.
type TraefikInfo struct {
	Found     bool
	Container string // docker container name; empty when detected via host process scan
	Network   string // docker network the container is on; empty for host-process traefik
	// DynamicDir is the host-side path of the file-provider directory
	// the traefik instance watches. Used by `pier serve` to drop
	// pier-dashboard.yml so http://pier.<tld> resolves through the
	// existing traefik. Empty when discovery couldn't pin it down — the
	// install wizard then asks the user.
	DynamicDir string
}

// HeadscaleInfo locates a running headscale instance and where its config
// lives, so install can offer to patch it.
type HeadscaleInfo struct {
	Found      bool
	Container  string // container name when running in docker
	ConfigPath string // path on the host (resolved through bind mounts when applicable)
	// BaseDomain is the value of dns.base_domain (0.26+) or
	// dns_config.base_domain (legacy) in the headscale config — the
	// MagicDNS suffix tailnet hostnames already resolve under.
	BaseDomain string
	// RecordsPath is the host path of the file referenced by
	// extra_records_path in the headscale config (resolved through bind
	// mounts), where pier's records adapter writes per-slug A records.
	// Empty when extra_records_path is not configured.
	RecordsPath string
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

// detectTraefik finds an existing traefik instance pier could register
// on. The docker code path is tried first (most common deployment); if
// nothing is found there we fall back to scanning host processes so
// systemd / package-manager installs aren't invisible.
func detectTraefik() TraefikInfo {
	if info := detectTraefikDocker(); info.Found {
		return info
	}
	return detectTraefikProcess()
}

// detectTraefikDocker walks `docker ps` for a traefik container that
// isn't pier's own (`pier-traefik`), excluding Found=true when more
// than one candidate exists so the wizard asks the user.
//
// `docker ps --filter ancestor=traefik` doesn't match versioned tags, so
// we list all containers and match the image string ourselves.
func detectTraefikDocker() TraefikInfo {
	out, err := exec.Command("docker", "ps", "--format", "{{.Names}}\t{{.Image}}").Output()
	if err != nil {
		return TraefikInfo{}
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		name, image := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if name == "" || name == "pier-traefik" {
			continue
		}
		// Match `traefik:<tag>` or bare `traefik`.
		base := strings.SplitN(image, ":", 2)[0]
		if base != "traefik" {
			continue
		}
		names = append(names, name)
	}
	if len(names) != 1 {
		return TraefikInfo{}
	}
	name := names[0]

	// Resolve the most likely network to register workloads on (skip
	// docker-default ones).
	netOut, err := exec.Command("docker", "inspect", "--format",
		"{{range $k, $_ := .NetworkSettings.Networks}}{{$k}} {{end}}", name).Output()
	info := TraefikInfo{Found: true, Container: name}
	if err == nil {
		for _, n := range strings.Fields(string(netOut)) {
			switch n {
			case "bridge", "host", "none":
				continue
			}
			info.Network = n
			break
		}
	}

	args := containerArgs(name)
	info.DynamicDir = extractTraefikDynamicDir(args, func(p string) string {
		return resolveContainerPath(name, p)
	})
	return info
}

// detectTraefikProcess scans host processes for a traefik binary.
// Useful for systemd / package-manager installs where there is no
// docker container to inspect. Skipped on platforms without a
// `ps -eo args` equivalent (silently returns Found=false).
//
// On Linux the host PID namespace also surfaces processes living
// inside docker containers, so a pier-managed traefik would look
// like a host process. We filter those out by inspecting
// /proc/<pid>/cgroup — anything cgroup-attached to docker /
// containerd is somebody else's problem.
func detectTraefikProcess() TraefikInfo {
	out, err := exec.Command("ps", "-eo", "pid=,args=").Output()
	if err != nil {
		return TraefikInfo{}
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, args := fields[0], fields[1:]
		// Match `traefik`, `/usr/local/bin/traefik`, etc. — but not
		// `traefik-foo` or other tools that happen to contain the
		// substring.
		base := args[0]
		if i := strings.LastIndex(base, "/"); i >= 0 {
			base = base[i+1:]
		}
		if base != "traefik" {
			continue
		}
		if processInContainer(pid) {
			continue
		}
		info := TraefikInfo{Found: true}
		info.DynamicDir = extractTraefikDynamicDir(args[1:], func(p string) string { return p })
		return info
	}
	return TraefikInfo{}
}

// processInContainer reports whether the pid lives in a docker /
// containerd / podman cgroup, by reading /proc/<pid>/cgroup. Returns
// false on any read error so the host-process detection still works
// on systems without procfs (non-Linux, restricted environments) —
// the false positive there is a smaller failure mode than missing a
// real host traefik.
func processInContainer(pid string) bool {
	body, err := os.ReadFile("/proc/" + pid + "/cgroup")
	if err != nil {
		return false
	}
	s := string(body)
	return strings.Contains(s, "/docker/") ||
		strings.Contains(s, "/docker-") ||
		strings.Contains(s, "/containerd/") ||
		strings.Contains(s, "/libpod-") ||
		strings.Contains(s, ".scope/payload")
}

// splitFlag splits a single argv entry on the first `=` so callers can
// match the flag name case-insensitively without re-implementing the
// boundary. Returns (flag, value, hasValue) — hasValue is false when
// the entry has no `=`, in which case value is empty.
func splitFlag(arg string) (flag, value string, hasValue bool) {
	if i := strings.IndexByte(arg, '='); i >= 0 {
		return arg[:i], arg[i+1:], true
	}
	return arg, "", false
}

// containerArgs returns the command line of a docker container as a
// slice of strings (entrypoint + args, in argv order). Empty on any
// error so callers can keep going with whatever they already have.
func containerArgs(name string) []string {
	out, err := exec.Command("docker", "inspect", "--format",
		`{{range .Config.Entrypoint}}{{.}}{{"\n"}}{{end}}{{range .Config.Cmd}}{{.}}{{"\n"}}{{end}}{{range .Args}}{{.}}{{"\n"}}{{end}}`,
		name).Output()
	if err != nil {
		return nil
	}
	var argv []string
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		argv = append(argv, line)
	}
	return argv
}

// extractTraefikDynamicDir scans traefik's argv (and its static
// configFile when referenced) for providers.file.directory, returning
// the host-side absolute path. resolvePath maps a path as seen by the
// traefik instance to its host-side equivalent — identity for a host
// process, bind-mount lookup for a docker container.
//
// We deliberately ignore providers.file.filename: pier needs to drop
// its own yaml as a sibling file, which is not how filename mode
// works (single static file). Wizard prompts the user in that case.
func extractTraefikDynamicDir(argv []string, resolvePath func(string) string) string {
	configFile := ""
	for i, a := range argv {
		flag, value, hasValue := splitFlag(a)
		lf := strings.ToLower(flag)
		switch lf {
		case "--providers.file.directory":
			if hasValue {
				return resolvePath(value)
			}
			if i+1 < len(argv) {
				return resolvePath(argv[i+1])
			}
		case "--configfile":
			if hasValue {
				configFile = value
			} else if i+1 < len(argv) {
				configFile = argv[i+1]
			}
		}
	}
	if configFile == "" {
		return ""
	}
	hostCfg := resolvePath(configFile)
	body, err := os.ReadFile(hostCfg)
	if err != nil {
		return ""
	}
	var stub struct {
		Providers struct {
			File struct {
				Directory string `yaml:"directory"`
			} `yaml:"file"`
		} `yaml:"providers"`
	}
	if err := yaml.Unmarshal(body, &stub); err != nil {
		return ""
	}
	if stub.Providers.File.Directory == "" {
		return ""
	}
	return resolvePath(stub.Providers.File.Directory)
}

// ----- headscale -----

// detectHeadscale locates a headscale container and resolves its config file
// on the host filesystem (via the container's bind mounts).
func detectHeadscale() HeadscaleInfo {
	out, err := exec.Command("docker", "ps", "--format", "{{.Names}}\t{{.Image}}").Output()
	if err != nil {
		return HeadscaleInfo{}
	}
	name := ""
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		image := strings.TrimSpace(parts[1])
		base := strings.SplitN(image, ":", 2)[0]
		if base == "headscale/headscale" || base == "headscale" {
			name = strings.TrimSpace(parts[0])
			break
		}
	}
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
	info := HeadscaleInfo{Found: true, Container: name, ConfigPath: configPath}
	if configPath != "" {
		info.BaseDomain = readHeadscaleBaseDomain(configPath)
		if rp := readHeadscaleRecordsPath(configPath); rp != "" {
			info.RecordsPath = resolveContainerPath(name, rp)
		}
	}
	return info
}

// readHeadscaleRecordsPath extracts dns.extra_records_path (or the legacy
// dns_config.extra_records_path) from cfgPath. Returns "" when unset.
func readHeadscaleRecordsPath(cfgPath string) string {
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		return ""
	}
	var stub struct {
		DNS struct {
			ExtraRecordsPath string `yaml:"extra_records_path"`
		} `yaml:"dns"`
		DNSConfig struct {
			ExtraRecordsPath string `yaml:"extra_records_path"`
		} `yaml:"dns_config"`
	}
	if err := yaml.Unmarshal(body, &stub); err != nil {
		return ""
	}
	if stub.DNS.ExtraRecordsPath != "" {
		return stub.DNS.ExtraRecordsPath
	}
	return stub.DNSConfig.ExtraRecordsPath
}

// resolveContainerPath maps a path inside the container to its source on
// the host by walking the container's bind mounts. Returns the input path
// unchanged when no matching mount is found (caller may still try it as a
// host path if it happens to be absolute and accessible).
func resolveContainerPath(container, containerPath string) string {
	out, err := exec.Command("docker", "inspect", "--format",
		`{{range .Mounts}}{{.Source}} -> {{.Destination}}{{"\n"}}{{end}}`, container).Output()
	if err != nil {
		return containerPath
	}
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.SplitN(line, " -> ", 2)
		if len(parts) != 2 {
			continue
		}
		host, dest := parts[0], parts[1]
		if dest == containerPath {
			return host
		}
		// Mount on a parent dir: rewrite the path to the host equivalent.
		if strings.HasPrefix(containerPath, dest+"/") {
			return host + strings.TrimPrefix(containerPath, dest)
		}
	}
	return containerPath
}

// readHeadscaleBaseDomain extracts dns.base_domain (or dns_config.base_domain
// on older headscale releases) from cfgPath. Returns "" on any error.
func readHeadscaleBaseDomain(cfgPath string) string {
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		return ""
	}
	var stub struct {
		DNS struct {
			BaseDomain string `yaml:"base_domain"`
		} `yaml:"dns"`
		DNSConfig struct {
			BaseDomain string `yaml:"base_domain"`
		} `yaml:"dns_config"`
	}
	if err := yaml.Unmarshal(body, &stub); err != nil {
		return ""
	}
	if stub.DNS.BaseDomain != "" {
		return stub.DNS.BaseDomain
	}
	return stub.DNSConfig.BaseDomain
}

// Summary returns a human-readable one-line summary per detected component.
func (e Environment) Summary() []string {
	var lines []string
	if e.Tailscale.Active {
		lines = append(lines, fmt.Sprintf("✓ tailscale: %s on %s", e.Tailscale.IPv4, emptyAs(e.Tailscale.Tailnet, "?")))
	}
	if e.Traefik.Found {
		who := "container=" + emptyAs(e.Traefik.Container, "<host process>")
		extra := ""
		if e.Traefik.Network != "" {
			extra = " network=" + e.Traefik.Network
		}
		if e.Traefik.DynamicDir != "" {
			extra += " dynamic_dir=" + e.Traefik.DynamicDir
		}
		lines = append(lines, fmt.Sprintf("✓ existing traefik: %s%s", who, extra))
	}
	if e.Headscale.Found {
		extra := ""
		if e.Headscale.BaseDomain != "" {
			extra += " base_domain=" + e.Headscale.BaseDomain
		}
		if e.Headscale.RecordsPath != "" {
			extra += " records=" + e.Headscale.RecordsPath
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
