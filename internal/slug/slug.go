// Package slug derives DNS-safe slugs from git branch names per DESIGN §5.1.
package slug

import (
	"errors"
	"regexp"
	"strings"
)

var (
	ErrEmpty   = errors.New("slug: empty after derivation")
	ErrInvalid = errors.New("slug: result is not a valid DNS label")
)

// conventionalPrefixes follow the Conventional Branch convention. Note that
// "release/" is intentionally absent: release branches keep their prefix so
// the resulting slug remains identifiable (e.g. release/v1.2 → release-v1-2).
var conventionalPrefixes = []string{
	"feat/", "fix/", "chore/", "docs/", "perf/", "refactor/",
	"style/", "test/", "ci/", "build/", "revert/",
}

var (
	dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)
)

// FromBranch returns the slug derived from branch.
//
// The algorithm strips a leading conventional prefix (feat/, fix/, ...) and
// an optional "worktree-" prefix, lowercases, replaces non-alphanumeric
// runs with a single dash, trims edge dashes, and validates against the
// DNS label regex. main and master are not special-cased — they pass
// through as-is. Use --slug or PIER_SLUG to override.
func FromBranch(branch string) (string, error) {
	branch = strings.TrimSpace(branch)
	s := branch
	for _, p := range conventionalPrefixes {
		if strings.HasPrefix(s, p) {
			s = s[len(p):]
			break
		}
	}
	s = strings.TrimPrefix(s, "worktree-")
	s = strings.ToLower(s)
	s = nonAlnum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")

	if s == "" {
		return "", ErrEmpty
	}
	if !dnsLabel.MatchString(s) {
		return "", ErrInvalid
	}
	return s, nil
}

// Validate reports whether s is a usable pier slug (DNS label).
func Validate(s string) error {
	if s == "" {
		return ErrEmpty
	}
	if !dnsLabel.MatchString(s) {
		return ErrInvalid
	}
	return nil
}
