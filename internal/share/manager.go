package share

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"

	"gopkg.in/yaml.v3"
)

// Manager owns share state and the selective Traefik gateways.
type Manager struct {
	Paths   Paths
	Network string
	Runtime Runtime
}

var managerLocks sync.Map // config-root path → *sync.Mutex

func NewManager(configRoot, network string) *Manager {
	return &Manager{
		Paths:   NewPaths(configRoot),
		Network: network,
		Runtime: NewDockerRuntime(),
	}
}

// Add publishes exact records on address. Re-adding a session route with
// --persist upgrades it; a persistent route is never silently downgraded.
func (m *Manager) Add(candidates []Candidate, address Address, persist bool) ([]SharedRecord, error) {
	if len(candidates) == 0 {
		return nil, errors.New("share: select at least one host")
	}
	if net.ParseIP(address.IP).To4() == nil || address.Interface == "" {
		return nil, fmt.Errorf("share: invalid LAN address %+v", address)
	}
	var added []SharedRecord
	err := m.withLock(func() error {
		checked := map[string]bool{}
		for _, candidate := range candidates {
			if checked[candidate.Container] {
				continue
			}
			state, err := m.Runtime.State(candidate.Container)
			if err != nil {
				return err
			}
			if !state.Running {
				return fmt.Errorf("share: workload container %s is not running (run `pier up` first)", candidate.Container)
			}
			checked[candidate.Container] = true
		}

		gateways, err := m.loadGateways(true)
		if err != nil {
			return err
		}
		id := gatewayID(address.IP)
		active := gateways[:0]
		for _, gateway := range gateways {
			if gateway.ID != id && !gateway.Container.Running && len(gateway.Persistent) == 0 {
				if err := m.Runtime.Remove(gateway.Meta.Container); err != nil {
					return err
				}
				if err := os.RemoveAll(gateway.Paths.Root); err != nil {
					return err
				}
				continue
			}
			active = append(active, gateway)
		}
		gateways = active
		for _, gateway := range gateways {
			if gateway.ID == id {
				continue
			}
			existingRecords := append([]Record(nil), gateway.Persistent...)
			if gateway.Container.Running {
				existingRecords = append(existingRecords, gateway.Session...)
			}
			for _, existing := range existingRecords {
				for _, candidate := range candidates {
					if existing.Host == candidate.Host {
						return fmt.Errorf("share: %s is already shared on %s; remove it before changing LAN address", candidate.Host, gateway.Meta.IP)
					}
				}
			}
		}

		gateway, ok := gatewayByID(gateways, id)
		if !ok {
			gateway = gatewayState{
				ID: id,
				Meta: gatewayMeta{
					Address:   address,
					Network:   m.Network,
					Container: gatewayContainer(address.IP),
				},
				Paths: m.Paths.forGateway(id),
			}
		} else {
			gateway.Meta.Address = address
			if gateway.Meta.Network != m.Network {
				if gateway.Container.Exists {
					if err := m.Runtime.Remove(gateway.Meta.Container); err != nil {
						return err
					}
				}
				gateway.Container = ContainerState{}
				gateway.Meta.Network = m.Network
			}
		}

		wantsPersistent := persist || len(gateway.Persistent) > 0
		fresh, err := m.ensureGateway(&gateway, wantsPersistent)
		if err != nil {
			return err
		}
		if fresh {
			// The gateway startup hook cleared session.json. Mirror that in
			// memory so a daemon restart cannot resurrect ephemeral routes.
			gateway.Session = nil
		} else {
			gateway.Session, err = readRecords(gateway.Paths.Session)
			if err != nil {
				return err
			}
		}
		gateway.Persistent, err = readRecords(gateway.Paths.Persistent)
		if err != nil {
			return err
		}

		for _, candidate := range candidates {
			record := candidate.Record()
			wasPersistent := removeHost(&gateway.Persistent, record.Host)
			removeHost(&gateway.Session, record.Host)
			recordPersistent := persist || wasPersistent
			if recordPersistent {
				gateway.Persistent = append(gateway.Persistent, record)
			} else {
				gateway.Session = append(gateway.Session, record)
			}
			added = append(added, SharedRecord{
				Record:        record,
				Address:       address,
				Persistent:    recordPersistent,
				GatewayUp:     true,
				WorkloadUp:    true,
				AddressUp:     true,
				ContainerName: gateway.Meta.Container,
			})
		}
		if err := m.writeGateway(&gateway); err != nil {
			return err
		}
		return m.Runtime.SetRestart(gateway.Meta.Container, len(gateway.Persistent) > 0)
	})
	return added, err
}

