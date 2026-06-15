package signaling

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// peerConn is the room goroutine's handle to a connected peer. Frames are delivered
// by a NON-blocking send to out, so a slow/stalled peer never blocks the room
// (AD-12 — drop slow peers); the transport owns the goroutine draining out.
type peerConn struct {
	id   PeerID
	role string
	slot SlotID // for obs source pages: the slot this conn sources ("" otherwise)
	out  chan<- Frame
}

// roomCmd is a closure run on the room goroutine with exclusive access to the pure
// state and the connection table. This is the actor's command channel payload (AD-2):
// all mutation funnels through here, so neither map needs a lock.
type roomCmd func(*roomState, map[PeerID]*peerConn)

// Room is a single live session's actor: one goroutine owns roomState and the conn
// table; every mutation arrives as a roomCmd. No locks on room state. A nil lockStore
// disables suppression-lock persistence (AD-22) — used by the pure transport tests.
type Room struct {
	id    string
	cmds  chan roomCmd
	done  chan struct{}
	locks LockPersistence
	log   *slog.Logger
}

func newRoom(id string, locks LockPersistence, log *slog.Logger) *Room {
	if log == nil {
		log = slog.Default()
	}
	return &Room{id: id, cmds: make(chan roomCmd, 64), done: make(chan struct{}), locks: locks, log: log}
}

// levelsTick is the audio-meter coalescing cadence (AD-13): every participant's last-reported
// level batches into one {t:levels} frame at ~6–7 Hz instead of riding the roster (no N² spam).
const levelsTick = 150 * time.Millisecond

// lockIOTimeout bounds a single suppression-lock read/write so a wedged disk can't stall the
// room goroutine indefinitely (AD-22 persistence is control-plane, not the per-frame hot path).
const lockIOTimeout = 5 * time.Second

func (r *Room) run() {
	state := newRoomState()
	conns := map[PeerID]*peerConn{}
	// Seed persisted suppression locks BEFORE the cmd loop, so they are in place before any peer
	// can Join — a force-muted guest reconnecting after a restart is locked from its first frame
	// (AD-22). This runs on the room goroutine; nobody is connected yet, so it blocks no one.
	r.loadLocks(state)
	// The audio-meter tick runs on THIS goroutine (race-free access to state + conns); it stays
	// quiet in an idle room (buildLevels returns nil), so an always-running ticker is cheap.
	ticker := time.NewTicker(levelsTick)
	defer ticker.Stop()
	for {
		select {
		case cmd := <-r.cmds:
			cmd(state, conns)
		case <-ticker.C:
			deliver(conns, state.buildLevels())
		case <-r.done:
			return
		}
	}
}

// loadLocks re-applies this session's persisted suppression locks on spawn (AD-22). A load
// failure is logged and the room continues unmuted rather than refusing to start — moderation
// is re-appliable, an un-startable room is not.
func (r *Room) loadLocks(state *roomState) {
	if r.locks == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), lockIOTimeout)
	defer cancel()
	locks, err := r.locks.LoadLocks(ctx, r.id)
	if err != nil {
		r.log.Error("loading suppression locks on room spawn", "session", r.id, "err", err)
		return
	}
	state.seedLocks(toSeeded(locks))
}

// persistLock writes one suppression-lock change through (AD-22), synchronously on the room
// goroutine. Moderation is a rare control-plane action and a local SQLite write is sub-ms, so
// this never blocks the per-frame hot path meaningfully; a write error is logged, not fatal
// (the in-memory lock stays authoritative for the live session — only restart survival degrades).
func (r *Room) persistLock(save bool, target PeerID, modality string, lk *lockState) {
	if r.locks == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), lockIOTimeout)
	defer cancel()
	var err error
	if save {
		applier := string(lk.applier)
		if lk.applier == "host" {
			applier = "" // the host has no pass → NULL applier_pass_id
		}
		err = r.locks.SaveLock(ctx, PersistedLock{Target: string(target), Modality: modality, ApplierRankFloor: rankName(lk.floor), Applier: applier})
	} else {
		err = r.locks.DeleteLock(ctx, string(target), modality)
	}
	if err != nil {
		r.log.Error("persisting suppression lock", "save", save, "target", target, "modality", modality, "err", err)
	}
}

func (r *Room) post(cmd roomCmd) {
	select {
	case r.cmds <- cmd:
	case <-r.done:
	}
}

