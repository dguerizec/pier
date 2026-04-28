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

// Status grades a single check's outcome. Pass means healthy, Warn means
// non-fatal degradation, Fail means a user-visible feature is broken.
type Status int

const (
	StatusPass Status = iota
	StatusWarn
	StatusFail
)

func (s Status) String() string {
	switch s {
	case StatusPass:
		return "✓"
	case StatusWarn:
		return "!"
	case StatusFail:
		return "✗"
	}
	return "?"
}

// Check is a single line of the doctor report.
type Check struct {
	Name   string
	Status Status
	Detail string
	// FixHint is the one-liner the user should run when auto-fix didn't
	// recover this check (or doctor was invoked without --fix).
	FixHint string
}

// Report is the doctor output.
type Report struct {
	Checks  []Check
	Actions []string // populated by Fix() to record what was attempted
}

func (r Report) HasFailures() bool {
	for _, c := range r.Checks {
		if c.Status == StatusFail {
			return true
		}
	}
	return false
}

func (r Report) Print(out io.Writer) {
	for _, c := range r.Checks {
		fmt.Fprintf(out, "%s %s\n", c.Status, c.Name)
		if c.Detail != "" {
			fmt.Fprintf(out, "  %s\n", c.Detail)
		}
		if c.FixHint != "" && c.Status != StatusPass {
			fmt.Fprintf(out, "  hint: %s\n", c.FixHint)
		}
	}
	for _, a := range r.Actions {
		fmt.Fprintf(out, "→ %s\n", a)
	}
}

// Diagnose runs the read-only infra checks. It does not look at the state DB
// or running workloads — those live in the cli layer (which already imports
// both infra and state).
func Diagnose() Report {
	r := Report{}
	paths, err := DefaultPaths()
	if err != nil {
		r.Checks = append(r.Checks, Check{Name: "config paths", Status: StatusFail, Detail: err.Error()})
		return r
	}
	cfg, err := LoadConfig(paths)
	if err != nil {
		r.Checks = append(r.Checks, Check{
			Name:    "pier installed",
			Status:  StatusFail,
			Detail:  err.Error(),
			FixHint: "pier install --mode local",
		})
		return r
	}

	r.Checks = append(r.Checks, checkConfigDir(paths))
	r.Checks = append(r.Checks, checkDocker())
	r.Checks = append(r.Checks, checkNetwork(NetworkName))
	r.Checks = append(r.Checks, checkContainerRunning(TraefikContainer))
	r.Checks = append(r.Checks, checkContainerRunning(DnsmasqContainer))
	r.Checks = append(r.Checks, checkDNSResolution(cfg.TLD, cfg.BindIP))
	r.Checks = append(r.Checks, checkResolvedDropin(cfg.TLD))
	return r
}

// Fix attempts to recover from the failures Diagnose reports. Returns a new
// report after the fixes have run.
func Fix() Report {
	paths, err := DefaultPaths()
	if err != nil {
		return Report{Checks: []Check{{Name: "config paths", Status: StatusFail, Detail: err.Error()}}}
	}
	cfg, err := LoadConfig(paths)
	if err != nil {
		return Diagnose()
	}

	report := Report{}

	// Step 1 — fix infra. Network, containers, drop-in, in that order
	// because each step can depend on the previous.
	d := newDocker()
	if err := d.ensureNetwork(NetworkName); err == nil {
		report.Actions = append(report.Actions, "ensured docker network "+NetworkName)
	}

	for _, c := range []struct {
		name string
		args func(*Paths, string) []string
	}{
		{TraefikContainer, traefikRunArgs},
		{DnsmasqContainer, dnsmasqRunArgs},
	} {
		if running := containerIsRunning(c.name); !running {
			_ = d.removeContainer(c.name)
			if _, err := d.run(c.args(paths, cfg.BindIP)...); err == nil {
				report.Actions = append(report.Actions, "restarted container "+c.name)
			}
		}
	}

	// Wait briefly for containers to settle before re-checking DNS.
	time.Sleep(500 * time.Millisecond)

	if err := verifyDNS(cfg.TLD, cfg.BindIP); err != nil {
		// Try restarting dnsmasq once more then re-verify.
		_, _ = d.run("restart", DnsmasqContainer)
		time.Sleep(500 * time.Millisecond)
	}

	// Step 2 — drop-in. Only re-write if the file is missing or
	// content-stale. configureHostDNS is interactive (sudo); we only call
	// it when the drop-in is actually wrong.
	if needsResolvedRewrite(cfg.TLD, cfg.BindIP) {
		if err := configureHostDNS(cfg.TLD, cfg.BindIP); err == nil {
			report.Actions = append(report.Actions, "rewrote systemd-resolved drop-in")
		}
	}

	final := Diagnose()
	final.Actions = append(report.Actions, final.Actions...)
	return final
}

