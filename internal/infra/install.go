package infra

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	NetworkName       = "pier"
	TraefikContainer  = "pier-traefik"
	DnsmasqContainer  = "pier-dnsmasq"
	TraefikImage      = "traefik:v3"
	DnsmasqImage      = "4km3/dnsmasq:2.90-r3"
	DefaultTLD        = "test"
	DefaultLocalBind  = "127.0.0.1"
	DefaultServerBind = "0.0.0.0"
)

// ErrManualDNSNeeded is returned by configureHostDNS when the host is not
// using a stack pier knows how to drive automatically.
var ErrManualDNSNeeded = errors.New("infra: host DNS must be configured manually on this system")

// InstallOptions controls the bootstrap.
type InstallOptions struct {
	Mode      string // ModeLocal | ModeServer
	TLD       string
	BindIP    string // listen address override
	AnswerIP  string // A record returned by dnsmasq (server mode); auto-detected from tailscale when empty
	ManualDNS bool   // skip /etc/systemd/resolved.conf.d/pier.conf, print instructions instead
	Out       io.Writer

	// ExternalTraefik names a user-managed traefik container to register
	// workloads on instead of spawning pier-traefik. Triggers BYO mode.
	ExternalTraefik string
	// TraefikNetwork is the docker network the external traefik watches.
	// When ExternalTraefik is set and this is empty, pier auto-picks the
	// first non-default network attached to the external traefik.
	TraefikNetwork string
	// ExternalTraefikDynamicDir is the host-side file-provider directory
	// of the user's traefik. Persisted to Config so `pier serve` can
	// drop pier-dashboard.yml in the right place to expose
	// http://pier.<tld>. Empty in pier-managed mode (paths.TraefikDynamic
	// is used) and in BYO mode when discovery + the wizard prompt both
	// returned no path.
	ExternalTraefikDynamicDir string

	// HeadscaleContainer is the headscale docker container name when
	// detected by the install wizard. Persisted to Config so that
	// Uninstall can `docker restart` after Unpatching the split-DNS
	// rule. Empty when headscale isn't present on the host.
	HeadscaleContainer string
	// HeadscaleConfigPath records the headscale config.yaml path when
	// the install wizard patches split-DNS into it. Persisted to Config
	// so Uninstall can revert the patch.
	HeadscaleConfigPath string
}

