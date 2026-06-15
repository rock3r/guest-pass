package signaling

import (
	"log/slog"
	"sync"
)

// Hub is the supervisor above the room actors (AD-2a): it owns the room registry,
// spawns/reaps room goroutines, and routes connections. It never reaches into a
// room's private state — cross-room work goes through a room's command channel. The
// mutex guards only the registry map (not room state), so the no-locks-on-room-state
// invariant holds.
type Hub struct {
	mu     sync.Mutex
	rooms  map[string]*Room
	closed bool // set by Shutdown; once true, Room never creates a new room
	// locks backs suppression-lock persistence (AD-22); nil disables it (pure tests). log is
	// the room logger. Both are handed to every room the hub spawns.
	locks LockPersistence
	log   *slog.Logger
}

// NewHub builds the room supervisor. lockStore persists suppression locks (AD-22); pass nil to
// disable persistence (the pure transport/reducer tests). log may be nil (rooms default to slog).
func NewHub(lockStore LockPersistence, log *slog.Logger) *Hub {
	return &Hub{rooms: map[string]*Room{}, locks: lockStore, log: log}
}

// Room returns the room for a session, creating and starting it on first use. It returns
// nil once the hub has been shut down, so a /ws handler that hijacked its connection
// before the drain can't race in and spawn a fresh, un-drained room that would leak and
// never receive a terminate; callers must handle nil by closing the connection.
func (h *Hub) Room(session string) *Room {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	r := h.rooms[session]
	if r == nil {
		r = newRoom(session, h.locks, h.log)
		go r.run()
		h.rooms[session] = r
	}
	return r
}

// TerminateSourceIfLive terminates the OBS source peer in the host's LIVE room, if one
// exists — WITHOUT spawning a room (a peek of the registry, not Room()). Backs D-22
// slot-token rotation: rotating while no stream is live is a DB-only update with no source
// to tear down, so this must not create an empty room as a side effect.
func (h *Hub) TerminateSourceIfLive(session string, source PeerID) {
	h.mu.Lock()
	r := h.rooms[session]
	h.mu.Unlock()
	if r != nil {
		r.RotateSource(source)
	}
}

// Shutdown gracefully terminates every live room for a server drain (RF-21): each room
// broadcasts a terminate frame with reason to its peers, then is stopped. The registry
// is cleared so no new work is routed to a stopping room.
func (h *Hub) Shutdown(reason string) {
	h.mu.Lock()
	h.closed = true
	rooms := make([]*Room, 0, len(h.rooms))
	for _, r := range h.rooms {
		rooms = append(rooms, r)
	}
	h.rooms = map[string]*Room{}
	h.mu.Unlock()

	// Terminate rooms CONCURRENTLY so the total drain time is bounded by a single room's
	// terminate budget, not the sum across rooms (which could blow the drain deadline).
	var wg sync.WaitGroup
	for _, r := range rooms {
		wg.Add(1)
		go func(r *Room) {
			defer wg.Done()
			r.Terminate(reason)
			r.Close()
		}(r)
	}
	wg.Wait()
}