// deliver routes reducer outbounds to peers' send channels, non-blocking (AD-12).
// Runs on the room goroutine, so conns access is race-free.
func deliver(conns map[PeerID]*peerConn, outs []outbound) {
	for _, o := range outs {
		c := conns[o.to]
		if c == nil {
			continue
		}
		select {
		case c.out <- o.frame:
		default:
			// Slow peer: drop the frame (AD-12). Peer eviction lands in M2/M3.
		}
	}
}

// Join registers a connection and enters it into the room, returning whether it was
// admitted. An OBS source page (role obs/obs_screen) also subscribes to its slot and is
// told the current binding.
//
// One connection per identity (EN-16): if a peer id is already connected, the prior
// connection is evicted (its out channel closed) before the new one is installed, so
// a duplicate id can't leave a stale conn that a later Leave would mis-target.
//
// Join returns false (admitting nothing) if the room is draining (Terminate ran) or its
// goroutine has stopped — so a connection that resolved this room just before a shutdown
// can't slip in after the terminate broadcast and strand itself with no teardown. The
// caller then closes the connection itself. Join is synchronous: it waits for the
// command to run, so the result reflects the room's actual state.
func (r *Room) Join(id PeerID, role, name string, slot SlotID, out chan<- Frame) bool {
	admitted := make(chan bool, 1)
	cmd := func(st *roomState, conns map[PeerID]*peerConn) {
		if st.terminating {
			admitted <- false
			return
		}
		if old := conns[id]; old != nil {
			// Tell the evicted client to reconnect (EN-9 transient) before closing
			// its channel, so a duplicate identity is a clean handover.
			select {
			case old.out <- Frame{T: "terminate", Reason: TerminateReconnect}:
			default:
			}
			close(old.out)
		}
		conns[id] = &peerConn{id: id, role: role, slot: slot, out: out}
		outs := st.join(id, role, name)
		if role == "obs" || role == "obs_screen" {
			outs = append(outs, st.attachSource(slot, id)...)
		}
		deliver(conns, outs)
		admitted <- true
	}
	select {
	case r.cmds <- cmd:
		// The command was enqueued, but the room goroutine may still exit on r.done
		// (Close) before running it — so wait on both, never just <-admitted, or a
		// Close racing the enqueue would block Join forever.
		select {
		case ok := <-admitted:
			return ok
		case <-r.done:
			return false
		}
	case <-r.done:
		return false
	}
}

// Leave removes a peer and CLOSES its out channel from the room goroutine — the only
// place that sends to it, so a send can never race a close. It is identity-checked by
// the out channel: a stale/evicted connection (a duplicate id that was already
// replaced) is a no-op, so it never tears down the connection that supplanted it.
func (r *Room) Leave(id PeerID, out chan<- Frame) {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		c := conns[id]
		if c == nil || c.out != out {
			return // not the current connection for this id; leave the live one alone
		}
		outs := st.leave(id)
		delete(conns, id)
		deliver(conns, outs)
		close(c.out)
	})
}

func (r *Room) Signal(from PeerID, f Frame) {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		deliver(conns, st.relaySignal(from, f))
	})
}

// Chat relays a backstage message to the room's participants, from-stamped (EN-7). The text is
// relayed and NEVER persisted or logged (EN-20): this path touches no store and no logger — the
// reducer is pure and the Room only delivers — so the guarantee holds by construction.
func (r *Room) Chat(from PeerID, text string) {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		deliver(conns, st.relayChat(from, text))
	})
}

// SetHand raises/lowers a participant's hand, folded into the roster's handRaised. A participant
// controls its OWN hand; the host may dismiss (lower) another's. Authority is enforced
// server-side against current rank (EN-7).
func (r *Room) SetHand(actor, target PeerID, raised bool) {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		deliver(conns, st.setHand(actor, target, raised))
	})
}

// Kick removes a target from the room (D-25). Authority (rank strictly above) is enforced
// server-side. When authorized, it runs `invalidate` (the caller's token-revocation closure)
// FIRST — on the room goroutine, before the teardown evicts the socket — so a reconnect is
// already refused (refuse-rejoin, race-free); then it clears the target's slot (epoch bump
// before the teardown, EN-3), broadcasts peer-left, sends the target a terminal
// {t:terminate,kicked} (EN-9), and evicts its connection. An unauthorized kick is a no-op and
// `invalidate` is NOT called. invalidate may be nil (tests).
func (r *Room) Kick(actor, target PeerID, invalidate func()) {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		if !st.canKick(actor, target) {
			return
		}
		if invalidate != nil {
			invalidate() // revoke the target's token BEFORE teardown — refuse-rejoin is race-free
		}
		deliver(conns, st.kickPeer(target))
		// Evict the connection AFTER delivering the terminate frame: the buffered terminate
		// flushes through the single writer before the closed channel ends it and shuts the socket.
		if c := conns[target]; c != nil {
			delete(conns, target)
			close(c.out)
		}
	})
}