// individual checks

func checkConfigDir(p *Paths) Check {
	if _, err := os.Stat(p.Root); err != nil {
		return Check{Name: "config dir", Status: StatusFail, Detail: err.Error(), FixHint: "pier install"}
	}
	// Touch a sentinel to confirm writable.
	probe, err := os.CreateTemp(p.Root, "doctor-*")
	if err != nil {
		return Check{Name: "config dir", Status: StatusFail, Detail: "not writable: " + err.Error()}
	}
	probe.Close()
	os.Remove(probe.Name())
	return Check{Name: "config dir " + p.Root, Status: StatusPass}
}

func checkDocker() Check {
	if err := exec.Command("docker", "info").Run(); err != nil {
		return Check{
			Name:    "docker daemon reachable",
			Status:  StatusFail,
			Detail:  err.Error(),
			FixHint: "is docker installed and running? `systemctl status docker`",
		}
	}
	return Check{Name: "docker daemon reachable", Status: StatusPass}
}

func checkNetwork(name string) Check {
	out, err := exec.Command("docker", "network", "inspect", name).Output()
	if err != nil {
		return Check{
			Name:    "docker network " + name,
			Status:  StatusFail,
			Detail:  "missing",
			FixHint: "pier doctor --fix  (or `docker network create " + name + "`)",
		}
	}
	_ = out
	return Check{Name: "docker network " + name, Status: StatusPass}
}

func checkContainerRunning(name string) Check {
	if !containerIsRunning(name) {
		return Check{
			Name:    "container " + name,
			Status:  StatusFail,
			Detail:  "not running",
			FixHint: "pier doctor --fix  (or `docker start " + name + "`)",
		}
	}
	return Check{Name: "container " + name, Status: StatusPass}
}

func containerIsRunning(name string) bool {
	out, err := exec.Command("docker", "inspect", "--format", "{{.State.Running}}", name).Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

func checkDNSResolution(tld, bindIP string) Check {
	if err := verifyDNS(tld, bindIP); err != nil {
		return Check{
			Name:    fmt.Sprintf("dnsmasq answers anything.%s", tld),
			Status:  StatusFail,
			Detail:  err.Error(),
			FixHint: "pier doctor --fix  (will restart pier-dnsmasq)",
		}
	}
	return Check{Name: fmt.Sprintf("dnsmasq answers anything.%s", tld), Status: StatusPass}
}

func checkResolvedDropin(tld string) Check {
	body, err := os.ReadFile(resolvedDropinPath)
	if errors.Is(err, os.ErrNotExist) {
		return Check{
			Name:    "systemd-resolved drop-in",
			Status:  StatusFail,
			Detail:  resolvedDropinPath + " missing",
			FixHint: "pier doctor --fix  (re-runs the sudo install step)",
		}
	}
	if err != nil {
		return Check{Name: "systemd-resolved drop-in", Status: StatusWarn, Detail: err.Error()}
	}
	if !strings.Contains(string(body), "Domains=~"+tld) {
		return Check{
			Name:    "systemd-resolved drop-in",
			Status:  StatusFail,
			Detail:  "Domains=~" + tld + " not found",
			FixHint: "pier doctor --fix",
		}
	}
	return Check{Name: "systemd-resolved drop-in", Status: StatusPass}
}

// needsResolvedRewrite returns true when the on-disk drop-in is missing or
// references a different (TLD, bindIP) than the active config.
func needsResolvedRewrite(tld, bindIP string) bool {
	body, err := os.ReadFile(resolvedDropinPath)
	if err != nil {
		return true
	}
	want := string(renderResolvedDropin(tld, bindIP))
	return string(body) != want
}
