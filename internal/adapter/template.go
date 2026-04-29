package adapter

import (
	"fmt"
	"regexp"
	"strings"
)

// tokenRE matches `{slug}`, `{base_domain}`, `{host.front}`, `{url.api}`,
// `{url.default}` style placeholders. The capture group is the token name.
var tokenRE = regexp.MustCompile(`\{([a-zA-Z][a-zA-Z0-9_.-]*)\}`)

// ExpandEnv evaluates templating tokens inside a single env value against
// the workload context. Supported tokens:
//
//	{slug}              the workload's slug
//	{base_domain}       project.base_domain
//	{pier.tld}          the TLD pier was installed with
//	{host.<service>}    `<host>.<slug>.<base>` for the named exposed service
//	{url.<service>}     `http://<host>.<slug>.<base>` for the named service
//	{url.default}       `http://<slug>.<base>` (the bare-slug alias)
//	{host.default}      `<slug>.<base>`
//
// Unknown tokens return an error so typos surface at `pier up` rather than
// silently producing broken values.
func ExpandEnv(value string, c Ctx) (string, error) {
	hosts := map[string]string{}
	for _, e := range c.Expose {
		hosts[e.Service] = HostFor(e, c.Slug, c.BaseDomain)
	}

	var firstErr error
	out := tokenRE.ReplaceAllStringFunc(value, func(match string) string {
		name := match[1 : len(match)-1]
		resolved, err := resolveToken(name, c, hosts)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		return resolved
	})
	if firstErr != nil {
		return "", firstErr
	}
	return out, nil
}

func resolveToken(name string, c Ctx, hosts map[string]string) (string, error) {
	switch name {
	case "slug":
		return c.Slug, nil
	case "base_domain":
		return c.BaseDomain, nil
	case "pier.tld":
		if c.TLD == "" {
			return "", fmt.Errorf("env template: {pier.tld} requires pier to be installed (Ctx.TLD is empty)")
		}
		return c.TLD, nil
	case "host.default":
		if c.DefaultService == "" {
			return "", fmt.Errorf("env template: {host.default} requires stack.service to designate an exposed service")
		}
		return AliasHost(c.Slug, c.BaseDomain), nil
	case "url.default":
		if c.DefaultService == "" {
			return "", fmt.Errorf("env template: {url.default} requires stack.service to designate an exposed service")
		}
		return "http://" + AliasHost(c.Slug, c.BaseDomain), nil
	}
	if rest, ok := strings.CutPrefix(name, "host."); ok {
		host, found := hosts[rest]
		if !found {
			return "", fmt.Errorf("env template: {host.%s} references unknown service (not in [[expose]])", rest)
		}
		return host, nil
	}
	if rest, ok := strings.CutPrefix(name, "url."); ok {
		host, found := hosts[rest]
		if !found {
			return "", fmt.Errorf("env template: {url.%s} references unknown service (not in [[expose]])", rest)
		}
		return "http://" + host, nil
	}
	return "", fmt.Errorf("env template: unknown token {%s}", name)
}

// ExpandPierTokens expands the subset of tokens that depend only on pier
// configuration (currently {pier.tld}). Use this for fields read at
// Ctx-build time that themselves feed into the Ctx — base_domain in
// particular, where workload tokens like {slug} aren't yet defined.
//
// Tokens unrelated to pier config error out so typos still surface, but
// workload-level tokens left in such a value would also error since
// they're not resolvable here. Keep manifest-level template values to
// {pier.tld} only.
func ExpandPierTokens(value, tld string) (string, error) {
	var firstErr error
	out := tokenRE.ReplaceAllStringFunc(value, func(match string) string {
		name := match[1 : len(match)-1]
		if name != "pier.tld" {
			if firstErr == nil {
				firstErr = fmt.Errorf("manifest template: token {%s} is not resolvable here (only {pier.tld} is allowed in manifest fields read at startup)", name)
			}
			return match
		}
		if tld == "" {
			if firstErr == nil {
				firstErr = fmt.Errorf("manifest template: {pier.tld} requires pier to be installed")
			}
			return match
		}
		return tld
	})
	if firstErr != nil {
		return "", firstErr
	}
	return out, nil
}

// ExpandEnvBlock expands every value of one service's env block. Returns
// the result in a flat map keyed by env var name.
func ExpandEnvBlock(env map[string]string, c Ctx) (map[string]string, error) {
	if len(env) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		expanded, err := ExpandEnv(v, c)
		if err != nil {
			return nil, fmt.Errorf("env[%s]: %w", k, err)
		}
		out[k] = expanded
	}
	return out, nil
}