// Install brings up the traefik + dnsmasq pair and (optionally) configures
// the host resolver. Idempotent: re-running stops and recreates containers.
func Install(opts InstallOptions) error {
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}

	if opts.Mode == "" {
		opts.Mode = ModeLocal
	}
	if opts.Mode != ModeLocal && opts.Mode != ModeServer {
		return fmt.Errorf("infra: --mode must be %q or %q (got %q)", ModeLocal, ModeServer, opts.Mode)
	}
	if opts.TLD == "" {
		opts.TLD = DefaultTLD
	}
	if opts.BindIP == "" {
		if opts.Mode == ModeServer {
			opts.BindIP = DefaultServerBind
		} else {
			opts.BindIP = DefaultLocalBind
		}
	}
	if opts.AnswerIP == "" {
		switch opts.Mode {
		case ModeLocal:
			opts.AnswerIP = opts.BindIP
		case ModeServer:
			ip, err := autoDetectTailnetIP()
			if err != nil {
				return fmt.Errorf("--mode server requires --answer-ip (auto-detect via tailscale failed: %w)", err)
			}
			fmt.Fprintf(out, "✓ auto-detected tailnet IP for --answer-ip: %s\n", ip)
			opts.AnswerIP = ip
		}
	}

	paths, err := DefaultPaths()
	if err != nil {
		return err
	}
	if err := paths.EnsureDirs(); err != nil {
		return fmt.Errorf("create config dirs: %w", err)
	}
	fmt.Fprintf(out, "✓ config dir: %s\n", paths.Root)

	d := newDocker()
	traefikNet := NetworkName
	byo := opts.ExternalTraefik != ""

	traefikConfChanged := false
	dnsmasqConfChanged := false

	if byo {
		net, err := resolveBYOTraefikNetwork(d, opts.ExternalTraefik, opts.TraefikNetwork)
		if err != nil {
			return err
		}
		traefikNet = net
		fmt.Fprintf(out, "✓ BYO-traefik: registering workloads on container %s via network %s\n", opts.ExternalTraefik, traefikNet)
	} else {
		traefikYAML, err := renderTraefikStatic()
		if err != nil {
			return err
		}
		traefikConfChanged, err = writeFileIfChanged(paths.TraefikStatic, traefikYAML, 0o644)
		if err != nil {
			return fmt.Errorf("write traefik.yml: %w", err)
		}
	}

	dnsmasqYAML, err := renderDnsmasqConfig(opts.TLD, opts.BindIP, opts.AnswerIP)
	if err != nil {
		return err
	}
	dnsmasqConfChanged, err = writeFileIfChanged(paths.DnsmasqConf, dnsmasqYAML, 0o644)
	if err != nil {
		return fmt.Errorf("write dnsmasq.conf: %w", err)
	}

	if !byo {
		if err := d.ensureNetwork(NetworkName); err != nil {
			return fmt.Errorf("docker network: %w", err)
		}
		fmt.Fprintf(out, "✓ docker network: %s\n", NetworkName)

		if d.imagePresent(TraefikImage) {
			fmt.Fprintf(out, "✓ image cached: %s\n", TraefikImage)
		} else {
			fmt.Fprintf(out, "  pulling %s...\n", TraefikImage)
			if err := d.pull(TraefikImage); err != nil {
				return err
			}
		}
	}
	if d.imagePresent(DnsmasqImage) {
		fmt.Fprintf(out, "✓ image cached: %s\n", DnsmasqImage)
	} else {
		fmt.Fprintf(out, "  pulling %s...\n", DnsmasqImage)
		if err := d.pull(DnsmasqImage); err != nil {
			return err
		}
	}

	if byo {
		// Switching from pier-managed to BYO: stop the old pier-traefik so
		// it doesn't shadow the user's. Same for the pier network if
		// nothing else is using it.
		_, _ = d.removeContainer(TraefikContainer)
		_, _ = d.removeNetwork(NetworkName)
	}

	if !byo {
		if err := ensureTraefikContainer(d, paths, opts.BindIP, traefikConfChanged, out); err != nil {
			return err
		}
	}

	if err := ensureDnsmasqContainer(d, paths, opts.BindIP, dnsmasqConfChanged, out); err != nil {
		return err
	}

	if !opts.ManualDNS {
		changed, err := configureHostDNS(opts.TLD, opts.BindIP)
		switch {
		case err == nil && changed:
			fmt.Fprintf(out, "✓ system DNS configured (.%s → %s)\n", opts.TLD, opts.BindIP)
		case err == nil:
			fmt.Fprintf(out, "✓ system DNS already configured (.%s → %s)\n", opts.TLD, opts.BindIP)
		case errors.Is(err, ErrManualDNSNeeded):
			fmt.Fprintf(out, "! system DNS not auto-configurable, falling back to manual:\n\n%s\n", manualDNSInstructions(opts.TLD, opts.BindIP))
		default:
			return err
		}
	} else {
		fmt.Fprintf(out, "! --manual-dns set; configure host DNS yourself:\n\n%s\n", manualDNSInstructions(opts.TLD, opts.BindIP))
	}

	// Server mode binds traefik on a specific (typically tailscale) IP.
	// At boot, docker may try to publish the port before tailscaled has
	// assigned the IP, silently lose the mapping, and leave the container
	// running with a ghost binding. The sysctl drop-in lets bind() succeed
	// against IPs that aren't yet on an interface — once tailscale brings
	// the IP up, packets flow normally. No-op for loopback/wildcard binds.
	changed, err := configureNonlocalBind(opts.BindIP)
	switch {
	case err == nil && changed:
		fmt.Fprintf(out, "✓ kernel: net.ipv4.ip_nonlocal_bind enabled (lets docker bind %s before tailscale assigns it)\n", opts.BindIP)
	case err == nil:
		// not applicable (loopback/wildcard) or already current — stay terse
	default:
		return err
	}

	cfg := &Config{
		Mode:                      opts.Mode,
		TLD:                       opts.TLD,
		BindIP:                    opts.BindIP,
		AnswerIP:                  opts.AnswerIP,
		TraefikNetwork:            traefikNet,
		ExternalTraefik:           opts.ExternalTraefik,
		ExternalTraefikDynamicDir: opts.ExternalTraefikDynamicDir,
		HeadscaleContainer:        opts.HeadscaleContainer,
		HeadscaleConfigPath:       opts.HeadscaleConfigPath,
	}
	if err := cfg.Save(paths); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	if !opts.ManualDNS {
		// Verify against the listen IP (where dnsmasq is reachable from this
		// host); the A record value is opts.AnswerIP.
		probeIP := opts.BindIP
		if probeIP == DefaultServerBind {
			// 0.0.0.0 isn't a usable resolver target; try 127.0.0.1 since
			// dnsmasq is also listening there (host network namespace).
			probeIP = "127.0.0.1"
		}
		if err := verifyDNS(opts.TLD, probeIP, opts.AnswerIP); err != nil {
			fmt.Fprintf(out, "! DNS verification failed (%v) — try `pier doctor` or run the manual steps above\n", err)
		} else {
			fmt.Fprintf(out, "✓ verified: anything.%s resolves to %s\n", opts.TLD, opts.AnswerIP)
		}
	}
	return nil
}

