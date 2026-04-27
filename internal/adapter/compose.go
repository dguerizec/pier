package adapter

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
)

const (
	overrideSubdir = ".pier"
	overrideFile   = "compose.override.yml"
)

type compose struct{}

func (compose) Up(c Ctx) (*Handle, error) {
	if c.Stack.File == "" {
		return nil, errors.New("compose: stack.file is required")
	}
	if c.Stack.Service == "" {
		return nil, errors.New("compose: stack.service is required")
	}
	if c.Stack.Port == 0 {
		return nil, errors.New("compose: stack.port is required")
	}

	overridePath, err := writeOverride(c)
	if err != nil {
		return nil, err
	}

	if _, err := composeRun(c, []string{"up", "-d", "--build"}, overridePath, true); err != nil {
		return nil, fmt.Errorf("compose up: %w", err)
	}

	cid, err := composeContainerID(c, overridePath)
	if err != nil {
		// Up succeeded, but we couldn't fetch the container id — surface a
		// warning rather than failing, so the user still has a running
		// workload they can inspect.
		fmt.Fprintf(c.Err, "warning: could not resolve container id (%v)\n", err)
	}
	return &Handle{ContainerID: cid}, nil
}

func (compose) Down(c Ctx) error {
	overridePath := filepath.Join(c.WorktreePath, overrideSubdir, overrideFile)
	if _, err := os.Stat(overridePath); errors.Is(err, os.ErrNotExist) {
		// Regenerate the override so `down` works even on a fresh checkout
		// where the .pier directory has been removed.
		var werr error
		overridePath, werr = writeOverride(c)
		if werr != nil {
			return werr
		}
	}
	if _, err := composeRun(c, []string{"down"}, overridePath, true); err != nil {
		return fmt.Errorf("compose down: %w", err)
	}
	return nil
}

func (compose) Logs(c Ctx, follow bool, tail int) error {
	overridePath := filepath.Join(c.WorktreePath, overrideSubdir, overrideFile)
	if _, err := os.Stat(overridePath); errors.Is(err, os.ErrNotExist) {
		var werr error
		overridePath, werr = writeOverride(c)
		if werr != nil {
			return werr
		}
	}
	args := []string{"logs"}
	if follow {
		args = append(args, "-f")
	}
	if tail > 0 {
		args = append(args, "--tail", strconv.Itoa(tail))
	}
	args = append(args, c.Stack.Service)
	_, err := composeRun(c, args, overridePath, true)
	return err
}

// composeRun shells out to `docker compose -f <stack> -f <override> -p <name> <args...>`.
// When stream is true, stdout/stderr are forwarded to the caller's writers.
func composeRun(c Ctx, args []string, overridePath string, stream bool) (string, error) {
	stackPath := stackFilePath(c)
	full := []string{
		"compose",
		"-f", stackPath,
		"-f", overridePath,
		"-p", Name(c.Project, c.Slug),
	}
	full = append(full, args...)

	cmd := exec.Command("docker", full...)
	cmd.Dir = c.WorktreePath
	if stream {
		cmd.Stdout = c.Out
		cmd.Stderr = c.Err
		err := cmd.Run()
		return "", err
	}

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = c.Err
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

func composeContainerID(c Ctx, overridePath string) (string, error) {
	cmd := exec.Command("docker",
		"compose",
		"-f", stackFilePath(c),
		"-f", overridePath,
		"-p", Name(c.Project, c.Slug),
		"ps", "-q", c.Stack.Service,
	)
	cmd.Dir = c.WorktreePath
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", errors.New("compose ps returned no container id")
	}
	// Multiple replicas would yield multi-line output; pier treats the
	// first as canonical until we add scale-aware status.
	return strings.SplitN(id, "\n", 2)[0], nil
}

func stackFilePath(c Ctx) string {
	if filepath.IsAbs(c.Stack.File) {
		return c.Stack.File
	}
	return filepath.Join(c.WorktreePath, c.Stack.File)
}

// writeOverride renders the pier-managed compose override under
// <worktree>/.pier/compose.override.yml and returns its path.
func writeOverride(c Ctx) (string, error) {
	dir := filepath.Join(c.WorktreePath, overrideSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, overrideFile)

	body, err := renderOverride(c)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

func renderOverride(c Ctx) ([]byte, error) {
	t := template.Must(template.New("override").Parse(`# managed by pier — do not edit
services:
  {{.Service}}:
    container_name: {{.Name}}
    labels:
      - traefik.enable=true
      - traefik.http.routers.{{.Name}}.rule=Host(` + "`{{.Slug}}.{{.BaseDomain}}`" + `)
      - traefik.http.routers.{{.Name}}.entrypoints=web
      - traefik.docker.network=pier
      - traefik.http.services.{{.Name}}.loadbalancer.server.port={{.Port}}
    networks: [default, pier]

networks:
  pier:
    external: true
`))
	data := struct {
		Service, Name, Slug, BaseDomain string
		Port                            int
	}{
		Service:    c.Stack.Service,
		Name:       Name(c.Project, c.Slug),
		Slug:       c.Slug,
		BaseDomain: c.BaseDomain,
		Port:       c.Stack.Port,
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("render override: %w", err)
	}
	return buf.Bytes(), nil
}