// Remove deletes exact hosts for a project/worktree from every gateway.
func (m *Manager) Remove(project, slug string, hosts []string) (int, error) {
	if len(hosts) == 0 {
		return 0, nil
	}
	wanted := make(map[string]bool, len(hosts))
	for _, host := range hosts {
		wanted[host] = true
	}
	removed := 0
	err := m.withLock(func() error {
		gateways, err := m.loadGateways(true)
		if err != nil {
			return err
		}
		for i := range gateways {
			gateway := &gateways[i]
			removed += removeMatching(&gateway.Persistent, project, slug, wanted)
			removed += removeMatching(&gateway.Session, project, slug, wanted)
			if err := m.reconcileGateway(gateway); err != nil {
				return err
			}
		}
		return nil
	})
	return removed, err
}

// RemoveWorktree removes every route whose recorded worktree path matches.
// Worktree deletion can call this after git has removed the directory because
// share state carries the canonical path independently of the manifest.
func (m *Manager) RemoveWorktree(worktreePath string) (int, error) {
	removed := 0
	err := m.withLock(func() error {
		gateways, err := m.loadGateways(true)
		if err != nil {
			return err
		}
		for i := range gateways {
			gateway := &gateways[i]
			removed += removeWorktreeRecords(&gateway.Persistent, worktreePath)
			removed += removeWorktreeRecords(&gateway.Session, worktreePath)
			if err := m.reconcileGateway(gateway); err != nil {
				return err
			}
		}
		return nil
	})
	return removed, err
}

// List returns active session routes and all persistent routes. Session state
// from a stopped gateway is intentionally omitted: its lifecycle ended when
// the gateway stopped.
func (m *Manager) List() ([]SharedRecord, error) {
	var out []SharedRecord
	err := m.withLock(func() error {
		gateways, err := m.loadGateways(false)
		if err != nil {
			return err
		}
		targetState := map[string]bool{}
		for _, gateway := range gateways {
			appendRecords := func(records []Record, persistent bool) {
				for _, record := range records {
					up, ok := targetState[record.Container]
					if !ok {
						state, stateErr := m.Runtime.State(record.Container)
						up = stateErr == nil && state.Running
						targetState[record.Container] = up
					}
					out = append(out, SharedRecord{
						Record:        record,
						Address:       gateway.Meta.Address,
						Persistent:    persistent,
						GatewayUp:     gateway.Container.Running,
						WorkloadUp:    up,
						AddressUp:     true,
						ContainerName: gateway.Meta.Container,
					})
				}
			}
			appendRecords(gateway.Persistent, true)
			if gateway.Container.Running {
				appendRecords(gateway.Session, false)
			}
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].Project != out[j].Project {
			return out[i].Project < out[j].Project
		}
		if out[i].Slug != out[j].Slug {
			return out[i].Slug < out[j].Slug
		}
		if out[i].Default != out[j].Default {
			return out[i].Default
		}
		if out[i].Host != out[j].Host {
			return out[i].Host < out[j].Host
		}
		return out[i].IP < out[j].IP
	})
	return out, err
}

// Stored returns session records even for a stopped gateway. It is used by
// remove so stale state remains cleanable.
func (m *Manager) Stored() ([]SharedRecord, error) {
	var out []SharedRecord
	err := m.withLock(func() error {
		gateways, err := m.loadGateways(true)
		if err != nil {
			return err
		}
		for _, gateway := range gateways {
			for _, pair := range []struct {
				records    []Record
				persistent bool
			}{
				{gateway.Persistent, true},
				{gateway.Session, false},
			} {
				for _, record := range pair.records {
					out = append(out, SharedRecord{
						Record:        record,
						Address:       gateway.Meta.Address,
						Persistent:    pair.persistent,
						GatewayUp:     gateway.Container.Running,
						AddressUp:     true,
						ContainerName: gateway.Meta.Container,
					})
				}
			}
		}
		return nil
	})
	return out, err
}

