// Package share publishes an explicit allowlist of Pier workload hosts on a
// LAN address. It deliberately runs a separate Traefik instance from Pier's
// normal local proxy so unshared workloads remain reachable on loopback only.
package share

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
)

const (
	Image          = "traefik:v3"
	containerLabel = "dev.pier.component=share"
)

// Address is one concrete IPv4 address assigned to a host interface. Pier
// binds the address snapshot, rather than following the interface, so a
// laptop changing networks cannot silently republish its shares.
type Address struct {
	Interface string `json:"interface"`
	IP        string `json:"ip"`
}

// Candidate is one exact URL the current workload can share.
type Candidate struct {
	Host         string
	Project      string
	Slug         string
	WorktreePath string
	Service      string
	HostLabel    string
	Container    string
	Port         int
	Default      bool
}

// Record is the durable representation of a Candidate. Persistence is encoded
// by which state file contains the record, not by this value.
type Record struct {
	Host         string `json:"host"`
	Project      string `json:"project"`
	Slug         string `json:"slug"`
	WorktreePath string `json:"worktree_path"`
	Service      string `json:"service"`
	HostLabel    string `json:"host_label,omitempty"`
	Container    string `json:"container"`
	Port         int    `json:"port"`
	Default      bool   `json:"default,omitempty"`
}

func (c Candidate) Record() Record {
	return Record{
		Host:         c.Host,
		Project:      c.Project,
		Slug:         c.Slug,
		WorktreePath: c.WorktreePath,
		Service:      c.Service,
		HostLabel:    c.HostLabel,
		Container:    c.Container,
		Port:         c.Port,
		Default:      c.Default,
	}
}

// SharedRecord combines a route with its gateway and live status.
type SharedRecord struct {
	Record
	Address
	Persistent    bool
	GatewayUp     bool
	WorkloadUp    bool
	AddressUp     bool
	ContainerName string
}

type gatewayMeta struct {
	Address
	Network   string `json:"network"`
	Container string `json:"container"`
}

// Paths is the share-owned subtree under Pier's config directory.
type Paths struct {
	Root     string
	Static   string
	Gateways string
	Lock     string
}

func NewPaths(configRoot string) Paths {
	root := filepath.Join(configRoot, "share")
	return Paths{
		Root:     root,
		Static:   filepath.Join(root, "traefik.yml"),
		Gateways: filepath.Join(root, "gateways"),
		Lock:     filepath.Join(root, "shares.lock"),
	}
}

func (p Paths) forGateway(id string) gatewayPaths {
	root := filepath.Join(p.Gateways, id)
	return gatewayPaths{
		Root:          root,
		Meta:          filepath.Join(root, "gateway.json"),
		Persistent:    filepath.Join(root, "persistent.json"),
		Session:       filepath.Join(root, "session.json"),
		Dynamic:       filepath.Join(root, "dynamic"),
		PersistentYML: filepath.Join(root, "dynamic", "persistent.yml"),
		SessionYML:    filepath.Join(root, "dynamic", "session.yml"),
		Ready:         filepath.Join(root, ".ready"),
	}
}

type gatewayPaths struct {
	Root          string
	Meta          string
	Persistent    string
	Session       string
	Dynamic       string
	PersistentYML string
	SessionYML    string
	Ready         string
}

func gatewayID(ip string) string {
	sum := sha256.Sum256([]byte(ip))
	return hex.EncodeToString(sum[:6])
}

func gatewayContainer(ip string) string {
	return "pier-share-" + gatewayID(ip)
}
