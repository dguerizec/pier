package infra

import (
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
	BindIP    string // optional override
	ManualDNS bool   // skip /etc/systemd/resolved.conf.d/pier.conf, print instructions instead
	Out       io.Writer

	// ExternalTraefik names a user-managed traefik container to register
	// workloads on instead of spawning pier-traefik. Triggers BYO mode.
	ExternalTraefik string
	// TraefikNetwork is the docker network the external traefik watches.
	// When ExternalTraefik is set and this is empty, pier auto-picks the
	// first non-default network attached to the external traefik.
	TraefikNetwork string
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
	if opts.Mode != ModeLocal {
		return fmt.Errorf("infra: only --mode local is supported in MVP (got %q)", opts.Mode)
	}
	if opts.TLD == "" {
		opts.TLD = DefaultTLD
	}
	if opts.BindIP == "" {
		opts.BindIP = DefaultLocalBind
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
		if err := os.WriteFile(paths.TraefikStatic, traefikYAML, 0o644); err != nil {
			return fmt.Errorf("write traefik.yml: %w", err)
		}
	}

	dnsmasqYAML, err := renderDnsmasqConfig(opts.TLD, opts.BindIP)
	if err != nil {
		return err
	}
	if err := os.WriteFile(paths.DnsmasqConf, dnsmasqYAML, 0o644); err != nil {
		return fmt.Errorf("write dnsmasq.conf: %w", err)
	}

	if !byo {
		if err := d.ensureNetwork(NetworkName); err != nil {
			return fmt.Errorf("docker network: %w", err)
		}
		fmt.Fprintf(out, "✓ docker network: %s\n", NetworkName)

		fmt.Fprintf(out, "  pulling %s...\n", TraefikImage)
		if err := d.pull(TraefikImage); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "  pulling %s...\n", DnsmasqImage)
	if err := d.pull(DnsmasqImage); err != nil {
		return err
	}

	if !byo {
		if err := d.removeContainer(TraefikContainer); err != nil {
			return fmt.Errorf("clean previous traefik: %w", err)
		}
	} else {
		// Switching from pier-managed to BYO: stop the old pier-traefik so
		// it doesn't shadow the user's. Same for the pier network if
		// nothing else is using it.
		_ = d.removeContainer(TraefikContainer)
		_ = d.removeNetwork(NetworkName)
	}
	if err := d.removeContainer(DnsmasqContainer); err != nil {
		return fmt.Errorf("clean previous dnsmasq: %w", err)
	}

	if !byo {
		if _, err := d.run(traefikRunArgs(paths, opts.BindIP)...); err != nil {
			return fmt.Errorf("start traefik: %w", err)
		}
		fmt.Fprintf(out, "✓ traefik up on %s:80\n", opts.BindIP)
	}

	if _, err := d.run(dnsmasqRunArgs(paths, opts.BindIP)...); err != nil {
		return fmt.Errorf("start dnsmasq: %w", err)
	}
	fmt.Fprintf(out, "✓ dnsmasq up on %s:53\n", opts.BindIP)

	if !opts.ManualDNS {
		switch err := configureHostDNS(opts.TLD, opts.BindIP); {
		case err == nil:
			fmt.Fprintf(out, "✓ system DNS configured (.%s → %s)\n", opts.TLD, opts.BindIP)
		case errors.Is(err, ErrManualDNSNeeded):
			fmt.Fprintf(out, "! system DNS not auto-configurable, falling back to manual:\n\n%s\n", manualDNSInstructions(opts.TLD, opts.BindIP))
		default:
			return err
		}
	} else {
		fmt.Fprintf(out, "! --manual-dns set; configure host DNS yourself:\n\n%s\n", manualDNSInstructions(opts.TLD, opts.BindIP))
	}

	cfg := &Config{
		Mode:            opts.Mode,
		TLD:             opts.TLD,
		BindIP:          opts.BindIP,
		TraefikNetwork:  traefikNet,
		ExternalTraefik: opts.ExternalTraefik,
	}
	if err := cfg.Save(paths); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	if !opts.ManualDNS {
		if err := verifyDNS(opts.TLD, opts.BindIP); err != nil {
			fmt.Fprintf(out, "! DNS verification failed (%v) — try `pier doctor` or run the manual steps above\n", err)
		} else {
			fmt.Fprintf(out, "✓ verified: anything.%s resolves to %s\n", opts.TLD, opts.BindIP)
		}
	}
	return nil
}

// Uninstall reverses Install. Best-effort: keeps going on individual errors
// so the user is left with a clean state even if one step fails. In BYO
// mode, leaves the user's traefik + network alone.
func Uninstall(out io.Writer, manualDNS bool) error {
	if out == nil {
		out = os.Stdout
	}
	paths, err := DefaultPaths()
	if err != nil {
		return err
	}
	cfg, _ := LoadConfig(paths) // tolerate missing config: act conservatively

	d := newDocker()

	// pier-traefik / pier network only ours to remove when we managed them.
	pierManaged := cfg == nil || cfg.ExternalTraefik == ""
	if pierManaged {
		if err := d.removeContainer(TraefikContainer); err != nil {
			fmt.Fprintf(out, "! remove %s: %v\n", TraefikContainer, err)
		} else {
			fmt.Fprintf(out, "✓ removed container %s\n", TraefikContainer)
		}
	}
	if err := d.removeContainer(DnsmasqContainer); err != nil {
		fmt.Fprintf(out, "! remove %s: %v\n", DnsmasqContainer, err)
	} else {
		fmt.Fprintf(out, "✓ removed container %s\n", DnsmasqContainer)
	}
	if pierManaged {
		if err := d.removeNetwork(NetworkName); err != nil {
			fmt.Fprintf(out, "! remove network %s: %v\n", NetworkName, err)
		} else {
			fmt.Fprintf(out, "✓ removed network %s\n", NetworkName)
		}
	}

	if !manualDNS {
		if err := unconfigureHostDNS(); err != nil {
			fmt.Fprintf(out, "! unconfigure host DNS: %v\n", err)
		} else {
			fmt.Fprintf(out, "✓ host DNS reverted\n")
		}
	}

	if err := os.RemoveAll(paths.Root); err != nil {
		fmt.Fprintf(out, "! remove %s: %v\n", paths.Root, err)
	} else {
		fmt.Fprintf(out, "✓ removed %s\n", paths.Root)
	}
	return nil
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

// verifyDNS issues a lookup against the dnsmasq container directly. We hit
// 127.0.0.1:53 (or whatever bindIP is) rather than the host resolver so the
// check is decoupled from systemd-resolved propagation timing.
func verifyDNS(tld, bindIP string) error {
	deadline := time.Now().Add(5 * time.Second)
	host := fmt.Sprintf("anything.%s", tld)
	var lastErr error
	for time.Now().Before(deadline) {
		cmd := exec.Command("dig", "+short", "+time=1", "+tries=1", "@"+bindIP, host)
		out, err := cmd.Output()
		if err == nil {
			answer := strings.TrimSpace(string(out))
			if answer == bindIP {
				return nil
			}
			lastErr = fmt.Errorf("expected %s, got %q", bindIP, answer)
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