// writeFileIfChanged writes body to path with the given mode only when
// the existing content differs (or the file is missing). The bool
// return reports whether a write actually happened — callers use it to
// decide whether dependent containers must be recreated.
func writeFileIfChanged(path string, body []byte, mode os.FileMode) (bool, error) {
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, body) {
		return false, nil
	}
	if err := os.WriteFile(path, body, mode); err != nil {
		return false, err
	}
	return true, nil
}

// autoDetectTailnetIP returns the host's tailscale IPv4. Used as a default
// AnswerIP when --mode server is set without --answer-ip.
func autoDetectTailnetIP() (string, error) {
	out, err := exec.Command("tailscale", "ip", "-4").Output()
	if err != nil {
		return "", fmt.Errorf("tailscale ip: %w", err)
	}
	ip := strings.TrimSpace(string(out))
	if ip == "" {
		return "", errors.New("tailscale returned no IPv4 address")
	}
	return ip, nil
}

// Uninstall reverses Install. Best-effort: keeps going on individual errors
// so the user is left with a clean state even if one step fails. In BYO
// mode, leaves the user's traefik + network alone. The bool reports
// whether any pier-owned resource was actually removed; the CLI uses it
// (combined with the AI-skill outcome) to print a "nothing to uninstall"
// hint on a second invocation.
func Uninstall(out io.Writer, manualDNS bool) (bool, error) {
	if out == nil {
		out = os.Stdout
	}
	paths, err := DefaultPaths()
	if err != nil {
		return false, err
	}
	cfg, _ := LoadConfig(paths) // tolerate missing config: act conservatively

	d := newDocker()

	touched := false
	// pier-traefik / pier network only ours to remove when we managed them.
	// Helpers return (removed, err): we suppress the ✓ line when nothing
	// was there so a second `pier uninstall` doesn't lie about deleting
	// resources that are already gone.
	pierManaged := cfg == nil || cfg.ExternalTraefik == ""
	if pierManaged {
		if removed, err := d.removeContainer(TraefikContainer); err != nil {
			fmt.Fprintf(out, "! remove %s: %v\n", TraefikContainer, err)
		} else if removed {
			touched = true
			fmt.Fprintf(out, "✓ removed container %s\n", TraefikContainer)
		}
	}
	if removed, err := d.removeContainer(DnsmasqContainer); err != nil {
		fmt.Fprintf(out, "! remove %s: %v\n", DnsmasqContainer, err)
	} else if removed {
		touched = true
		fmt.Fprintf(out, "✓ removed container %s\n", DnsmasqContainer)
	}
	if pierManaged {
		if removed, err := d.removeNetwork(NetworkName); err != nil {
			fmt.Fprintf(out, "! remove network %s: %v\n", NetworkName, err)
		} else if removed {
			touched = true
			fmt.Fprintf(out, "✓ removed network %s\n", NetworkName)
		}
	}

	// In BYO mode `pier serve` may have written pier-dashboard.yml into
	// the user's traefik file-provider dir. The serve shutdown hook
	// removes it, but a crash (or `pier uninstall` while serve isn't
	// running) leaves it behind — and that yaml references config we're
	// about to wipe. Best-effort cleanup; we don't own the dir and the
	// user may have already deleted things.
	if cfg != nil && cfg.ExternalTraefikDynamicDir != "" {
		if removed, err := RemoveDashboardRoute(cfg.ExternalTraefikDynamicDir); err != nil {
			fmt.Fprintf(out, "! remove dashboard route from %s: %v\n", cfg.ExternalTraefikDynamicDir, err)
		} else if removed {
			touched = true
			fmt.Fprintf(out, "✓ removed dashboard route from %s\n", cfg.ExternalTraefikDynamicDir)
		}
	}

	if !manualDNS {
		if removed, err := unconfigureHostDNS(); err != nil {
			fmt.Fprintf(out, "! unconfigure host DNS: %v\n", err)
		} else if removed {
			touched = true
			fmt.Fprintf(out, "✓ host DNS reverted\n")
		}
	}

	// Mirror of the install-time sysctl drop-in. Removing the file +
	// re-reading sysctl from --system lets any other source (keepalived,
	// HAProxy, …) keep ownership of the setting; if nothing else sets it,
	// the kernel default reapplies on next boot.
	if removed, err := unconfigureNonlocalBind(); err != nil {
		fmt.Fprintf(out, "! unconfigure kernel sysctl: %v\n", err)
	} else if removed {
		touched = true
		fmt.Fprintf(out, "✓ removed kernel sysctl drop-in\n")
	}

	if _, err := os.Stat(paths.Root); err == nil {
		if err := os.RemoveAll(paths.Root); err != nil {
			fmt.Fprintf(out, "! remove %s: %v\n", paths.Root, err)
		} else {
			touched = true
			fmt.Fprintf(out, "✓ removed %s\n", paths.Root)
		}
	}
	return touched, nil
}

