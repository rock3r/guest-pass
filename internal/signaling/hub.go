package signaling

import (
	"log/slog"
	"sync"
	"time"
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
	// graceWindow is the slot-binding grace on a transient guest drop (D-40), handed to every room
	// the hub spawns. <=0 lets newRoom fall back to defaultGraceWindow.
	graceWindow time.Duration
}

// HubOption configures a Hub at construction. Variadic so existing callers (and the many tests that
// build a bare hub) need no change.
type HubOption func(*Hub)

// WithGraceWindow sets the slot-binding grace window applied to every room the hub spawns (D-40/
// D-M5.5-3). The production value is config-backed (SLOT_GRACE_WINDOW); tests pass a short value to
// exercise expiry without waiting. A non-positive value falls back to defaultGraceWindow per room.
func WithGraceWindow(d time.Duration) HubOption {
	return func(h *Hub) { h.graceWindow = d }
}

// NewHub builds the room supervisor. lockStore persists suppression locks (AD-22); pass nil to
// disable persistence (the pure transport/reducer tests). log may be nil (rooms default to slog).
func NewHub(lockStore LockPersistence, log *slog.Logger, opts ...HubOption) *Hub {
	h := &Hub{rooms: map[string]*Room{}, locks: lockStore, log: log}
	for _, o := range opts {
		o(h)
	}
	return h
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
		r = newRoom(session, h.locks, h.log, h.graceWindow)
		go r.run()
		h.rooms[session] = r
	}
	return r
}

// RoomIfLive returns the host's live room, or nil WITHOUT spawning one — a peek of the
// registry, not Room(). Backs control actions (slot (re)bind, D-20) that are DB-only when no
// stream is live: binding a guest to a slot with no live room must not create an empty room.
func (h *Hub) RoomIfLive(session string) *Room {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.rooms[session]
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

// EndSession terminates the host's live room (if any) with reason and removes it from the
// registry, so guests and OBS sources get the terminal teardown and NO connection carries into
// the next session — rooms are keyed by host id, so without this an "end session" would leave the
// old peers live in the room the next stream reuses (D-40). The room is terminated/closed WHILE it
// is still the registry entry, THEN removed: during teardown a concurrent /ws handshake for this
// host resolves (via Hub.Room) to the draining room — whose Join refuses (terminating) or whose
// closed r.done rejects it — instead of spawning a fresh room that would survive the teardown and
// carry into the next session (codex). hub.mu is not held across the blocking Terminate, so other
// hub ops aren't stalled. A no-op when no room is live. The NEXT connection (after removal) spawns
// a fresh room.
func (h *Hub) EndSession(session, reason string) {
	h.mu.Lock()
	r := h.rooms[session]
	h.mu.Unlock()
	if r == nil {
		return
	}
	// Participants get the terminal reason (session-ended); host-global OBS sources get a
	// recoverable reconnect so they outlive the session and re-attach to the next one (codex).
	r.TerminateSession(reason) // marks the room draining (Join refuses), still discoverable
	r.Close()
	h.mu.Lock()
	if h.rooms[session] == r { // don't drop a room a racing start already replaced
		delete(h.rooms, session)
	}
	h.mu.Unlock()
}

// TerminateHostRoom terminates the host's live room (if any) sending EVERY peer — participants AND
// OBS source pages — the given TERMINAL reason, then deregisters it. Unlike EndSession (which gives
// sources a recoverable reconnect so they outlive a normal session end), this is for ACCOUNT
// ERASURE (DELETE /api/me, D-37/AC-5): the host's slot tokens are about to be deleted, so a source
// must STOP (a terminal reason → obs.js ends the reconnect loop, EN-9) rather than reconnect-loop
// against a now-dead token (codex). The room is terminated WHILE still registered (a racing connect
// resolves the draining room and is refused), THEN closed + removed. No-op when no room is live;
// never spawns one.
func (h *Hub) TerminateHostRoom(session, reason string) {
	h.mu.Lock()
	r := h.rooms[session]
	h.mu.Unlock()
	if r == nil {
		return
	}
	r.Terminate(reason) // EVERY peer (incl. sources) gets the terminal reason; marks draining
	r.Close()
	h.mu.Lock()
	if h.rooms[session] == r {
		delete(h.rooms, session)
	}
	h.mu.Unlock()
}

// EvictIfLive evicts the named peers from the host's live room (if any) with a terminal reason —
// a system teardown when their passes are deleted (stream delete), so the deleted stream's guests
// don't linger in the host-scoped room. No-op when no room is live or no targets are given; it
// never spawns a room (peek, not Room()).
func (h *Hub) EvictIfLive(session, reason string, targets []PeerID) {
	if len(targets) == 0 {
		return
	}
	h.mu.Lock()
	r := h.rooms[session]
	h.mu.Unlock()
	if r != nil {
		r.EvictPeers(reason, targets)
	}
}

// ParticipantCount returns the number of connected greenroom participants in the host's live room
// (0 if no room is live) — the idle-session reaper's non-destructive idleness probe (D-40). It
// peeks the registry (never spawns a room) and excludes OBS source pages, so a session held open
// only by a lingering source still reads as idle.
func (h *Hub) ParticipantCount(session string) int {
	h.mu.Lock()
	r := h.rooms[session]
	h.mu.Unlock()
	if r == nil {
		return 0
	}
	return r.ParticipantCount()
}

// ReapIfIdle ends the host's live session IFF no participant is connected — the reaper's atomic
// reap (D-40). It returns reaped=true when it ended the session (running onReaped, the caller's DB
// session-end) and reaped=false when a participant is connected (a reconnect won the poll→reap
// race, so the session is NOT idle and is left alone). The error is whatever onReaped returned.
//
// Race-safety (codex/bugbot): the reap and a reconnect must serialize so a reconnect can't spawn or
// join a fresh room for the session while it is being ended. So this routes through the SAME entry
// reconnects use — hub.Room (spawn-if-absent), giving even the "no live room" case (a session
// active since before a restart) a registered room to act as the gate. TerminateIfIdle then does
// the participant check + draining mark atomically on the room goroutine (FIFO-ordered with any
// concurrent Join), and onReaped runs WHILE the room is still registered + terminating — so a
// reconnect resolves to it via hub.Room and its Join is refused (EN-9). Only after onReaped does it
// Close + deregister. Returns reaped=false if the hub is draining (Shutdown owns that teardown).
func (h *Hub) ReapIfIdle(session string, onReaped func() error) (reaped bool, err error) {
	r := h.Room(session) // spawn-if-absent: a registered room to gate reconnects during the DB end
	if r == nil {
		return false, nil // hub draining; Shutdown handles teardown, don't reap underneath it
	}
	if !r.TerminateIfIdle() {
		return false, nil // a participant is connected (or reconnected in the race) — not idle
	}
	if onReaped != nil {
		err = onReaped() // room still registered + terminating → a reconnecting peer's Join is refused
	}
	r.Close()
	h.mu.Lock()
	if h.rooms[session] == r { // don't drop a room a racing start already replaced
		delete(h.rooms, session)
	}
	h.mu.Unlock()
	return true, err
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
