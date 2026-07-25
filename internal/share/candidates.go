package share

import (
	"fmt"
	"path"
	"strings"

	"github.com/dguerizec/pier/internal/adapter"
)

// Candidates returns the exact host allowlist a workload can expose. The bare
// default alias comes first, matching adapter.URLs and `pier url`.
func Candidates(c adapter.Ctx) []Candidate {
	var out []Candidate
	defaultHost := strings.TrimPrefix(adapter.DefaultURL(c), "http://")

	if c.DefaultService != "" {
		for _, expose := range c.Expose {
			if expose.Service != c.DefaultService {
				continue
			}
			host := adapter.AliasHost(c.Slug, c.BaseDomain)
			out = append(out, Candidate{
				Host:         host,
				Project:      c.Project,
				Slug:         c.Slug,
				WorktreePath: c.WorktreePath,
				Service:      expose.Service,
				Container:    adapter.ServiceName(c.Project, c.Slug, expose.Service),
				Port:         expose.Port,
				Default:      host == defaultHost,
			})
			break
		}
	}

	for _, expose := range c.Expose {
		host := adapter.HostFor(expose, c.Slug, c.BaseDomain)
		out = append(out, Candidate{
			Host:         host,
			Project:      c.Project,
			Slug:         c.Slug,
			WorktreePath: c.WorktreePath,
			Service:      expose.Service,
			HostLabel:    expose.Hostname(),
			Container:    adapter.ServiceName(c.Project, c.Slug, expose.Service),
			Port:         expose.Port,
			Default:      host == defaultHost,
		})
	}
	return out
}

// SelectCandidates expands user selectors against the finite set of current
// workload URLs. A glob never reaches Traefik: the returned candidates always
// contain exact hostnames.
func SelectCandidates(candidates []Candidate, selectors []string, tld string) ([]Candidate, error) {
	if len(selectors) == 0 {
		return nil, nil
	}
	selected := make([]bool, len(candidates))
	for _, selector := range selectors {
		matched := false
		for i, candidate := range candidates {
			ok, err := candidateMatches(candidate, selector, tld)
			if err != nil {
				return nil, fmt.Errorf("invalid selector %q: %w", selector, err)
			}
			if ok {
				selected[i] = true
				matched = true
			}
		}
		if !matched {
			return nil, noCandidateMatch(selector, candidates)
		}
	}
	out := make([]Candidate, 0, len(candidates))
	for i, candidate := range candidates {
		if selected[i] {
			out = append(out, candidate)
		}
	}
	return out, nil
}

func candidateMatches(candidate Candidate, selector, tld string) (bool, error) {
	representations := []string{candidate.Host}
	if suffix := "." + strings.TrimPrefix(tld, "."); tld != "" && strings.HasSuffix(candidate.Host, suffix) {
		representations = append(representations, strings.TrimSuffix(candidate.Host, suffix))
	}
	if candidate.HostLabel != "" {
		representations = append(representations, candidate.HostLabel)
	}
	if candidate.Service != "" && candidate.HostLabel != "" {
		representations = append(representations, candidate.Service)
	}
	for _, value := range representations {
		ok, err := path.Match(selector, value)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

func noCandidateMatch(selector string, candidates []Candidate) error {
	hosts := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		hosts = append(hosts, candidate.Host)
	}
	return fmt.Errorf("selector %q matches no Pier host (available: %s)", selector, strings.Join(hosts, ", "))
}

// SelectRecords is the stored-route counterpart used by remove/hosts/url.
func SelectRecords(records []SharedRecord, selectors []string, tld string) ([]SharedRecord, error) {
	if len(selectors) == 0 {
		return records, nil
	}
	candidates := make([]Candidate, 0, len(records))
	for _, record := range records {
		candidates = append(candidates, Candidate{
			Host:      record.Host,
			Service:   record.Service,
			HostLabel: record.HostLabel,
		})
	}
	matches, err := SelectCandidates(candidates, selectors, tld)
	if err != nil {
		return nil, err
	}
	wanted := make(map[string]bool, len(matches))
	for _, match := range matches {
		wanted[match.Host] = true
	}
	out := make([]SharedRecord, 0, len(matches))
	for _, record := range records {
		if wanted[record.Host] {
			out = append(out, record)
		}
	}
	return out, nil
}
