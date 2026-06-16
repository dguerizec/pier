package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dguerizec/pier/internal/infra"
	"github.com/dguerizec/pier/internal/state"
)

// SSE pacing. Two-second polling matches the dashboard's auto-refresh
// default and keeps `docker inspect` load tolerable for ~50 workloads.
// Heartbeats let clients detect a dead TCP connection within 30s when
// no live transitions are flowing.
const (
	pollInterval      = 2 * time.Second
	heartbeatInterval = 30 * time.Second
	subscriberBuffer  = 16
)

// sseEvent is one named SSE message — Event becomes the `event:` line,
// Data is JSON-marshaled into the `data:` line.
type sseEvent struct {
	Event string
	Data  any
}

// eventHub is the shared pub-sub for SSE clients. A single goroutine polls
// the state DB + docker every pollInterval, computes diffs against the
// previous snapshot, and fans events out to every connected subscriber.
// Slow consumers get events dropped (best-effort) rather than blocking
// the poll loop.
type eventHub struct {
	paths *infra.Paths
	cfg   *infra.Config

	mu           sync.Mutex
	subscribers  map[chan sseEvent]struct{}
	statuses     map[string]string // "project/slug" → docker State.Status
	doctorFailed bool
	started      bool
}

func newEventHub(paths *infra.Paths, cfg *infra.Config) *eventHub {
	return &eventHub{
		paths:       paths,
		cfg:         cfg,
		subscribers: map[chan sseEvent]struct{}{},
		statuses:    map[string]string{},
	}
}

// start kicks off the poll loop in a goroutine. Idempotent — safe to call
// from multiple places (only one loop ever runs).
func (h *eventHub) start(ctx context.Context) {
	h.mu.Lock()
	if h.started {
		h.mu.Unlock()
		return
	}
	h.started = true
	h.mu.Unlock()

	// Prime the snapshot synchronously so the first poll tick doesn't
	// emit `workload.up` events for every existing row.
	h.poll(false)

	go h.run(ctx)
}

func (h *eventHub) run(ctx context.Context) {
	poll := time.NewTicker(pollInterval)
	defer poll.Stop()
	hb := time.NewTicker(heartbeatInterval)
	defer hb.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			h.poll(true)
		case <-hb.C:
			h.broadcast(sseEvent{Event: "ping"})
		}
	}
}

// poll reads the current world (state DB + docker + doctor) and broadcasts
// one event per detected transition. When emit is false the snapshot gets
// initialized without firing events — used for the first call so we don't
// flood new clients with synthetic up events for already-running workloads.
func (h *eventHub) poll(emit bool) {
	store, err := state.Open(h.paths.StateDB)
	if err != nil {
		return
	}
	list, err := store.List()
	store.Close()
	if err != nil {
		return
	}

	current := map[string]*state.Workload{}
	statuses := map[string]string{}
	for _, w := range list {
		key := w.Project + "/" + w.Slug
		current[key] = w
		statuses[key] = containerStatus(w)
	}

	h.mu.Lock()
	prev := h.statuses
	h.statuses = statuses
	prevDoctor := h.doctorFailed
	h.mu.Unlock()

	if !emit {
		return
	}

	var events []sseEvent
	for key, status := range statuses {
		prevStatus, existed := prev[key]
		wl := current[key]
		if !existed {
			events = append(events, sseEvent{
				Event: "workload.up",
				Data:  buildAPIWorkload(h.cfg, wl),
			})
			continue
		}
		if status == prevStatus {
			continue
		}
		events = append(events, sseEvent{
			Event: classifyTransition(prevStatus, status),
			Data:  buildAPIWorkload(h.cfg, wl),
		})
	}
	for key := range prev {
		if _, still := statuses[key]; !still {
			project, slug, _ := strings.Cut(key, "/")
			events = append(events, sseEvent{
				Event: "workload.removed",
				Data: map[string]string{
					"project": project,
					"slug":    slug,
				},
			})
		}
	}

	failed := infra.Diagnose().HasFailures()
	if failed != prevDoctor {
		h.mu.Lock()
		h.doctorFailed = failed
		h.mu.Unlock()
		ev := "doctor.recovered"
		if failed {
			ev = "doctor.fail"
		}
		events = append(events, sseEvent{Event: ev})
	}

	for _, ev := range events {
		h.broadcast(ev)
	}
}

// classifyTransition picks the event name for a status change of a workload
// still present in the state DB. Removal is handled separately. The
// "running" target always means up — fresh start or post-crash restart;
// callers don't need to distinguish.
func classifyTransition(prev, current string) string {
	switch current {
	case "running":
		return "workload.up"
	case "restarting":
		return "workload.restarting"
	default:
		return "workload.crashed"
	}
}

func (h *eventHub) subscribe() chan sseEvent {
	ch := make(chan sseEvent, subscriberBuffer)
	h.mu.Lock()
	h.subscribers[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *eventHub) unsubscribe(ch chan sseEvent) {
	h.mu.Lock()
	if _, ok := h.subscribers[ch]; ok {
		delete(h.subscribers, ch)
		close(ch)
	}
	h.mu.Unlock()
}

func (h *eventHub) broadcast(ev sseEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subscribers {
		select {
		case ch <- ev:
		default:
			// Slow consumer — drop to keep the poll loop responsive.
			// Client will catch up at next reconnect via state.snapshot.
		}
	}
}

// currentSnapshot returns the full workload list as it should be sent on
// the initial state.snapshot event when a new client connects.
func (h *eventHub) currentSnapshot() []apiWorkload {
	store, err := state.Open(h.paths.StateDB)
	if err != nil {
		return []apiWorkload{}
	}
	defer store.Close()
	list, err := store.List()
	if err != nil {
		return []apiWorkload{}
	}
	out := make([]apiWorkload, 0, len(list))
	for _, w := range list {
		out = append(out, buildAPIWorkload(h.cfg, w))
	}
	return out
}

// streamEvents serves a long-lived Server-Sent Events stream — initial
// state.snapshot, then live transitions, with periodic heartbeat pings.
// The connection ends when the client disconnects or the hub closes the
// subscriber channel.
func (h *apiHandler) streamEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := h.hub.subscribe()
	defer h.hub.unsubscribe(ch)

	if err := writeSSE(w, "state.snapshot", h.hub.currentSnapshot()); err != nil {
		return
	}
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if err := writeSSE(w, ev.Event, ev.Data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeSSE writes one SSE record. `ping` events have no payload — clients
// only care about the event name there.
func writeSSE(w io.Writer, event string, data any) error {
	if data == nil {
		_, err := fmt.Fprintf(w, "event: %s\ndata: {}\n\n", event)
		return err
	}
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body)
	return err
}
