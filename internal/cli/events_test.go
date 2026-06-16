package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dguerizec/pier/internal/infra"
	"github.com/dguerizec/pier/internal/state"
)

func TestClassifyTransition(t *testing.T) {
	cases := []struct {
		prev, current, want string
	}{
		{"exited", "running", "workload.up"},
		{"unknown", "running", "workload.up"},
		{"running", "exited", "workload.crashed"},
		{"running", "dead", "workload.crashed"},
		{"running", "restarting", "workload.restarting"},
		{"exited", "restarting", "workload.restarting"},
		{"running", "paused", "workload.crashed"}, // grouped with non-running
	}
	for _, tc := range cases {
		if got := classifyTransition(tc.prev, tc.current); got != tc.want {
			t.Errorf("classifyTransition(%q,%q) = %q, want %q", tc.prev, tc.current, got, tc.want)
		}
	}
}

func TestEventHubBroadcastDelivers(t *testing.T) {
	hub := newEventHub(&infra.Paths{}, &infra.Config{})
	ch := hub.subscribe()
	defer hub.unsubscribe(ch)

	hub.broadcast(sseEvent{Event: "ping"})
	select {
	case got := <-ch:
		if got.Event != "ping" {
			t.Errorf("got event %q, want ping", got.Event)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("subscriber never received broadcast")
	}
}

func TestEventHubBroadcastDropsSlowConsumer(t *testing.T) {
	hub := newEventHub(&infra.Paths{}, &infra.Config{})
	ch := hub.subscribe()
	defer hub.unsubscribe(ch)

	// Saturate the buffer + one extra. The extra must be dropped — not
	// block — otherwise a stuck client freezes the poll loop.
	for i := 0; i < subscriberBuffer+5; i++ {
		hub.broadcast(sseEvent{Event: "ping"})
	}
	if len(ch) != subscriberBuffer {
		t.Errorf("buffered events = %d, want %d (extras dropped)", len(ch), subscriberBuffer)
	}
}

func TestEventHubUnsubscribeClosesChannel(t *testing.T) {
	hub := newEventHub(&infra.Paths{}, &infra.Config{})
	ch := hub.subscribe()
	hub.unsubscribe(ch)
	_, ok := <-ch
	if ok {
		t.Error("channel should be closed after unsubscribe")
	}
}

func TestWriteSSEFormat(t *testing.T) {
	var buf bytes.Buffer
	if err := writeSSE(&buf, "workload.up", map[string]string{"slug": "main"}); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.HasPrefix(got, "event: workload.up\n") {
		t.Errorf("missing event line: %q", got)
	}
	if !strings.Contains(got, `"slug":"main"`) {
		t.Errorf("missing payload: %q", got)
	}
	if !strings.HasSuffix(got, "\n\n") {
		t.Errorf("missing terminator: %q", got)
	}
}

func TestWriteSSENilData(t *testing.T) {
	// `ping` and `doctor.fail` events carry no payload but SSE still
	// requires a `data:` line per RFC, otherwise some clients ignore.
	var buf bytes.Buffer
	if err := writeSSE(&buf, "ping", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "data: {}\n") {
		t.Errorf("nil payload should still emit a data line, got %q", buf.String())
	}
}

func TestStreamEventsSendsSnapshot(t *testing.T) {
	dir := t.TempDir()
	paths := &infra.Paths{Root: dir, StateDB: filepath.Join(dir, "state.db")}
	cfg := &infra.Config{Mode: infra.ModeLocal, TLD: "test", BindIP: "127.0.0.1"}

	store, err := state.Open(paths.StateDB)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	hub := newEventHub(paths, cfg)
	api := &apiHandler{paths: paths, cfg: cfg, hub: hub}
	mux := http.NewServeMux()
	api.register(mux)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	// Push a synthetic event through the hub *after* the handler has
	// written its snapshot, then cancel. We do this in a goroutine so
	// we can drive the handler synchronously.
	done := make(chan struct{})
	go func() {
		mux.ServeHTTP(rec, req)
		close(done)
	}()

	// Give the handler a tick to subscribe + emit snapshot.
	time.Sleep(50 * time.Millisecond)
	hub.broadcast(sseEvent{Event: "workload.up", Data: map[string]string{"slug": "main"}})
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, body)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("content-type = %q", got)
	}

	scanner := bufio.NewScanner(strings.NewReader(body))
	var events []string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			events = append(events, strings.TrimPrefix(line, "event: "))
		}
	}
	if len(events) < 2 || events[0] != "state.snapshot" {
		t.Errorf("expected first event = state.snapshot, got %v", events)
	}
	if !contains(events, "workload.up") {
		t.Errorf("expected workload.up event, got %v", events)
	}
	// state.snapshot data must be a JSON array (even if empty).
	if !strings.Contains(body, "data: []") {
		t.Errorf("expected snapshot data []: %s", body)
	}
}

func TestCurrentSnapshotEmpty(t *testing.T) {
	dir := t.TempDir()
	paths := &infra.Paths{Root: dir, StateDB: filepath.Join(dir, "state.db")}
	cfg := &infra.Config{TLD: "test"}
	store, err := state.Open(paths.StateDB)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	hub := newEventHub(paths, cfg)
	got := hub.currentSnapshot()
	if got == nil {
		t.Fatal("snapshot must never be nil — clients break on null")
	}
	b, _ := json.Marshal(got)
	if string(b) != "[]" {
		t.Errorf("snapshot = %s, want []", string(b))
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
