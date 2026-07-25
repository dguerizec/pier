package share

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

type fakeRuntime struct {
	states   map[string]ContainerState
	starts   []gatewaySpec
	restarts map[string]bool
	removed  []string
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{
		states:   map[string]ContainerState{},
		restarts: map[string]bool{},
	}
}

func (f *fakeRuntime) State(name string) (ContainerState, error) {
	if !strings.HasPrefix(name, "pier-share-") {
		return ContainerState{Exists: true, Running: true}, nil
	}
	return f.states[name], nil
}

func (f *fakeRuntime) Start(spec gatewaySpec) error {
	f.starts = append(f.starts, spec)
	_ = os.Remove(spec.DataPath + "/session.json")
	_ = os.Remove(spec.DataPath + "/dynamic/session.yml")
	if err := os.WriteFile(spec.ReadyPath, []byte{}, 0o600); err != nil {
		return err
	}
	f.states[spec.Name] = ContainerState{Exists: true, Running: true}
	f.restarts[spec.Name] = spec.Restart
	return nil
}

func (f *fakeRuntime) SetRestart(name string, persistent bool) error {
	f.restarts[name] = persistent
	return nil
}

func (f *fakeRuntime) Remove(name string) error {
	f.removed = append(f.removed, name)
	delete(f.states, name)
	delete(f.restarts, name)
	return nil
}

func testManager(t *testing.T) (*Manager, *fakeRuntime) {
	t.Helper()
	runtime := newFakeRuntime()
	manager := &Manager{
		Paths:   NewPaths(t.TempDir()),
		Network: "pier",
		Runtime: runtime,
	}
	return manager, runtime
}

func TestManagerSessionAddListAndRemove(t *testing.T) {
	manager, runtime := testManager(t)
	address := Address{Interface: "enp3s0", IP: "192.168.1.42"}
	candidates := testCandidates()

	added, err := manager.Add(candidates, address, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 3 || added[0].Persistent {
		t.Fatalf("added = %+v", added)
	}
	if len(runtime.starts) != 1 || runtime.restarts[gatewayContainer(address.IP)] {
		t.Fatalf("gateway should be session-scoped: %+v", runtime)
	}
	paths := manager.Paths.forGateway(gatewayID(address.IP))
	body, err := os.ReadFile(paths.SessionYML)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.PersistentYML); !os.IsNotExist(err) {
		t.Fatalf("empty persistent config should not exist: %v", err)
	}
	if strings.Contains(string(body), "HostRegexp") || strings.Contains(string(body), "Host(`*") {
		t.Fatalf("wildcard leaked into Traefik config:\n%s", body)
	}
	for _, candidate := range candidates {
		if !strings.Contains(string(body), "Host(`"+candidate.Host+"`)") {
			t.Fatalf("missing exact host %s:\n%s", candidate.Host, body)
		}
		if !strings.Contains(string(body), "http://"+candidate.Host+":") {
			t.Fatalf("missing Docker-network alias target %s:\n%s", candidate.Host, body)
		}
	}

	records, err := manager.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("list = %+v", records)
	}

	hosts := []string{candidates[0].Host, candidates[1].Host, candidates[2].Host}
	removed, err := manager.Remove("jobo", "main", hosts)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 3 {
		t.Fatalf("removed = %d", removed)
	}
	if len(runtime.removed) != 1 {
		t.Fatalf("gateway should be removed: %+v", runtime.removed)
	}
}

func TestManagerPersistUpgradesSessionRoute(t *testing.T) {
	manager, runtime := testManager(t)
	address := Address{Interface: "enp3s0", IP: "192.168.1.42"}
	candidate := testCandidates()[0]
	if _, err := manager.Add([]Candidate{candidate}, address, false); err != nil {
		t.Fatal(err)
	}
	added, err := manager.Add([]Candidate{candidate}, address, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || !added[0].Persistent {
		t.Fatalf("added = %+v", added)
	}
	if !runtime.restarts[gatewayContainer(address.IP)] {
		t.Fatal("persistent route should promote restart policy")
	}
	paths := manager.Paths.forGateway(gatewayID(address.IP))
	persistent, err := readRecords(paths.Persistent)
	if err != nil {
		t.Fatal(err)
	}
	session, err := readRecords(paths.Session)
	if err != nil {
		t.Fatal(err)
	}
	if len(persistent) != 1 || len(session) != 0 {
		t.Fatalf("persistent=%+v session=%+v", persistent, session)
	}
}

func TestManagerSessionDisappearsWhenGatewayRestarts(t *testing.T) {
	manager, runtime := testManager(t)
	address := Address{Interface: "wlan0", IP: "10.0.0.8"}
	if _, err := manager.Add([]Candidate{testCandidates()[0]}, address, false); err != nil {
		t.Fatal(err)
	}
	paths := manager.Paths.forGateway(gatewayID(address.IP))
	// This mirrors the gateway entrypoint cleanup on a daemon restart.
	_ = os.Remove(paths.Session)
	_ = os.Remove(paths.SessionYML)
	runtime.states[gatewayContainer(address.IP)] = ContainerState{Exists: true, Running: true}

	records, err := manager.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("session routes survived restart: %+v", records)
	}
}

func TestManagerRemoveWorktreeCleansPersistentAndSessionRoutes(t *testing.T) {
	manager, runtime := testManager(t)
	address := Address{Interface: "enp3s0", IP: "192.168.1.42"}
	candidates := testCandidates()
	if _, err := manager.Add([]Candidate{candidates[0]}, address, true); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Add([]Candidate{candidates[1]}, address, false); err != nil {
		t.Fatal(err)
	}
	removed, err := manager.RemoveWorktree("/work/jobo")
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d", removed)
	}
	if len(runtime.removed) != 1 {
		t.Fatalf("gateway not removed: %+v", runtime.removed)
	}
}

func TestManagerRejectsSameHostOnAnotherAddress(t *testing.T) {
	manager, _ := testManager(t)
	candidate := testCandidates()[0]
	if _, err := manager.Add([]Candidate{candidate}, Address{Interface: "eth0", IP: "192.168.1.42"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Add([]Candidate{candidate}, Address{Interface: "wlan0", IP: "10.0.0.8"}, false); err == nil {
		t.Fatal("duplicate host on a second address should fail")
	}
}

func TestManagerSerializesConcurrentAdds(t *testing.T) {
	manager, _ := testManager(t)
	address := Address{Interface: "enp3s0", IP: "192.168.1.42"}
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			host := fmt.Sprintf("service-%d.main.jobo.lap.test", i)
			_, err := manager.Add([]Candidate{{
				Host:         host,
				Project:      "jobo",
				Slug:         "main",
				WorktreePath: "/work/jobo",
				Service:      fmt.Sprintf("service-%d", i),
				HostLabel:    fmt.Sprintf("service-%d", i),
				Container:    fmt.Sprintf("jobo-main-service-%d", i),
				Port:         8000 + i,
			}}, address, false)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	records, err := manager.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 10 {
		t.Fatalf("records = %d, want 10", len(records))
	}
}