// resolveBYOTraefikNetwork validates the user-supplied container exists and
// is running, then returns the network name to register workloads on. If
// the user didn't pass --traefik-network, auto-pick the first non-default
// network the container is attached to.
func resolveBYOTraefikNetwork(d *docker, container, requestedNetwork string) (string, error) {
	out, err := d.run("inspect", "--format", "{{.State.Running}}", container)
	if err != nil {
		return "", fmt.Errorf("BYO-traefik: container %q not found: %w", container, err)
	}
	if strings.TrimSpace(out) != "true" {
		return "", fmt.Errorf("BYO-traefik: container %q is not running", container)
	}

	if requestedNetwork != "" {
		// Validate the network exists and the container is on it.
		if _, err := d.run("network", "inspect", requestedNetwork); err != nil {
			return "", fmt.Errorf("BYO-traefik: network %q not found: %w", requestedNetwork, err)
		}
		attached, err := d.run("inspect", "--format",
			"{{range $k, $_ := .NetworkSettings.Networks}}{{$k}} {{end}}", container)
		if err != nil {
			return "", err
		}
		if !containsToken(attached, requestedNetwork) {
			return "", fmt.Errorf("BYO-traefik: %s is not attached to network %q (attached: %s)",
				container, requestedNetwork, strings.TrimSpace(attached))
		}
		return requestedNetwork, nil
	}

	attached, err := d.run("inspect", "--format",
		"{{range $k, $_ := .NetworkSettings.Networks}}{{$k}} {{end}}", container)
	if err != nil {
		return "", err
	}
	for _, name := range strings.Fields(attached) {
		if name == "bridge" || name == "host" || name == "none" {
			continue
		}
		return name, nil
	}
	return "", fmt.Errorf("BYO-traefik: %s has no usable docker network — pass --traefik-network <name>", container)
}

func containsToken(haystack, needle string) bool {
	for _, t := range strings.Fields(haystack) {
		if t == needle {
			return true
		}
	}
	return false
}