// ApplyState folds a participant's self-presence ({t:state}, EN-7) into the roster: each
// provided (non-nil) modality updates and, on a real change, every viewer's roster
// re-broadcasts. An absent modality is left unchanged (a meter-only update must not clobber
// presence). The audio meter, if provided, is stored in-memory only and rides the batched
// {t:levels} tick (AD-13), never the roster — so a level-only update never re-broadcasts.
func (r *Room) ApplyState(id PeerID, cam, mic, screen *bool, level *float64) {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		deliver(conns, st.applyState(id, cam, mic, screen, level))
	})
}

// ApplyStats folds a publisher's {t:stats} self-report (AD-21) into its roster entry on the room
// goroutine, broadcasting only when signal/degraded materially changed (per-frame stats stay in
// memory, EN-11).
func (r *Room) ApplyStats(id PeerID, signal, rttMs int, degraded *DegradedView) {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		deliver(conns, st.applyStats(id, signal, rttMs, degraded))
	})
}

// RecoverQuality broadcasts a host "bump quality now" to every participant (AD-21/D-34). Authority
// is checked at the dispatch layer (host-only); this is a pure broadcast on the room goroutine.
func (r *Room) RecoverQuality() {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		deliver(conns, st.recoverQuality())
	})
}

// Force applies a suppression force (force-mute/force-no-cam/force-no-share) from actor onto
// target's modality (D-13/EN-7). Authority is enforced server-side against current rank — a
// guest's or peer's attempt is a no-op. Modality is mic | cam | share.
func (r *Room) Force(actor, target PeerID, modality string) {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		before := st.lockOn(target, modality)
		deliver(conns, st.force(actor, target, modality))
		// Persist only a genuine change (a newly applied or raised lock is a NEW *lockState, so
		// the pointer differs); a no-op or rejected force writes nothing (AD-22).
		if after := st.lockOn(target, modality); after != nil && after != before {
			r.persistLock(true, target, modality, after)
		}
	})
}

// Release lifts a suppression lock on target's modality (D-13/EN-7). The target can never
// self-release; authority is the actor's current rank ≥ the lock floor (the host always can).
func (r *Room) Release(actor, target PeerID, modality string) {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		had := st.locked(target, modality)
		deliver(conns, st.release(actor, target, modality))
		// Delete the row only when a lock was actually released (an unauthorized release leaves
		// it in place; a release of an absent lock writes nothing) (AD-22).
		if had && !st.locked(target, modality) {
			r.persistLock(false, target, modality, nil)
		}
	})
}

// SetRole promotes/demotes a participant between co-host and guest (D-15). Authority (host-only,
// target strictly below) is enforced server-side against current rank; a non-host actor or an
// invalid role is a no-op. A demoted ex-co-host's suppression locks re-evaluate safely (it keeps
// the floors but loses release rights; the host always can release).
func (r *Room) SetRole(actor, target PeerID, newRole string) {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		deliver(conns, st.setRole(actor, target, newRole))
	})
}

// DeliverTo enqueues a frame to one peer's connection (non-blocking, AD-12). It runs on
// the room goroutine — the sole owner of the conn table and the out channels — so it can
// never race the channel close on eviction/leave/terminate. Used for per-connection
// control frames that don't mutate room state, e.g. an {t:ice-refresh} re-mint.
func (r *Room) DeliverTo(id PeerID, f Frame) {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		deliver(conns, []outbound{{to: id, frame: f}})
	})
}

