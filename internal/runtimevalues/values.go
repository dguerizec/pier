// Package runtimevalues resolves and persists per-worktree values used to
// render a .pier.toml template before its final parse.
package runtimevalues

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const relativePath = ".pier/resolved-values.json"

var (
	tokenRE = regexp.MustCompile(`\{value\.([A-Za-z_][A-Za-z0-9_]*)\}`)
	keyRE   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Values is the scalar object returned by [hooks].resolve_values.
// Scalars keep interpolation predictable in TOML and map directly to
// environment variables for Docker Compose.
type Values map[string]any

// Path returns the per-worktree cache path.
func Path(worktreePath string) string {
	return filepath.Join(worktreePath, filepath.FromSlash(relativePath))
}

// HasTokens reports whether body contains at least one {value.<name>} token.
func HasTokens(body []byte) bool {
	mode := quoteNone
	for i := 0; i < len(body); {
		if mode == quoteNone && body[i] == '#' {
			end := bytes.IndexByte(body[i:], '\n')
			if end < 0 {
				return false
			}
			i += end + 1
			continue
		}
		if nextMode, width, ok := quoteTransition(body, i, mode); ok {
			mode = nextMode
			i += width
			continue
		}
		if loc := tokenRE.FindIndex(body[i:]); loc != nil && loc[0] == 0 {
			return true
		}
		if mode == quoteBasic && body[i] == '\\' && i+1 < len(body) {
			i += 2
			continue
		}
		i++
	}
	return false
}

// Mask replaces value tokens with a harmless scalar so the static bootstrap
// fields can be decoded before the resolver has run. The masked document is
// never used to start a workload.
func Mask(body []byte) []byte {
	return tokenRE.ReplaceAll(body, []byte("1"))
}

// Resolve runs command through sh -c in the target worktree and parses its
// stdout as one JSON object. Stdout is reserved for the value protocol;
// diagnostics from the resolver stream through stderr.
func Resolve(command, worktreePath string, env []string, stderr io.Writer) (Values, error) {
	if strings.TrimSpace(command) == "" {
		return nil, errors.New("resolve_values command is empty")
	}
	if stderr == nil {
		stderr = io.Discard
	}

	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = worktreePath
	cmd.Env = env
	cmd.Stderr = stderr
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("resolve_values %q: %w", command, err)
	}

	values, err := decode(stdout.Bytes())
	if err != nil {
		return nil, fmt.Errorf("resolve_values output: %w", err)
	}
	return values, nil
}

// Load reads a previously persisted value object.
func Load(path string) (Values, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	values, err := decode(body)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return values, nil
}

// Save atomically persists values with owner-only permissions. Resolved values
// may include secrets even though the initial use case is a callback port.
func Save(path string, values Values) error {
	body, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return fmt.Errorf("encode resolved values: %w", err)
	}
	body = append(body, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".resolved-values-*.tmp")
	if err != nil {
		return fmt.Errorf("create resolved values temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod resolved values temp file: %w", err)
	}
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return fmt.Errorf("write resolved values temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close resolved values temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// Render substitutes value tokens in a TOML template. Outside a quoted string
// the replacement is a TOML scalar literal; inside a string it is escaped as
// string content. This makes both forms valid:
//
//	preserve_ports = [{value.oauth_port}]
//	callback = "http://127.0.0.1:{value.oauth_port}/callback"
func Render(body []byte, values Values) ([]byte, error) {
	var out bytes.Buffer
	mode := quoteNone

	for i := 0; i < len(body); {
		if mode == quoteNone && body[i] == '#' {
			end := bytes.IndexByte(body[i:], '\n')
			if end < 0 {
				out.Write(body[i:])
				break
			}
			end += i
			out.Write(body[i : end+1])
			i = end + 1
			continue
		}

		if nextMode, width, ok := quoteTransition(body, i, mode); ok {
			out.Write(body[i : i+width])
			mode = nextMode
			i += width
			continue
		}

		loc := tokenRE.FindSubmatchIndex(body[i:])
		if loc != nil && loc[0] == 0 {
			key := string(body[i+loc[2] : i+loc[3]])
			value, ok := values[key]
			if !ok {
				return nil, fmt.Errorf("value %q was not returned by resolve_values", key)
			}
			rendered, err := renderScalar(value, mode)
			if err != nil {
				return nil, fmt.Errorf("value %q: %w", key, err)
			}
			out.WriteString(rendered)
			i += loc[1]
			continue
		}

		if mode == quoteBasic && body[i] == '\\' && i+1 < len(body) {
			out.Write(body[i : i+2])
			i += 2
			continue
		}
		out.WriteByte(body[i])
		i++
	}
	return out.Bytes(), nil
}

// Environment converts values to the names exposed to Docker Compose.
// Keys are case-insensitively unique because both foo and FOO would map to
// PIER_VALUE_FOO.
func Environment(values Values) (map[string]string, error) {
	out := make(map[string]string, len(values))
	seen := make(map[string]string, len(values))
	for key, value := range values {
		name := "PIER_VALUE_" + strings.ToUpper(key)
		if previous, ok := seen[name]; ok {
			return nil, fmt.Errorf("value keys %q and %q map to the same environment variable %s", previous, key, name)
		}
		text, err := scalarText(value)
		if err != nil {
			return nil, fmt.Errorf("value %q: %w", key, err)
		}
		seen[name] = key
		out[name] = text
	}
	return out, nil
}

// MergeEnv layers vars over base without leaving duplicate environment keys.
func MergeEnv(base []string, vars map[string]string) []string {
	if len(vars) == 0 {
		return append([]string(nil), base...)
	}
	out := make([]string, 0, len(base)+len(vars))
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if ok {
			if _, replaced := vars[key]; replaced {
				continue
			}
		}
		out = append(out, entry)
	}
	for key, value := range vars {
		out = append(out, key+"="+value)
	}
	return out
}

