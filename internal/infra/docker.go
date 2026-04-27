package infra

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// docker wraps `docker` CLI calls; we don't need the full SDK for bootstrap.
type docker struct{}

func newDocker() *docker { return &docker{} }

func (d *docker) run(args ...string) (string, error) {
	cmd := exec.Command("docker", args...)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("docker %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("docker %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ensureNetwork creates the named docker network if missing. Idempotent.
func (d *docker) ensureNetwork(name string) error {
	out, err := d.run("network", "ls", "--filter", "name=^"+name+"$", "--format", "{{.Name}}")
	if err != nil {
		return err
	}
	if out == name {
		return nil
	}
	_, err = d.run("network", "create", name)
	return err
}

// removeNetwork removes the named network. Returns nil if absent.
func (d *docker) removeNetwork(name string) error {
	out, _ := d.run("network", "ls", "--filter", "name=^"+name+"$", "--format", "{{.Name}}")
	if out != name {
		return nil
	}
	_, err := d.run("network", "rm", name)
	return err
}

// removeContainer force-removes the named container. Returns nil if absent.
func (d *docker) removeContainer(name string) error {
	out, _ := d.run("ps", "-a", "--filter", "name=^"+name+"$", "--format", "{{.Names}}")
	if out != name {
		return nil
	}
	_, err := d.run("rm", "-f", name)
	return err
}

// pull retrieves an image. Streams progress to stderr by inheriting the
// caller's stdout/stderr.
func (d *docker) pull(image string) error {
	cmd := exec.Command("docker", "pull", image)
	cmd.Stdout = nil
	cmd.Stderr = nil
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker pull %s: %s", image, strings.TrimSpace(string(out)))
	}
	return nil
}
