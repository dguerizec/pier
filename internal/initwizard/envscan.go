package initwizard

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/LeoPartt/pier/internal/manifest"
)

// EnvSuggestion is a candidate replacement for a hard-coded URL in a
// compose service's environment block. The wizard offers them to the
// user; only those they accept land in the manifest's [env.<service>]
// table.
type EnvSuggestion struct {
	Service     string // service whose environment carries the variable
	Key         string // env var name
	Value       string // current literal value in the compose file
	Target      string // service the URL points at
	Replacement string // proposed value, e.g. "{url.api}" or "{url.api}/v1"
}

// EnvVarPrompt represents an env var whose compose value is a pure
// host-side interpolation (e.g. `${VITE_ALLOWED_HOSTS-}`). The wizard
// asks the user whether they want to pin a value in [env.<service>]
// instead of leaving it to the surrounding shell or .env file.
type EnvVarPrompt struct {
	Service string
	Key     string
	Raw     string // raw compose value, e.g. "${VITE_ALLOWED_HOSTS-}"
	HostVar string // host env var the interpolation references
	Default string // the -default / :-default portion, "" when none
}

// composeEnv is the slice of compose docs we care about for env scanning.
// We re-parse the file rather than threading state through detect.go
// because the env-scan path is optional and only runs when the wizard
// will actually offer suggestions.
type composeDoc struct {
	Services map[string]struct {
		Ports       []any `yaml:"ports"`
		Environment any   `yaml:"environment"`
	} `yaml:"services"`
}

// urlRE matches the minimal set of URLs we know how to convert to a
// pier `{url.<svc>}` template. Group 1 is the host, group 2 the port
// (with the leading colon kept), group 3 the path.
var urlRE = regexp.MustCompile(`^https?://([A-Za-z0-9_.-]+)(?::(\d+))?(/[^\s]*)?$`)

// pureInterpRE matches an env value that is a single ${...}
// interpolation with nothing around it. We only handle this strict form
// — partial interpolations like "prefix-${VAR}" are too ambiguous to
// suggest a sensible replacement for.
var pureInterpRE = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)([:-][^}]*)?\}$`)

// ScanEnvSuggestions reads composePath, walks every service.environment
// entry, and proposes substitutions for values that point at another
// service in the same file. Two link forms are recognised:
//
//   - Direct service hostname: `http://api:8000` → {url.api}
//   - Loopback + published host port: `http://localhost:60181` (when
//     60181 is the host side of `60181:8000` on service api) → {url.api}
//
// Suggestions for env keys already present in `existing` are filtered
// out so re-init never proposes to overwrite a manual override.
func ScanEnvSuggestions(composePath string, existing *manifest.Manifest) []EnvSuggestion {
	body, err := os.ReadFile(composePath)
	if err != nil {
		return nil
	}
	var doc composeDoc
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil
	}

	services := map[string]bool{}
	for name := range doc.Services {
		services[name] = true
	}
	hostPortToService := map[int]string{}
	for name, svc := range doc.Services {
		for _, p := range svc.Ports {
			if hp := parseHostPort(p); hp > 0 {
				hostPortToService[hp] = name
			}
		}
	}

	var out []EnvSuggestion
	serviceNames := make([]string, 0, len(doc.Services))
	for name := range doc.Services {
		serviceNames = append(serviceNames, name)
	}
	sort.Strings(serviceNames)

	for _, name := range serviceNames {
		entries := flattenEnv(doc.Services[name].Environment)
		// Stable order so the wizard renders deterministically.
		keys := make([]string, 0, len(entries))
		for k := range entries {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			val := entries[key]
			if alreadyOverridden(existing, name, key) {
				continue
			}
			target, replacement, ok := suggestForValue(val, name, services, hostPortToService)
			if !ok {
				continue
			}
			out = append(out, EnvSuggestion{
				Service:     name,
				Key:         key,
				Value:       val,
				Target:      target,
				Replacement: replacement,
			})
		}
	}
	return out
}