func decode(body []byte) (Values, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var values Values
	if err := dec.Decode(&values); err != nil {
		return nil, err
	}
	if values == nil {
		return nil, errors.New("expected a JSON object")
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("expected exactly one JSON object")
		}
		return nil, err
	}

	caseFolded := map[string]string{}
	for key, value := range values {
		if !keyRE.MatchString(key) {
			return nil, fmt.Errorf("key %q must match %s", key, keyRE.String())
		}
		folded := strings.ToUpper(key)
		if previous, ok := caseFolded[folded]; ok {
			return nil, fmt.Errorf("keys %q and %q differ only by case", previous, key)
		}
		caseFolded[folded] = key
		switch value.(type) {
		case string, json.Number, bool:
		default:
			return nil, fmt.Errorf("value %q must be a string, number, or boolean", key)
		}
	}
	return values, nil
}

type quoteMode uint8

const (
	quoteNone quoteMode = iota
	quoteBasic
	quoteLiteral
	quoteMultilineBasic
	quoteMultilineLiteral
)

func quoteTransition(body []byte, i int, mode quoteMode) (quoteMode, int, bool) {
	remaining := body[i:]
	switch mode {
	case quoteNone:
		switch {
		case bytes.HasPrefix(remaining, []byte(`"""`)):
			return quoteMultilineBasic, 3, true
		case bytes.HasPrefix(remaining, []byte(`'''`)):
			return quoteMultilineLiteral, 3, true
		case body[i] == '"':
			return quoteBasic, 1, true
		case body[i] == '\'':
			return quoteLiteral, 1, true
		}
	case quoteBasic:
		if body[i] == '"' {
			return quoteNone, 1, true
		}
	case quoteLiteral:
		if body[i] == '\'' {
			return quoteNone, 1, true
		}
	case quoteMultilineBasic:
		if bytes.HasPrefix(remaining, []byte(`"""`)) {
			return quoteNone, 3, true
		}
	case quoteMultilineLiteral:
		if bytes.HasPrefix(remaining, []byte(`'''`)) {
			return quoteNone, 3, true
		}
	}
	return mode, 0, false
}

func renderScalar(value any, mode quoteMode) (string, error) {
	if mode == quoteNone {
		switch value := value.(type) {
		case string:
			body, _ := json.Marshal(value)
			return string(body), nil
		case json.Number:
			return value.String(), nil
		case bool:
			return strconv.FormatBool(value), nil
		}
	}

	text, err := scalarText(value)
	if err != nil {
		return "", err
	}
	switch mode {
	case quoteBasic, quoteMultilineBasic:
		body, _ := json.Marshal(text)
		return string(body[1 : len(body)-1]), nil
	case quoteLiteral:
		if strings.ContainsAny(text, "'\r\n") {
			return "", errors.New("cannot interpolate quotes or newlines into a literal TOML string")
		}
		return text, nil
	case quoteMultilineLiteral:
		if strings.Contains(text, "'''") {
			return "", errors.New("cannot interpolate triple quotes into a multiline literal TOML string")
		}
		return text, nil
	default:
		return "", fmt.Errorf("unsupported scalar type %T", value)
	}
}

func scalarText(value any) (string, error) {
	switch value := value.(type) {
	case string:
		return value, nil
	case json.Number:
		return value.String(), nil
	case bool:
		return strconv.FormatBool(value), nil
	default:
		return "", fmt.Errorf("must be a string, number, or boolean, got %T", value)
	}
}