type gatewayState struct {
	ID         string
	Meta       gatewayMeta
	Paths      gatewayPaths
	Persistent []Record
	Session    []Record
	Container  ContainerState
}

func (m *Manager) ensureGateway(gateway *gatewayState, persistent bool) (bool, error) {
	if err := m.ensureFiles(gateway); err != nil {
		return false, err
	}
	state, err := m.Runtime.State(gateway.Meta.Container)
	if err != nil {
		return false, err
	}
	gateway.Container = state
	if state.Running {
		return false, nil
	}
	if state.Exists {
		if err := m.Runtime.Remove(gateway.Meta.Container); err != nil {
			return false, err
		}
	}
	_ = os.Remove(gateway.Paths.Session)
	_ = os.Remove(gateway.Paths.SessionYML)
	if err := m.Runtime.Start(gatewaySpec{
		Name:       gateway.Meta.Container,
		Network:    gateway.Meta.Network,
		BindIP:     gateway.Meta.IP,
		Restart:    persistent,
		StaticPath: m.Paths.Static,
		DataPath:   gateway.Paths.Root,
		ReadyPath:  gateway.Paths.Ready,
	}); err != nil {
		return false, err
	}
	gateway.Container = ContainerState{Exists: true, Running: true}
	return true, nil
}

func (m *Manager) ensureFiles(gateway *gatewayState) error {
	if err := os.MkdirAll(gateway.Paths.Dynamic, 0o700); err != nil {
		return err
	}
	if err := writeFileIfChanged(m.Paths.Static, []byte(staticConfig), 0o600); err != nil {
		return err
	}
	if err := writeJSON(gateway.Paths.Meta, gateway.Meta); err != nil {
		return err
	}
	if _, err := os.Stat(gateway.Paths.PersistentYML); errors.Is(err, os.ErrNotExist) {
		if err := writeRoutes(gateway.Paths.PersistentYML, gateway.Persistent); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) writeGateway(gateway *gatewayState) error {
	sortRecords(gateway.Persistent)
	sortRecords(gateway.Session)
	if err := writeRecords(gateway.Paths.Persistent, gateway.Persistent); err != nil {
		return err
	}
	if err := writeRecords(gateway.Paths.Session, gateway.Session); err != nil {
		return err
	}
	if err := writeRoutes(gateway.Paths.PersistentYML, gateway.Persistent); err != nil {
		return err
	}
	return writeRoutes(gateway.Paths.SessionYML, gateway.Session)
}

func (m *Manager) reconcileGateway(gateway *gatewayState) error {
	if len(gateway.Persistent) == 0 && len(gateway.Session) == 0 {
		if err := m.Runtime.Remove(gateway.Meta.Container); err != nil {
			return err
		}
		if err := os.RemoveAll(gateway.Paths.Root); err != nil {
			return err
		}
		return nil
	}
	if err := m.writeGateway(gateway); err != nil {
		return err
	}
	if gateway.Container.Exists {
		return m.Runtime.SetRestart(gateway.Meta.Container, len(gateway.Persistent) > 0)
	}
	return nil
}

func (m *Manager) loadGateways(includeStoppedSession bool) ([]gatewayState, error) {
	entries, err := os.ReadDir(m.Paths.Gateways)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []gatewayState
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		paths := m.Paths.forGateway(entry.Name())
		var meta gatewayMeta
		if err := readJSON(paths.Meta, &meta); err != nil {
			return nil, err
		}
		state, err := m.Runtime.State(meta.Container)
		if err != nil {
			return nil, err
		}
		persistent, err := readRecords(paths.Persistent)
		if err != nil {
			return nil, err
		}
		var session []Record
		if state.Running || includeStoppedSession {
			session, err = readRecords(paths.Session)
			if err != nil {
				return nil, err
			}
		}
		out = append(out, gatewayState{
			ID:         entry.Name(),
			Meta:       meta,
			Paths:      paths,
			Persistent: persistent,
			Session:    session,
			Container:  state,
		})
	}
	return out, nil
}