// ensureTraefikContainer brings pier-traefik up, reusing the running
// container when the image and bind IP already match. Recreating costs
// a brief outage on the dev domain — skipping it on idempotent
// re-installs is what users expect.
func ensureTraefikContainer(d *docker, paths *Paths, bindIP string, confChanged bool, out io.Writer) error {
	running, image := d.containerStatus(TraefikContainer)
	if running && image == TraefikImage && !confChanged {
		current, _ := d.run("inspect", "--format",
			`{{(index (index .NetworkSettings.Ports "80/tcp") 0).HostIp}}`,
			TraefikContainer)
		if current == bindIP {
			fmt.Fprintf(out, "✓ traefik already up on %s:80\n", bindIP)
			return nil
		}
	}
	if _, err := d.removeContainer(TraefikContainer); err != nil {
		return fmt.Errorf("clean previous traefik: %w", err)
	}
	if _, err := d.run(traefikRunArgs(paths, bindIP)...); err != nil {
		return fmt.Errorf("start traefik: %w", err)
	}
	fmt.Fprintf(out, "✓ traefik up on %s:80\n", bindIP)
	return nil
}

// ensureDnsmasqContainer is the dnsmasq counterpart — same recycle
// heuristic. dnsmasq runs with --network host so the bind IP is
// inside its conf, not its docker port mapping; if the running
// container's image matches we trust the conf write to have updated
// the bind IP and we just skip the recreate.
func ensureDnsmasqContainer(d *docker, paths *Paths, bindIP string, confChanged bool, out io.Writer) error {
	running, image := d.containerStatus(DnsmasqContainer)
	if running && image == DnsmasqImage && !confChanged {
		fmt.Fprintf(out, "✓ dnsmasq already up on %s:53\n", bindIP)
		return nil
	}
	if _, err := d.removeContainer(DnsmasqContainer); err != nil {
		return fmt.Errorf("clean previous dnsmasq: %w", err)
	}
	if _, err := d.run(dnsmasqRunArgs(paths, bindIP)...); err != nil {
		return fmt.Errorf("start dnsmasq: %w", err)
	}
	fmt.Fprintf(out, "✓ dnsmasq up on %s:53\n", bindIP)
	return nil
}

func traefikRunArgs(paths *Paths, bindIP string) []string {
	return []string{
		"run", "-d",
		"--name", TraefikContainer,
		"--network", NetworkName,
		"--restart", "unless-stopped",
		"-p", fmt.Sprintf("%s:80:80", bindIP),
		"-v", "/var/run/docker.sock:/var/run/docker.sock:ro",
		"-v", paths.TraefikStatic + ":/etc/traefik/traefik.yml:ro",
		"-v", paths.TraefikDynamic + ":/etc/traefik/dynamic:ro",
		TraefikImage,
	}
}

// dnsmasqRunArgs uses --network host so dnsmasq binds the host's
// <bindIP>:53 directly. This avoids docker-proxy's well-known UDP reply
// quirks (queries arrive but replies get lost) and removes the need for
// CAP_NET_BIND_SERVICE since dnsmasq runs as root in the container before
// dropping privileges.
func dnsmasqRunArgs(paths *Paths, bindIP string) []string {
	return []string{
		"run", "-d",
		"--name", DnsmasqContainer,
		"--restart", "unless-stopped",
		"--network", "host",
		"-v", paths.DnsmasqConf + ":/etc/dnsmasq.conf:ro",
		DnsmasqImage,
	}
}

// verifyDNS issues a lookup against the dnsmasq container directly. probeIP
// is where dnsmasq is reachable from the running process; expectedIP is the
// A record value dnsmasq should return.
func verifyDNS(tld, probeIP, expectedIP string) error {
	deadline := time.Now().Add(5 * time.Second)
	host := fmt.Sprintf("anything.%s", tld)
	var lastErr error
	for time.Now().Before(deadline) {
		cmd := exec.Command("dig", "+short", "+time=1", "+tries=1", "@"+probeIP, host)
		out, err := cmd.Output()
		if err == nil {
			answer := strings.TrimSpace(string(out))
			if answer == expectedIP {
				return nil
			}
			lastErr = fmt.Errorf("expected %s, got %q", expectedIP, answer)
		} else {
			lastErr = err
		}
		time.Sleep(200 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = errors.New("timeout")
	}
	return lastErr
}
