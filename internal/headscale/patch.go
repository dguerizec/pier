// Package headscale patches and reverts a headscale config.yaml to
// register/deregister a pier-managed TLD as a split-DNS domain, and
// maintains the extra_records JSON file when records mode is active.
// All edits are surgical: existing `dns:` keys, comments, and unrelated
// config (ACL, derp, log, ...) are preserved through yaml.Node.
package headscale

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"gopkg.in/yaml.v3"
)

// Patch reads cfgPath, appends the split-DNS rule for tld → ip, writes back,
// and saves a .bak alongside. The function is idempotent: re-running with
// the same args is a no-op.
func Patch(cfgPath, tld, ip string) (changed bool, err error) {
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", cfgPath, err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return false, fmt.Errorf("parse %s: %w", cfgPath, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return false, errors.New("headscale: unexpected yaml structure (no document node)")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return false, errors.New("headscale: top-level is not a mapping")
	}

	dns := getOrCreateMap(root, "dns")
	nameservers := getOrCreateMap(dns, "nameservers")
	split := getOrCreateMap(nameservers, "split")

	// dns.nameservers.split.<tld>: [<ip>]
	added := ensureScalarInList(split, tld, ip)
	// dns.search_domains: [..., <tld>]
	added2 := ensureScalarInTopLevelList(dns, "search_domains", tld)
	if !added && !added2 {
		return false, nil
	}

	if err := os.WriteFile(cfgPath+".bak", body, 0o644); err != nil {
		return false, fmt.Errorf("write backup: %w", err)
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return false, fmt.Errorf("re-marshal: %w", err)
	}
	if err := os.WriteFile(cfgPath, out, 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", cfgPath, err)
	}
	return true, nil
}

// Reload restarts the headscale container so it picks up the new DNS
// config. Headscale's SIGHUP handler only reloads ACL policy as of 0.28,
// not dns.* keys, so a full restart is required for split-DNS changes to
// reach peers.
func Reload(container string) error {
	return exec.Command("docker", "restart", container).Run()
}

// getOrCreateMap returns the mapping node under parent at key. Creates the
// entry if missing. Errors silently into the parent mutation.
func getOrCreateMap(parent *yaml.Node, key string) *yaml.Node {
	for i := 0; i < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			n := parent.Content[i+1]
			if n.Kind != yaml.MappingNode {
				// overwrite scalar with empty map; would only happen on
				// very malformed config
				n.Kind = yaml.MappingNode
				n.Tag = "!!map"
				n.Content = nil
			}
			return n
		}
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: key, Tag: "!!str"}
	valNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	parent.Content = append(parent.Content, keyNode, valNode)
	return valNode
}

// ensureScalarInList ensures parent[key] is a sequence containing val. Adds
// the entry idempotently. Returns true when a write actually happened.
func ensureScalarInList(parent *yaml.Node, key, val string) bool {
	for i := 0; i < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			seq := parent.Content[i+1]
			if seq.Kind != yaml.SequenceNode {
				seq.Kind = yaml.SequenceNode
				seq.Tag = "!!seq"
				seq.Content = nil
			}
			for _, item := range seq.Content {
				if item.Value == val {
					return false
				}
			}
			seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: val, Tag: "!!str"})
			return true
		}
	}
	parent.Content = append(parent.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key, Tag: "!!str"},
		&yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: val, Tag: "!!str"},
		}})
	return true
}

// ensureScalarInTopLevelList variant where the outer key is a list (not a map).
func ensureScalarInTopLevelList(parent *yaml.Node, key, val string) bool {
	return ensureScalarInList(parent, key, val)
}

// Unpatch is the inverse of Patch: it removes the split-DNS entry pier
// added at install time. Idempotent — running it on a config that no
// longer references tld returns changed=false. The .bak written by
// Patch is left in place as an audit trail.
//
// Surgical: only the (tld, ip) tuple under dns.nameservers.split and
// the bare tld under dns.search_domains are touched. Unrelated keys
// the user added (global nameservers, magic_dns, base_domain, other
// split entries) are preserved. Empty containers we created are
// pruned bottom-up: an emptied split.<tld> list drops the tld key,
// an emptied split map drops the split key. nameservers and dns are
// never removed even when they end up empty — the user may rely on
// the keys existing for future edits.
func Unpatch(cfgPath, tld, ip string) (changed bool, err error) {
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", cfgPath, err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return false, fmt.Errorf("parse %s: %w", cfgPath, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return false, errors.New("headscale: unexpected yaml structure (no document node)")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return false, errors.New("headscale: top-level is not a mapping")
	}

	dns := findMap(root, "dns")
	if dns == nil {
		return false, nil
	}

	splitChanged := false
	if nameservers := findMap(dns, "nameservers"); nameservers != nil {
		if split := findMap(nameservers, "split"); split != nil {
			if seq := findSequence(split, tld); seq != nil {
				if removeScalarFromList(seq, ip) {
					splitChanged = true
					if len(seq.Content) == 0 {
						removeKeyFromMap(split, tld)
						if len(split.Content) == 0 {
							removeKeyFromMap(nameservers, "split")
						}
					}
				}
			}
		}
	}

	searchChanged := false
	if seq := findSequence(dns, "search_domains"); seq != nil {
		if removeScalarFromList(seq, tld) {
			searchChanged = true
			if len(seq.Content) == 0 {
				removeKeyFromMap(dns, "search_domains")
			}
		}
	}

	if !splitChanged && !searchChanged {
		return false, nil
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return false, fmt.Errorf("re-marshal: %w", err)
	}
	if err := os.WriteFile(cfgPath, out, 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", cfgPath, err)
	}
	return true, nil
}

// findMap returns the mapping under parent at key, or nil if absent or
// not a mapping node.
func findMap(parent *yaml.Node, key string) *yaml.Node {
	for i := 0; i < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			n := parent.Content[i+1]
			if n.Kind == yaml.MappingNode {
				return n
			}
			return nil
		}
	}
	return nil
}

// findSequence returns the sequence under parent at key, or nil if
// absent or not a sequence node.
func findSequence(parent *yaml.Node, key string) *yaml.Node {
	for i := 0; i < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			n := parent.Content[i+1]
			if n.Kind == yaml.SequenceNode {
				return n
			}
			return nil
		}
	}
	return nil
}

// removeKeyFromMap drops the (key, value) pair from parent's Content.
// Returns true when a removal happened.
func removeKeyFromMap(parent *yaml.Node, key string) bool {
	for i := 0; i < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			parent.Content = append(parent.Content[:i], parent.Content[i+2:]...)
			return true
		}
	}
	return false
}

// removeScalarFromList drops the first item in seq matching val.
// Returns true when a removal happened.
func removeScalarFromList(seq *yaml.Node, val string) bool {
	for i, item := range seq.Content {
		if item.Value == val {
			seq.Content = append(seq.Content[:i], seq.Content[i+1:]...)
			return true
		}
	}
	return false
}