// ScanEnvVarPrompts walks service.environment and returns one entry for
// every value that is a pure ${VAR} or ${VAR-default} interpolation.
// These values are not actually set inside the container until something
// in the host shell or .env exports them; pier's [env.<service>] table
// is the natural place to pin them per-worktree.
//
// Keys already present in the existing manifest's [env.<svc>] are
// filtered out so re-init doesn't re-prompt for what the user has
// already settled.
func ScanEnvVarPrompts(composePath string, existing *manifest.Manifest) []EnvVarPrompt {
	body, err := os.ReadFile(composePath)
	if err != nil {
		return nil
	}
	var doc composeDoc
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil
	}

	serviceNames := make([]string, 0, len(doc.Services))
	for name := range doc.Services {
		serviceNames = append(serviceNames, name)
	}
	sort.Strings(serviceNames)

	var out []EnvVarPrompt
	for _, name := range serviceNames {
		entries := flattenEnv(doc.Services[name].Environment)
		keys := make([]string, 0, len(entries))
		for k := range entries {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if alreadyOverridden(existing, name, key) {
				continue
			}
			raw := strings.TrimSpace(entries[key])
			m := pureInterpRE.FindStringSubmatch(raw)
			if m == nil {
				continue
			}
			hostVar := m[1]
			def := ""
			if m[2] != "" {
				// m[2] is ":-default", "-default", ":?error", or ":+alt".
				// We only treat ":-" and "-" as actual defaults the user
				// may want to inherit; the others are diagnostic forms
				// that don't carry a sensible pin value.
				switch {
				case strings.HasPrefix(m[2], ":-"):
					def = m[2][2:]
				case strings.HasPrefix(m[2], "-"):
					def = m[2][1:]
				}
			}
			out = append(out, EnvVarPrompt{
				Service: name,
				Key:     key,
				Raw:     raw,
				HostVar: hostVar,
				Default: def,
			})
		}
	}
	return out
}

// suggestForValue returns the target service and replacement template,
// or ok=false when the value can't be templated. selfService is the
// service whose environment holds the value: a self-reference (front
// pointing at front) is dropped because pier templates cross-service
// URLs, not the service's own.
func suggestForValue(val, selfService string, services map[string]bool, hostPortToService map[int]string) (target, replacement string, ok bool) {
	v := strings.TrimSpace(val)
	m := urlRE.FindStringSubmatch(v)
	if m == nil {
		return "", "", false
	}
	host, portStr, path := m[1], m[2], m[3]

	switch {
	case services[host]:
		target = host
	case isLoopback(host):
		if portStr == "" {
			return "", "", false
		}
		port, err := strconv.Atoi(portStr)
		if err != nil {
			return "", "", false
		}
		svc, ok := hostPortToService[port]
		if !ok {
			return "", "", false
		}
		target = svc
	default:
		return "", "", false
	}

	if target == selfService {
		return "", "", false
	}

	replacement = fmt.Sprintf("{url.%s}", target)
	if path != "" && path != "/" {
		replacement += path
	}
	return target, replacement, true
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "0.0.0.0"
}

// flattenEnv normalises both compose env forms into a key→value map.
// The list form ("KEY=value") loses ordering; we don't care here.
func flattenEnv(raw any) map[string]string {
	out := map[string]string{}
	switch v := raw.(type) {
	case map[string]any:
		for k, vv := range v {
			out[k] = stringify(vv)
		}
	case []any:
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				continue
			}
			eq := strings.IndexByte(s, '=')
			if eq < 0 {
				// "KEY" without value — compose passes it from the host
				// env. Nothing for the wizard to suggest against.
				continue
			}
			out[s[:eq]] = s[eq+1:]
		}
	}
	return out
}

func stringify(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int:
		return strconv.Itoa(x)
	case bool:
		return strconv.FormatBool(x)
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

func alreadyOverridden(m *manifest.Manifest, service, key string) bool {
	if m == nil {
		return false
	}
	svc, ok := m.Env[service]
	if !ok {
		return false
	}
	_, has := svc[key]
	return has
}

// parseHostPort extracts the host-side port from a compose ports entry.
// Mirror of parseContainerPort but returns the published port (left-most
// segment of the host:container mapping). Returns 0 when there is no
// host mapping (bare "3000" or long form without `published`).
func parseHostPort(entry any) int {
	switch v := entry.(type) {
	case string:
		s := strings.TrimSpace(v)
		if idx := strings.Index(s, "/"); idx >= 0 {
			s = s[:idx]
		}
		// Reduce ${VAR:-N} runs to their default before splitting on `:`,
		// so the inner colon of `${PORT:-8080}` doesn't confuse the parse.
		s = expandEnvDefaults(s)
		parts := strings.Split(s, ":")
		switch len(parts) {
		case 2:
			// host:container
			if n, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil {
				return n
			}
		case 3:
			// ip:host:container
			if n, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil {
				return n
			}
		}
	case int:
		return 0 // bare int = container port only
	case map[string]any:
		if p, ok := v["published"]; ok {
			switch pv := p.(type) {
			case int:
				return pv
			case string:
				if n, err := strconv.Atoi(pv); err == nil {
					return n
				}
			}
		}
	}
	return 0
}

var envDefaultRE = regexp.MustCompile(`\$\{[^}]*\}`)

// expandEnvDefaults replaces every ${VAR:-default} with `default` and
// every ${VAR} with the empty string. It's intentionally minimal — we
// only need the result to feed strconv.Atoi.
func expandEnvDefaults(s string) string {
	return envDefaultRE.ReplaceAllStringFunc(s, func(match string) string {
		body := match[2 : len(match)-1]
		if idx := strings.Index(body, ":-"); idx >= 0 {
			return body[idx+2:]
		}
		return ""
	})
}
