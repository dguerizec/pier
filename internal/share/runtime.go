package share

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ContainerState is the small slice of Docker state the share manager needs.
type ContainerState struct {
	Exists  bool
	Running bool
}

type gatewaySpec struct {
	Name       string
	Network    string
	BindIP     string
	Restart    bool
	StaticPath string
	DataPath   string
	ReadyPath  string
}

// Runtime isolates Docker I/O from state and routing logic.
type Runtime interface {
	State(name string) (ContainerState, error)
	Start(gatewaySpec) error
	SetRestart(name string, persistent bool) error
	Remove(name string) error
}

type dockerRuntime struct{}

func NewDockerRuntime() Runtime { return &dockerRuntime{} }

func (d *dockerRuntime) State(name string) (ContainerState, error) {
	out, err := dockerRun(
		"ps", "-a",
		"--filter", "name=^"+name+"$",
		"--format", "{{.Names}}",
	)
	if err != nil {
		return ContainerState{}, err
	}
	if strings.TrimSpace(out) != name {
		return ContainerState{}, nil
	}
	out, err = dockerRun("inspect", "--format", "{{.State.Running}}", name)
	if err != nil {
		return ContainerState{}, err
	}
	return ContainerState{Exists: true, Running: strings.TrimSpace(out) == "true"}, nil
}

func (d *dockerRuntime) Start(spec gatewaySpec) error {
	if err := os.Remove(spec.ReadyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	restart := "no"
	if spec.Restart {
		restart = "unless-stopped"
	}
	// The startup cleanup is what gives session shares their lifecycle:
	// restarting this gateway removes its session state/routes before
	// Traefik starts, while persistent.yml/json remain untouched.
	const startup = `rm -f /data/session.json /data/dynamic/session.yml /data/.ready
( sleep 0.25
  kill -0 "$$" 2>/dev/null && touch /data/.ready
) &
exec traefik --configFile=/etc/traefik/traefik.yml`
	_, err := dockerRun(
		"run", "-d",
		"--name", spec.Name,
		"--network", spec.Network,
		"--restart", restart,
		"--label", containerLabel,
		"--user", "0:0",
		"-p", spec.BindIP+":80:80/tcp",
		"-v", spec.StaticPath+":/etc/traefik/traefik.yml:ro",
		"-v", spec.DataPath+":/data",
		"--entrypoint", "/bin/sh",
		Image,
		"-c", startup,
	)
	if err != nil {
		return fmt.Errorf("start LAN gateway on %s:80: %w", spec.BindIP, err)
	}

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(spec.ReadyPath); err == nil {
			return nil
		}
		state, stateErr := d.State(spec.Name)
		if stateErr == nil && state.Exists && !state.Running {
			logs, _ := dockerRun("logs", spec.Name)
			return fmt.Errorf("LAN gateway exited during startup: %s", strings.TrimSpace(logs))
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("LAN gateway did not become ready within 4s")
}

func (d *dockerRuntime) SetRestart(name string, persistent bool) error {
	policy := "no"
	if persistent {
		policy = "unless-stopped"
	}
	_, err := dockerRun("update", "--restart="+policy, name)
	return err
}

func (d *dockerRuntime) Remove(name string) error {
	state, err := d.State(name)
	if err != nil {
		return err
	}
	if !state.Exists {
		return nil
	}
	_, err = dockerRun("rm", "-f", name)
	return err
}

func dockerRun(args ...string) (string, error) {
	cmd := exec.Command("docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return strings.TrimSpace(string(out)), nil
}
