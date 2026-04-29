// Package initwizard derives a .pier.toml from project introspection.
// It is split from internal/cli so the logic can be unit-tested without
// dragging in the cobra command wiring.
package initwizard

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ComposeCandidate is one (service, container_port) pair extracted from
// the compose file's `services.<name>.ports` entries.
type ComposeCandidate struct {
	Service string
	Port    int
}

// DetectComposeFile resolves the compose file pier should use. When
// override is non-empty it is honoured (relative paths resolve against
// toplevel); otherwise the conventional candidates are probed in order.
func DetectComposeFile(toplevel, override string) (string, error) {
	if override != "" {
		path := override
		if !filepath.IsAbs(path) {
			path = filepath.Join(toplevel, path)
		}
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("compose file %s not found", path)
		}
		return path, nil
	}
	candidates := []string{
		"docker-compose.dev.yml",
		"docker-compose.dev.yaml",
		"compose.dev.yml",
		"compose.dev.yaml",
		"docker-compose.yml",
		"docker-compose.yaml",
		"compose.yml",
		"compose.yaml",
	}
	for _, name := range candidates {
		p := filepath.Join(toplevel, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", errors.New(`no docker-compose file found in this directory.

pier is intentionally coupled to docker compose: every workload — even raw
processes (uv/npm/cargo) — runs inside a container so worktrees stay
isolated and reproducible across hosts.

If your project doesn't otherwise containerize, drop a minimal
docker-compose.dev.yml at its root, e.g.:

  services:
    app:
      image: python:3.13-slim          # or node:20, rust:1, ...
      working_dir: /app
      volumes:
        - ./:/app
      command: sh -c "pip install uv && uv sync && uv run python run.py"
      ports:
        - "${PORT:-3000}:3000"

Then re-run pier init.`)
}

// ListComposeServicesWithPorts returns every service that declares at
// least one published port, paired with its first container-side port.
// The list is sorted by service name so the wizard renders deterministically.
//
// We don't pull compose-go for this — a tiny stub of the relevant fields
// is enough and keeps the dep graph small.
func ListComposeServicesWithPorts(path string) []ComposeCandidate {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc struct {
		Services map[string]struct {
			Ports []any `yaml:"ports"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil
	}
	out := make([]ComposeCandidate, 0, len(doc.Services))
	for name, svc := range doc.Services {
		for _, p := range svc.Ports {
			if cp := parseContainerPort(p); cp > 0 {
				out = append(out, ComposeCandidate{Service: name, Port: cp})
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out
}

// parseContainerPort extracts the container-side port from a compose
// ports entry. Handles short syntax ("3000", "8080:3000",
// "${PORT:-8080}:3000") and the long form ({"target": 3000, "published": 8080}).
func parseContainerPort(entry any) int {
	switch v := entry.(type) {
	case string:
		// Container port is the right-most colon-separated segment, after
		// stripping any /protocol suffix.
		s := v
		if idx := strings.LastIndex(s, ":"); idx >= 0 {
			s = s[idx+1:]
		}
		if idx := strings.Index(s, "/"); idx >= 0 {
			s = s[:idx]
		}
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			return n
		}
	case int:
		return v
	case map[string]any:
		if t, ok := v["target"]; ok {
			switch tv := t.(type) {
			case int:
				return tv
			case string:
				if n, err := strconv.Atoi(tv); err == nil {
					return n
				}
			}
		}
	}
	return 0
}

// DetectDefaultBranch returns the conventional default branch (main or
// master) of the repo at toplevel, or "" when neither exists.
func DetectDefaultBranch(toplevel string) string {
	for _, candidate := range []string{"main", "master"} {
		c := exec.Command("git", "rev-parse", "--verify", "--quiet", candidate)
		c.Dir = toplevel
		if c.Run() == nil {
			return candidate
		}
	}
	return ""
}
