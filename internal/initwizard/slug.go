package initwizard

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	slugRE      = regexp.MustCompile(`[^a-z0-9]+`)
	dnsLabelRE  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
)

// Slugify lowercases s and collapses runs of non-alphanumerics into a
// single dash, trimming edge dashes. Result may still be empty or
// invalid — callers must validate.
func Slugify(s string) string {
	out := slugRE.ReplaceAllString(strings.ToLower(s), "-")
	return strings.Trim(out, "-")
}

// ValidateName checks that name is a valid DNS label (RFC 1035).
func ValidateName(name string) error {
	if !dnsLabelRE.MatchString(name) {
		return fmt.Errorf("project name %q is not a valid DNS label", name)
	}
	return nil
}