// RotateSource tears down an OBS source-page connection whose slot token was just rotated
// (D-22 "my URLs leaked"). It frees the slot's source pointer (degrading its on-air to
// unknown, EN-3) and tells the others (via leave), sends the source a TERMINAL
// {t:terminate,token-rotated} so the page stops — not reconnect — and the host re-pastes the
// fresh URL, then evicts the connection (the buffered terminate flushes through the single
// writer before the socket closes). A no-op if no such source is connected (rotated while the
// source was offline) or if the id isn't an OBS source — a participant is never terminated
// here. Host authority is enforced at the web layer (RequireHost); this is the teardown only.
func (r *Room) RotateSource(source PeerID) {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		p := st.peers[source]
		if p == nil || isParticipant(p.role) {
			return
		}
		c := conns[source]
		deliver(conns, st.leave(source)) // peer-left + roster to the OTHER peers (non-blocking, AD-12)
		if c == nil {
			return
		}
		delete(conns, source)
		// The terminal token-rotated frame must NOT be silently dropped on a full queue (RF-16) —
		// else the OBS page sees a bare close, treats it as transient, and reconnect-loops the dead
		// token. Send it with a budget (like Terminate) in a goroutine — now the sole owner of
		// c.out — so a wedged socket can't stall the room goroutine; a stuck peer is given up on and
		// still closed.
		go func() {
			t := time.NewTimer(terminateBudget)
			defer t.Stop()
			select {
			case c.out <- Frame{T: "terminate", Reason: TerminateTokenRotated}:
			case <-t.C:
			}
			close(c.out)
		}()
	})
}

func (r *Room) Rebind(slot SlotID, occupant PeerID) {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		deliver(conns, st.rebindSlot(slot, occupant))
	})
}

// ResumeBind replays a guest's persisted slot binding on join (D-40) WITHOUT displacing a
// different live occupant — see roomState.resumeBind. Used by the /ws join replay; the host's
// explicit greenroom (re)bind still displaces via Rebind/RebindOrVacate.
func (r *Room) ResumeBind(slot SlotID, occupant PeerID) {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		deliver(conns, st.resumeBind(slot, occupant))
	})
}

// RebindOrVacate binds the slot to occupant if it is connected, else VACATES the slot — so a
// greenroom (re)bind whose new occupant is OFFLINE drops the slot to placeholder instead of
// stranding the displaced prior occupant live (see rebindOrVacate).
func (r *Room) RebindOrVacate(slot SlotID, occupant PeerID) {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		deliver(conns, st.rebindOrVacate(slot, occupant))
	})
}

func (r *Room) Unbind(slot SlotID) {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		deliver(conns, st.unbindSlot(slot))
	})
}

// VacateOccupant clears any cam slot the peer occupies (the greenroom "unassign"), keyed on the
// room's own live occupancy rather than a caller label — so a concurrent move of the same guest
// can't strand a stale slot bound (see vacateOccupant).
func (r *Room) VacateOccupant(occupant PeerID) {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		deliver(conns, st.vacateOccupant(occupant))
	})
}

func (r *Room) ObsActive(slot SlotID, active bool, epoch int) {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		deliver(conns, st.obsSourceActive(slot, active, epoch))
	})
}

// ObsStreaming reflects OBS's broadcast-level "we're live" state (D-24) to the room. Global,
// not slot-scoped, so it carries no epoch.
func (r *Room) ObsStreaming(active bool) {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		deliver(conns, st.obsStreaming(active))
	})
}

// terminateBudget bounds how long Terminate waits PER PEER to enqueue a terminate frame
// into a backed-up out queue during a drain.
const terminateBudget = 2 * time.Second

// Terminate sends a terminate frame to every connected peer, then closes their out
// channels and clears the roster. Used for graceful shutdown (RF-21): the transient
// reason "reconnect" tells clients to retry with backoff (keyed by pass_id), so a
// deploy/restart isn't a hard mass-drop.
//
// terminate is a terminal control frame, so it must not be silently dropped on a full
// queue (RF-16). Each peer's send therefore BLOCKS until its writeLoop drains a slot,
// with its OWN budget so one wedged socket can't consume the time for the others; the
// peers are handled CONCURRENTLY so total time is ~one budget, not the sum. A genuinely
// stuck peer is given up on (its socket is dead anyway) and still closed. It runs on the
// room goroutine, so a concurrent readLoop Leave for a now-removed conn is a no-op
// (identity-checked).
func (r *Room) Terminate(reason string) {
	done := make(chan struct{})
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		st.terminating = true // refuse any late Join that arrives after this command
		var wg sync.WaitGroup
		for id, c := range conns {
			wg.Add(1)
			go func(c *peerConn) {
				defer wg.Done()
				t := time.NewTimer(terminateBudget)
				defer t.Stop()
				select {
				case c.out <- Frame{T: "terminate", Reason: reason}:
				case <-t.C: // this peer is wedged; give up on it
				}
				close(c.out)
			}(c)
			delete(conns, id)
		}
		wg.Wait()
		close(done)
	})
	select {
	case <-done:
	case <-r.done:
	}
}

// Close stops the room goroutine.
func (r *Room) Close() { close(r.done) }
