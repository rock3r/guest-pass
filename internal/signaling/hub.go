package signaling

import "sync"

// Hub is the supervisor above the room actors (AD-2a): it owns the room registry,
// spawns/reaps room goroutines, and routes connections. It never reaches into a
// room's private state — cross-room work goes through a room's command channel. The
// mutex guards only the registry map (not room state), so the no-locks-on-room-state
// invariant holds.
type Hub struct {
	mu    sync.Mutex
	rooms map[string]*Room
}

func NewHub() *Hub { return &Hub{rooms: map[string]*Room{}} }

// Room returns the room for a session, creating and starting it on first use.
func (h *Hub) Room(session string) *Room {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.rooms[session]
	if r == nil {
		r = newRoom(session)
		go r.run()
		h.rooms[session] = r
	}
	return r
}