func (m *Manager) withLock(fn func() error) error {
	value, _ := managerLocks.LoadOrStore(m.Paths.Lock, &sync.Mutex{})
	processLock := value.(*sync.Mutex)
	processLock.Lock()
	defer processLock.Unlock()

	if err := os.MkdirAll(m.Paths.Root, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(m.Paths.Lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return fn()
}

func gatewayByID(gateways []gatewayState, id string) (gatewayState, bool) {
	for _, gateway := range gateways {
		if gateway.ID == id {
			return gateway, true
		}
	}
	return gatewayState{}, false
}

func removeHost(records *[]Record, host string) bool {
	out := (*records)[:0]
	found := false
	for _, record := range *records {
		if record.Host == host {
			found = true
			continue
		}
		out = append(out, record)
	}
	*records = out
	return found
}

func removeMatching(records *[]Record, project, slug string, hosts map[string]bool) int {
	out := (*records)[:0]
	removed := 0
	for _, record := range *records {
		if record.Project == project && record.Slug == slug && hosts[record.Host] {
			removed++
			continue
		}
		out = append(out, record)
	}
	*records = out
	return removed
}

func removeWorktreeRecords(records *[]Record, worktreePath string) int {
	out := (*records)[:0]
	removed := 0
	for _, record := range *records {
		if record.WorktreePath == worktreePath {
			removed++
			continue
		}
		out = append(out, record)
	}
	*records = out
	return removed
}

func sortRecords(records []Record) {
	sort.Slice(records, func(i, j int) bool { return records[i].Host < records[j].Host })
}

const staticConfig = `# managed by pier
entryPoints:
  web:
    address: ":80"

providers:
  file:
    directory: "/data/dynamic"
    watch: true

api:
  dashboard: false

log:
  level: INFO
accessLog: {}
`

type dynamicConfig struct {
	HTTP dynamicHTTP `yaml:"http"`
}

type dynamicHTTP struct {
	Routers  map[string]dynamicRouter  `yaml:"routers,omitempty"`
	Services map[string]dynamicService `yaml:"services,omitempty"`
}

type dynamicRouter struct {
	Rule        string   `yaml:"rule"`
	EntryPoints []string `yaml:"entryPoints"`
	Service     string   `yaml:"service"`
}

type dynamicService struct {
	LoadBalancer dynamicLoadBalancer `yaml:"loadBalancer"`
}

type dynamicLoadBalancer struct {
	Servers []dynamicServer `yaml:"servers"`
}

type dynamicServer struct {
	URL string `yaml:"url"`
}

func writeRoutes(path string, records []Record) error {
	if len(records) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	cfg := dynamicConfig{HTTP: dynamicHTTP{
		Routers:  map[string]dynamicRouter{},
		Services: map[string]dynamicService{},
	}}
	for _, record := range records {
		id := routeID(record.Host)
		cfg.HTTP.Routers[id] = dynamicRouter{
			Rule:        "Host(`" + record.Host + "`)",
			EntryPoints: []string{"web"},
			Service:     id,
		}
		cfg.HTTP.Services[id] = dynamicService{
			LoadBalancer: dynamicLoadBalancer{
				Servers: []dynamicServer{{
					// The compose adapter explicitly attaches this exact
					// workload FQDN as a Docker-network alias.
					URL: fmt.Sprintf("http://%s:%d", record.Host, record.Port),
				}},
			},
		}
	}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return writeFileIfChanged(path, body, 0o600)
}

func routeID(host string) string {
	sum := sha256Bytes(host)
	return "share-" + sum
}

func sha256Bytes(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:6])
}

func readRecords(path string) ([]Record, error) {
	var records []Record
	if err := readJSON(path, &records); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return records, nil
}

func writeRecords(path string, records []Record) error {
	if len(records) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeJSON(path, records)
}

func readJSON(path string, value any) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, value); err != nil {
		return fmt.Errorf("share: parse %s: %w", path, err)
	}
	return nil
}

func writeJSON(path string, value any) error {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return writeFileIfChanged(path, body, 0o600)
}

func writeFileIfChanged(path string, body []byte, mode fs.FileMode) error {
	existing, err := os.ReadFile(path)
	if err == nil && bytes.Equal(existing, body) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
