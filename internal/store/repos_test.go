package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// seedHost creates an active host and returns it.
func seedHost(t *testing.T, st *Store, sub string) *Host {
	t.Helper()
	h, err := st.CreateHost(context.Background(), CreateHostParams{
		GoogleSub: sub, Email: sub + "@example.com", Name: "Host", Status: HostActive,
	})
	if err != nil {
		t.Fatalf("seedHost: %v", err)
	}
	return h
}

func TestStreamRepo_CRUD(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	h := seedHost(t, st, "host-stream")

	dur := int64(60)
	s1, err := st.CreateStream(ctx, CreateStreamParams{HostID: h.ID, Title: "Show", DurationMin: &dur})
	if err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	if s1.Status != StreamDraft {
		t.Errorf("default status = %q, want draft", s1.Status)
	}

	got, err := st.GetStream(ctx, s1.ID)
	if err != nil || got.Title != "Show" || got.DurationMin == nil || *got.DurationMin != 60 {
		t.Fatalf("GetStream round-trip: %+v / %v", got, err)
	}

	// Second stream for list ordering.
	if _, err := st.CreateStream(ctx, CreateStreamParams{HostID: h.ID, Title: "Show 2"}); err != nil {
		t.Fatalf("CreateStream 2: %v", err)
	}
	list, err := st.ListStreamsByHost(ctx, h.ID)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListStreamsByHost = %d streams, %v", len(list), err)
	}

	got.Title = "Renamed"
	got.Status = StreamScheduled
	if err := st.UpdateStream(ctx, got); err != nil {
		t.Fatalf("UpdateStream: %v", err)
	}
	reload, _ := st.GetStream(ctx, s1.ID)
	if reload.Title != "Renamed" || reload.Status != StreamScheduled {
		t.Errorf("after update: %+v", reload)
	}

	if err := st.DeleteStream(ctx, s1.ID); err != nil {
		t.Fatalf("DeleteStream: %v", err)
	}
	if _, err := st.GetStream(ctx, s1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v, want ErrNotFound", err)
	}
	if err := st.DeleteStream(ctx, s1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double delete: %v, want ErrNotFound", err)
	}
}

func TestStreamRepo_ForeignKeyEnforced(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	if _, err := st.CreateStream(ctx, CreateStreamParams{HostID: "no-such-host", Title: "x"}); err == nil {
		t.Fatal("expected FK violation creating stream for missing host, got nil")
	}
}

func TestSlotRepo_CRUD(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	h := seedHost(t, st, "host-slot")

	idx := int64(1)
	sl, err := st.CreateSlot(ctx, CreateSlotParams{HostID: h.ID, Kind: SlotCam, Idx: &idx, SourceTokenHash: "hash-cam-1"})
	if err != nil {
		t.Fatalf("CreateSlot: %v", err)
	}
	if _, err := st.CreateSlot(ctx, CreateSlotParams{HostID: h.ID, Kind: SlotScreenshare, SourceTokenHash: "hash-screen"}); err != nil {
		t.Fatalf("CreateSlot screenshare: %v", err)
	}

	bySrc, err := st.GetSlotBySourceTokenHash(ctx, "hash-cam-1")
	if err != nil || bySrc.ID != sl.ID {
		t.Fatalf("GetSlotBySourceTokenHash: %+v / %v", bySrc, err)
	}
	if bySrc.Idx == nil || *bySrc.Idx != 1 || bySrc.Epoch != 0 {
		t.Errorf("slot fields: %+v", bySrc)
	}

	list, err := st.ListSlotsByHost(ctx, h.ID)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListSlotsByHost = %d, %v", len(list), err)
	}
	if _, err := st.GetSlotBySourceTokenHash(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing source token: %v, want ErrNotFound", err)
	}
}

func TestSlotRepo_SourceTokenUnique(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	h := seedHost(t, st, "host-slot-uniq")
	if _, err := st.CreateSlot(ctx, CreateSlotParams{HostID: h.ID, Kind: SlotCam, Idx: i64(1), SourceTokenHash: "dup"}); err != nil {
		t.Fatalf("first slot: %v", err)
	}
	// Different idx so this trips the source-token unique index, not the cam-idx one.
	if _, err := st.CreateSlot(ctx, CreateSlotParams{HostID: h.ID, Kind: SlotCam, Idx: i64(2), SourceTokenHash: "dup"}); err == nil {
		t.Fatal("expected UNIQUE violation on duplicate source_token_hash, got nil")
	}
}

func TestSlotRepo_OneCamPerIdx(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	h := seedHost(t, st, "host-cam-idx")
	if _, err := st.CreateSlot(ctx, CreateSlotParams{HostID: h.ID, Kind: SlotCam, Idx: i64(1), SourceTokenHash: "c1a"}); err != nil {
		t.Fatalf("first cam-1: %v", err)
	}
	// A second cam slot with the same idx for the same host is a duplicate fixed slot (D-20).
	if _, err := st.CreateSlot(ctx, CreateSlotParams{HostID: h.ID, Kind: SlotCam, Idx: i64(1), SourceTokenHash: "c1b"}); err == nil {
		t.Fatal("expected unique violation for duplicate cam idx, got nil")
	}
	// A different idx is fine.
	if _, err := st.CreateSlot(ctx, CreateSlotParams{HostID: h.ID, Kind: SlotCam, Idx: i64(2), SourceTokenHash: "c2"}); err != nil {
		t.Fatalf("cam-2: %v", err)
	}
}

// EnsureSlotPool backs idempotent Sources-tab provisioning (M4 PR-4): in one transaction it
// inserts only the missing slots and reports which it inserted, never overwriting an
// existing token — so re-opening the tab can't duplicate the pool or rotate tokens, and a
// concurrent first open gets an all-or-none reveal.
func TestSlotRepo_EnsurePoolIdempotent(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	h := seedHost(t, st, "host-ensure")

	specs := []SlotSpec{
		{Kind: SlotCam, Idx: i64(1), SourceTokenHash: "cam1-first"},
		{Kind: SlotScreenshare, SourceTokenHash: "scr-first"},
	}
	ins, err := st.EnsureSlotPool(ctx, h.ID, specs)
	if err != nil {
		t.Fatalf("first EnsureSlotPool: %v", err)
	}
	if len(ins) != 2 || !ins[0] || !ins[1] {
		t.Fatalf("first ensure inserted = %v, want both true", ins)
	}

	// A second ensure with NEW tokens is a no-op and keeps the originals (no overwrite).
	ins, err = st.EnsureSlotPool(ctx, h.ID, []SlotSpec{
		{Kind: SlotCam, Idx: i64(1), SourceTokenHash: "cam1-second"},
		{Kind: SlotScreenshare, SourceTokenHash: "scr-second"},
	})
	if err != nil {
		t.Fatalf("second EnsureSlotPool: %v", err)
	}
	if ins[0] || ins[1] {
		t.Fatalf("second ensure inserted = %v, want both false (idempotent)", ins)
	}
	if _, err := st.GetSlotBySourceTokenHash(ctx, "cam1-first"); err != nil {
		t.Fatalf("original cam token should still resolve: %v", err)
	}
	if _, err := st.GetSlotBySourceTokenHash(ctx, "cam1-second"); err == nil {
		t.Fatal("ensure overwrote the existing slot's token")
	}

	// A mixed call inserts only the genuinely-missing slot.
	ins, err = st.EnsureSlotPool(ctx, h.ID, []SlotSpec{
		{Kind: SlotCam, Idx: i64(1), SourceTokenHash: "cam1-x"}, // exists → false
		{Kind: SlotCam, Idx: i64(2), SourceTokenHash: "cam2"},   // new → true
	})
	if err != nil {
		t.Fatalf("mixed EnsureSlotPool: %v", err)
	}
	if ins[0] || !ins[1] {
		t.Fatalf("mixed ensure inserted = %v, want [false true]", ins)
	}
}

// Concurrent first opens of the Sources tab must produce an all-or-none reveal: exactly one
// caller inserts the whole missing pool (and reveals all its URLs) while the rest insert
// nothing — never a split where each caller created a different subset (codex/Bugbot, M4
// PR-4). Proven by running EnsureSlotPool from many goroutines at once.
func TestSlotRepo_EnsurePoolConcurrentAllOrNone(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	h := seedHost(t, st, "host-concurrent")

	const goroutines = 8
	specs := func(tag string) []SlotSpec {
		out := make([]SlotSpec, 0, 9)
		for i := int64(1); i <= 8; i++ {
			idx := i
			out = append(out, SlotSpec{Kind: SlotCam, Idx: &idx, SourceTokenHash: fmt.Sprintf("%s-cam-%d", tag, i)})
		}
		return append(out, SlotSpec{Kind: SlotScreenshare, SourceTokenHash: tag + "-scr"})
	}

	type result struct {
		anyInserted bool
		count       int
	}
	results := make(chan result, goroutines)
	start := make(chan struct{})
	for g := 0; g < goroutines; g++ {
		go func(tag string) {
			<-start
			ins, err := st.EnsureSlotPool(ctx, h.ID, specs(tag))
			if err != nil {
				results <- result{}
				t.Errorf("EnsureSlotPool: %v", err)
				return
			}
			r := result{}
			for _, b := range ins {
				if b {
					r.anyInserted = true
					r.count++
				}
			}
			results <- r
		}(fmt.Sprintf("g%d", g))
	}
	close(start)

	winners, totalInserted := 0, 0
	for i := 0; i < goroutines; i++ {
		r := <-results
		if r.anyInserted {
			winners++
			if r.count != 9 {
				t.Fatalf("a winning open inserted %d slots, want all 9 (all-or-none)", r.count)
			}
		}
		totalInserted += r.count
	}
	if winners != 1 {
		t.Fatalf("%d opens reported inserts, want exactly 1 (all-or-none reveal)", winners)
	}
	if totalInserted != 9 {
		t.Fatalf("total slots inserted = %d, want 9 (no duplicates, no split)", totalInserted)
	}
	if slots, _ := st.ListSlotsByHost(ctx, h.ID); len(slots) != 9 {
		t.Fatalf("pool has %d slots after concurrent ensure, want 9", len(slots))
	}
}

// RotateSlotToken backs the D-22 regenerate: the old hash stops resolving, the new one
// resolves, and the leak-detection metadata is cleared.
func TestSlotRepo_RotateToken(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	h := seedHost(t, st, "host-rotate")
	sl, err := st.CreateSlot(ctx, CreateSlotParams{HostID: h.ID, Kind: SlotCam, Idx: i64(1), SourceTokenHash: "old"})
	if err != nil {
		t.Fatalf("CreateSlot: %v", err)
	}
	if err := st.RecordSlotTokenUse(ctx, sl.ID, "203.0.113.9"); err != nil {
		t.Fatalf("RecordSlotTokenUse: %v", err)
	}

	if err := st.RotateSlotToken(ctx, sl.ID, "new"); err != nil {
		t.Fatalf("RotateSlotToken: %v", err)
	}
	if _, err := st.GetSlotBySourceTokenHash(ctx, "old"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old token still resolves after rotation: %v", err)
	}
	got, err := st.GetSlotBySourceTokenHash(ctx, "new")
	if err != nil || got.ID != sl.ID {
		t.Fatalf("new token does not resolve to the slot: %+v / %v", got, err)
	}
	if got.SourceTokenLastUsedAt != nil || got.SourceTokenLastSourceIP != nil {
		t.Fatalf("rotation must clear leak-detection metadata, got used=%v ip=%v",
			got.SourceTokenLastUsedAt, got.SourceTokenLastSourceIP)
	}
	if err := st.RotateSlotToken(ctx, "no-such-slot", "x"); err == nil {
		t.Fatal("rotating a missing slot should error")
	}
}

// RotateSlotTokens (rotate-all) is all-or-nothing: every slot's hash rotates together, and a
// batch naming a missing slot rotates NONE — so a mid-batch failure never leaves some slots on
// fresh, un-revealed tokens.
func TestSlotRepo_RotateTokensBatch(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	h := seedHost(t, st, "host-batch")
	c1, _ := st.CreateSlot(ctx, CreateSlotParams{HostID: h.ID, Kind: SlotCam, Idx: i64(1), SourceTokenHash: "c1-old"})
	scr, _ := st.CreateSlot(ctx, CreateSlotParams{HostID: h.ID, Kind: SlotScreenshare, SourceTokenHash: "scr-old"})

	if err := st.RotateSlotTokens(ctx, map[string]string{c1.ID: "c1-new", scr.ID: "scr-new"}); err != nil {
		t.Fatalf("RotateSlotTokens: %v", err)
	}
	for _, old := range []string{"c1-old", "scr-old"} {
		if _, err := st.GetSlotBySourceTokenHash(ctx, old); !errors.Is(err, ErrNotFound) {
			t.Fatalf("old token %q still resolves after rotate-all", old)
		}
	}
	for _, nw := range []string{"c1-new", "scr-new"} {
		if _, err := st.GetSlotBySourceTokenHash(ctx, nw); err != nil {
			t.Fatalf("new token %q does not resolve: %v", nw, err)
		}
	}

	// A batch naming a missing slot rolls back entirely — the existing slots keep "*-new".
	if err := st.RotateSlotTokens(ctx, map[string]string{c1.ID: "c1-doomed", "ghost": "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("batch with a missing slot = %v, want ErrNotFound", err)
	}
	if _, err := st.GetSlotBySourceTokenHash(ctx, "c1-new"); err != nil {
		t.Fatal("a failed batch must not rotate any slot (c1 should still be c1-new)")
	}
	if _, err := st.GetSlotBySourceTokenHash(ctx, "c1-doomed"); err == nil {
		t.Fatal("a failed batch rotated a slot anyway")
	}
}

func TestSlotRepo_SingletonKinds(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	h := seedHost(t, st, "host-singleton")
	for _, kind := range []string{SlotHost, SlotScreenshare} {
		if _, err := st.CreateSlot(ctx, CreateSlotParams{HostID: h.ID, Kind: kind, SourceTokenHash: kind + "-1"}); err != nil {
			t.Fatalf("first %s: %v", kind, err)
		}
		// At most one host slot and one screenshare slot per host (D-18/D-21/D-20).
		if _, err := st.CreateSlot(ctx, CreateSlotParams{HostID: h.ID, Kind: kind, SourceTokenHash: kind + "-2"}); err == nil {
			t.Fatalf("expected unique violation for a second %s slot, got nil", kind)
		}
	}
}

func TestSlotRepo_ShapeConstraint(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	h := seedHost(t, st, "host-shape")
	cases := []struct {
		name string
		p    CreateSlotParams
	}{
		{"cam without idx", CreateSlotParams{HostID: h.ID, Kind: SlotCam, SourceTokenHash: "s1"}},
		{"cam idx out of range", CreateSlotParams{HostID: h.ID, Kind: SlotCam, Idx: i64(9), SourceTokenHash: "s2"}},
		{"cam idx zero", CreateSlotParams{HostID: h.ID, Kind: SlotCam, Idx: i64(0), SourceTokenHash: "s3"}},
		{"screenshare with idx", CreateSlotParams{HostID: h.ID, Kind: SlotScreenshare, Idx: i64(1), SourceTokenHash: "s4"}},
		{"host with idx", CreateSlotParams{HostID: h.ID, Kind: SlotHost, Idx: i64(1), SourceTokenHash: "s5"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := st.CreateSlot(ctx, tc.p); err == nil {
				t.Fatalf("expected shape CHECK violation for %q, got nil", tc.name)
			}
		})
	}
}

func TestPassRepo_CRUDAndStatus(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	h := seedHost(t, st, "host-pass")
	stream, _ := st.CreateStream(ctx, CreateStreamParams{HostID: h.ID, Title: "Show"})

	name, email := "Guest", "guest@example.com"
	p, err := st.CreatePass(ctx, CreatePassParams{StreamID: stream.ID, Name: &name, Email: &email, TokenHash: "tok-hash-1", CanScreen: true})
	if err != nil {
		t.Fatalf("CreatePass: %v", err)
	}
	if p.Role != RoleGuest || p.Status != PassCreated {
		t.Errorf("defaults: role=%q status=%q", p.Role, p.Status)
	}

	byTok, err := st.GetPassByTokenHash(ctx, "tok-hash-1")
	if err != nil || byTok.ID != p.ID || !byTok.CanScreen {
		t.Fatalf("GetPassByTokenHash: %+v / %v", byTok, err)
	}

	if err := st.SetPassStatus(ctx, p.ID, PassSent); err != nil {
		t.Fatalf("SetPassStatus sent: %v", err)
	}
	reload, _ := st.GetPass(ctx, p.ID)
	if reload.Status != PassSent || reload.SentAt == nil {
		t.Errorf("after sent: status=%q sent_at=%v", reload.Status, reload.SentAt)
	}
	if err := st.SetPassStatus(ctx, p.ID, PassAccepted); err != nil {
		t.Fatalf("SetPassStatus accepted: %v", err)
	}
	reload, _ = st.GetPass(ctx, p.ID)
	if reload.Status != PassAccepted || reload.AcceptedAt == nil {
		t.Errorf("after accepted: status=%q accepted_at=%v", reload.Status, reload.AcceptedAt)
	}

	if _, err := st.GetPassByTokenHash(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing token: %v, want ErrNotFound", err)
	}
}

// SetPassRole and ReissuePass back the host's invites tab (M4 PR-3). Role flips guest↔cohost
// in place; re-issue rotates the token (old hash stops resolving — EN-5), returns the pass to
// "sent" stamping sent_at, and keeps the rest of the row.
func TestPassRepo_SetRoleAndReissue(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	h := seedHost(t, st, "host-reissue")
	stream, _ := st.CreateStream(ctx, CreateStreamParams{HostID: h.ID, Title: "Show"})
	past := int64(1)
	p, err := st.CreatePass(ctx, CreatePassParams{StreamID: stream.ID, TokenHash: "old-hash", Status: PassOpened, ExpiresAt: &past})
	if err != nil {
		t.Fatalf("CreatePass: %v", err)
	}

	// Role flip.
	if err := st.SetPassRole(ctx, p.ID, RoleCohost); err != nil {
		t.Fatalf("SetPassRole cohost: %v", err)
	}
	if got, _ := st.GetPass(ctx, p.ID); got.Role != RoleCohost {
		t.Fatalf("role = %q, want cohost", got.Role)
	}

	// Re-issue rotates the token + returns to sent.
	if err := st.ReissuePass(ctx, p.ID, "new-hash"); err != nil {
		t.Fatalf("ReissuePass: %v", err)
	}
	if _, err := st.GetPassByTokenHash(ctx, "old-hash"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old token still resolves after re-issue: %v", err)
	}
	got, err := st.GetPassByTokenHash(ctx, "new-hash")
	if err != nil || got.ID != p.ID {
		t.Fatalf("new token does not resolve to the pass: %+v / %v", got, err)
	}
	if got.Status != PassSent || got.SentAt == nil {
		t.Fatalf("after re-issue: status=%q sent_at=%v, want sent + stamped", got.Status, got.SentAt)
	}
	if got.ExpiresAt != nil {
		t.Fatalf("re-issue must clear expires_at (so the fresh link isn't born expired), got %v", *got.ExpiresAt)
	}

	// Both operations error on an unknown id (errIfNoRows).
	if err := st.SetPassRole(ctx, "nope", RoleGuest); err == nil {
		t.Fatal("SetPassRole on missing id should error")
	}
	if err := st.ReissuePass(ctx, "nope", "x"); err == nil {
		t.Fatal("ReissuePass on missing id should error")
	}
}

// Re-assigning a slot clears it from EVERY other row on that (stream, slot) — including a
// retired row that kept a stale slot_id when it was revoked/expired (codex, M4 PR-6). Without
// that cleanup the stale binding survives, and a later Re-issue re-activates the row back into
// the partial unique index, colliding with the slot's current active occupant. Re-issue itself
// deliberately leaves slot_id alone (so it can't silently unbind a connected guest from the
// live room), which is exactly why the displacement, not re-issue, must do the cleanup.
func TestPassRepo_RebindClearsRetiredSlotBinding(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	h := seedHost(t, st, "host-reissue-slot")
	stream, _ := st.CreateStream(ctx, CreateStreamParams{HostID: h.ID, Title: "S"})
	slot, _ := st.CreateSlot(ctx, CreateSlotParams{HostID: h.ID, Kind: SlotCam, Idx: i64(1), SourceTokenHash: "src-reissue"})

	a, _ := st.CreatePass(ctx, CreatePassParams{StreamID: stream.ID, TokenHash: "a-tok"})
	b, _ := st.CreatePass(ctx, CreatePassParams{StreamID: stream.ID, TokenHash: "b-tok"})

	// Bind A to cam-1, revoke A (slot_id retained but A excluded from the active index), then
	// bind B to cam-1 — B becomes the active occupant and the displacement must scrub A's stale
	// slot_id even though A is retired.
	if err := st.AssignPassSlot(ctx, a.ID, slot.ID); err != nil {
		t.Fatalf("assign a: %v", err)
	}
	if err := st.SetPassStatus(ctx, a.ID, PassRevoked); err != nil {
		t.Fatalf("revoke a: %v", err)
	}
	if err := st.AssignPassSlot(ctx, b.ID, slot.ID); err != nil {
		t.Fatalf("assign b: %v", err)
	}
	if got, _ := st.GetPass(ctx, a.ID); got.SlotID != nil {
		t.Fatalf("re-binding the slot must scrub the retired row's stale slot_id, got %v", got.SlotID)
	}

	// Re-issuing A must therefore SUCCEED (no index collision with B) and leave B untouched.
	if err := st.ReissuePass(ctx, a.ID, "a-tok-2"); err != nil {
		t.Fatalf("re-issuing a previously-bound-then-revoked pass should not conflict: %v", err)
	}
	if got, _ := st.GetPass(ctx, b.ID); got.SlotID == nil || *got.SlotID != slot.ID {
		t.Fatalf("B's binding must be untouched, got %v", got.SlotID)
	}
}

// Session lifecycle (EN-2/D-20): a host goes live for one stream at a time. StartSession opens
// the active session; ActiveSession reports it; a second concurrent StartSession is rejected
// (one-live-per-host); EndActiveSession closes it and frees the host to go live again.
func TestSessionRepo_Lifecycle(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	h := seedHost(t, st, "host-session")
	other := seedHost(t, st, "host-session-other")
	x, _ := st.CreateStream(ctx, CreateStreamParams{HostID: h.ID, Title: "X"})
	y, _ := st.CreateStream(ctx, CreateStreamParams{HostID: h.ID, Title: "Y"})
	foreign, _ := st.CreateStream(ctx, CreateStreamParams{HostID: other.ID, Title: "F"})

	if _, err := st.ActiveSession(ctx, h.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no session yet: want ErrNotFound, got %v", err)
	}

	// StartSession refuses a stream that isn't the host's (RF-2), without leaking it.
	if _, err := st.StartSession(ctx, foreign.ID, h.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign stream: want ErrNotFound, got %v", err)
	}

	sess, err := st.StartSession(ctx, x.ID, h.ID)
	if err != nil {
		t.Fatalf("StartSession(x): %v", err)
	}
	if sess.Status != SessionActive || sess.StreamID != x.ID || sess.EndedAt != nil {
		t.Fatalf("started session = %+v, want active on x with no ended_at", sess)
	}
	got, err := st.ActiveSession(ctx, h.ID)
	if err != nil || got.StreamID != x.ID {
		t.Fatalf("ActiveSession = %+v / %v, want x", got, err)
	}

	// One live session per host: a second StartSession while one is active is rejected.
	if _, err := st.StartSession(ctx, y.ID, h.ID); !errors.Is(err, ErrSessionAlreadyLive) {
		t.Fatalf("second StartSession: want ErrSessionAlreadyLive, got %v", err)
	}

	// End frees the host; ActiveSession reports none and a fresh StartSession succeeds.
	if err := st.EndActiveSession(ctx, h.ID); err != nil {
		t.Fatalf("EndActiveSession: %v", err)
	}
	if _, err := st.ActiveSession(ctx, h.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after end: want ErrNotFound, got %v", err)
	}
	// End again is an idempotent no-op (nothing live).
	if err := st.EndActiveSession(ctx, h.ID); err != nil {
		t.Fatalf("EndActiveSession (idempotent): %v", err)
	}
	if _, err := st.StartSession(ctx, y.ID, h.ID); err != nil {
		t.Fatalf("StartSession(y) after end: %v", err)
	}
	if got, _ := st.ActiveSession(ctx, h.ID); got.StreamID != y.ID {
		t.Fatalf("active session now = %+v, want y", got)
	}
}

// BoundCamPassesForStream (the Go-live replay source) excludes passes past their expires_at
// DEADLINE, not just status-retired ones (codex): a guest can connect pre-live and cross its
// deadline before Go live (status still "sent"), and such a no-longer-joinable invite must not be
// replayed onto an OBS slot. Mirrors passJoinable.
func TestPassRepo_BoundCamPassesExcludesDeadlineExpired(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	h := seedHost(t, st, "host-bound-exp")
	stream, _ := st.CreateStream(ctx, CreateStreamParams{HostID: h.ID, Title: "S"})
	cam1, _ := st.CreateSlot(ctx, CreateSlotParams{HostID: h.ID, Kind: SlotCam, Idx: i64(1), SourceTokenHash: "src-be-1"})
	cam2, _ := st.CreateSlot(ctx, CreateSlotParams{HostID: h.ID, Kind: SlotCam, Idx: i64(2), SourceTokenHash: "src-be-2"})

	past := time.Now().Unix() - 3600
	expired, _ := st.CreatePass(ctx, CreatePassParams{StreamID: stream.ID, TokenHash: "exp", Status: PassSent, ExpiresAt: &past})
	live, _ := st.CreatePass(ctx, CreatePassParams{StreamID: stream.ID, TokenHash: "liv", Status: PassSent}) // no deadline
	if err := st.AssignPassSlot(ctx, expired.ID, cam1.ID); err != nil {
		t.Fatalf("assign expired: %v", err)
	}
	if err := st.AssignPassSlot(ctx, live.ID, cam2.ID); err != nil {
		t.Fatalf("assign live: %v", err)
	}

	bound, err := st.BoundCamPassesForStream(ctx, stream.ID)
	if err != nil {
		t.Fatalf("BoundCamPassesForStream: %v", err)
	}
	for _, b := range bound {
		if b.PassID == expired.ID {
			t.Fatalf("a deadline-expired pass (%s) must not be replayed onto %s", b.PassID, b.SlotLabel)
		}
	}
	var sawLive bool
	for _, b := range bound {
		if b.PassID == live.ID && b.SlotLabel == "cam-2" {
			sawLive = true
		}
	}
	if !sawLive {
		t.Fatalf("the still-joinable pass should be replayed onto cam-2, got %+v", bound)
	}
}

// MarkPassOpened is atomic exactly-once: it transitions only from created/sent, returns
// whether it did, never re-stamps opened_at, and never regresses a further-along pass.
func TestPassRepo_MarkPassOpenedIsAtomicOnce(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	h := seedHost(t, st, "host-open")
	stream, _ := st.CreateStream(ctx, CreateStreamParams{HostID: h.ID, Title: "Show"})
	p, _ := st.CreatePass(ctx, CreatePassParams{StreamID: stream.ID, TokenHash: "tok-open", Status: PassSent})

	ok, err := st.MarkPassOpened(ctx, p.ID)
	if err != nil || !ok {
		t.Fatalf("first MarkPassOpened: ok=%v err=%v, want true", ok, err)
	}
	first, _ := st.GetPass(ctx, p.ID)
	if first.Status != PassOpened || first.OpenedAt == nil {
		t.Fatalf("after open: status=%q opened_at=%v", first.Status, first.OpenedAt)
	}

	// A second call is a no-op (already opened): returns false, opened_at unchanged.
	ok, _ = st.MarkPassOpened(ctx, p.ID)
	second, _ := st.GetPass(ctx, p.ID)
	if ok || second.OpenedAt == nil || *second.OpenedAt != *first.OpenedAt {
		t.Fatalf("repeat MarkPassOpened should be a no-op: ok=%v openedAt %v→%v", ok, first.OpenedAt, second.OpenedAt)
	}

	// It must not regress a further-along (accepted) pass.
	_ = st.SetPassStatus(ctx, p.ID, PassAccepted)
	if ok, _ := st.MarkPassOpened(ctx, p.ID); ok {
		t.Fatal("MarkPassOpened must not transition an accepted pass")
	}
	if reload, _ := st.GetPass(ctx, p.ID); reload.Status != PassAccepted {
		t.Fatalf("accepted pass regressed to %q", reload.Status)
	}

	// A pass past its expiry deadline can't be opened, even from a sent state — the
	// deadline is enforced inside the atomic UPDATE (no read-then-write race).
	past := time.Now().Add(-time.Minute).Unix()
	exp, _ := st.CreatePass(ctx, CreatePassParams{StreamID: stream.ID, TokenHash: "tok-open-exp", Status: PassSent, ExpiresAt: &past})
	if ok, err := st.MarkPassOpened(ctx, exp.ID); err != nil || ok {
		t.Fatalf("MarkPassOpened on a past-deadline pass: ok=%v err=%v, want false", ok, err)
	}
	if reload, _ := st.GetPass(ctx, exp.ID); reload.Status != PassSent || reload.OpenedAt != nil {
		t.Fatalf("past-deadline pass must not be opened: status=%q openedAt=%v", reload.Status, reload.OpenedAt)
	}
}

func TestPassRepo_AssignSlotSameHostInvariant(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	hostA := seedHost(t, st, "host-A")
	hostB := seedHost(t, st, "host-B")
	streamA, _ := st.CreateStream(ctx, CreateStreamParams{HostID: hostA.ID, Title: "A"})
	slotA, _ := st.CreateSlot(ctx, CreateSlotParams{HostID: hostA.ID, Kind: SlotCam, Idx: i64(1), SourceTokenHash: "src-A"})
	slotB, _ := st.CreateSlot(ctx, CreateSlotParams{HostID: hostB.ID, Kind: SlotCam, Idx: i64(1), SourceTokenHash: "src-B"})

	passA, _ := st.CreatePass(ctx, CreatePassParams{StreamID: streamA.ID, TokenHash: "p-A"})

	// Cross-host assignment is refused (RF-2).
	if err := st.AssignPassSlot(ctx, passA.ID, slotB.ID); !errors.Is(err, ErrSlotHostMismatch) {
		t.Fatalf("cross-host assign = %v, want ErrSlotHostMismatch", err)
	}
	// Same-host assignment succeeds.
	if err := st.AssignPassSlot(ctx, passA.ID, slotA.ID); err != nil {
		t.Fatalf("same-host assign: %v", err)
	}
	reload, _ := st.GetPass(ctx, passA.ID)
	if reload.SlotID == nil || *reload.SlotID != slotA.ID {
		t.Errorf("slot not assigned: %+v", reload)
	}
}

func TestPassRepo_AssignSlotRejectsNonCam(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	h := seedHost(t, st, "host-noncam")
	stream, _ := st.CreateStream(ctx, CreateStreamParams{HostID: h.ID, Title: "S"})
	pass, _ := st.CreatePass(ctx, CreatePassParams{StreamID: stream.ID, TokenHash: "p-noncam"})

	screen, _ := st.CreateSlot(ctx, CreateSlotParams{HostID: h.ID, Kind: SlotScreenshare, SourceTokenHash: "src-screen"})
	hostSlot, _ := st.CreateSlot(ctx, CreateSlotParams{HostID: h.ID, Kind: SlotHost, SourceTokenHash: "src-host"})

	// Passes (guest occupants) bind only to cam slots (D-20); host/screenshare are refused.
	if err := st.AssignPassSlot(ctx, pass.ID, screen.ID); !errors.Is(err, ErrSlotNotCam) {
		t.Fatalf("assign screenshare slot = %v, want ErrSlotNotCam", err)
	}
	if err := st.AssignPassSlot(ctx, pass.ID, hostSlot.ID); !errors.Is(err, ErrSlotNotCam) {
		t.Fatalf("assign host slot = %v, want ErrSlotNotCam", err)
	}
	// The pass remains unbound.
	reload, _ := st.GetPass(ctx, pass.ID)
	if reload.SlotID != nil {
		t.Errorf("pass should be unbound after refused assigns, got %v", reload.SlotID)
	}
}

func TestSlotRepo_RecordTokenUse(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	h := seedHost(t, st, "host-tokuse")
	sl, _ := st.CreateSlot(ctx, CreateSlotParams{HostID: h.ID, Kind: SlotCam, Idx: i64(1), SourceTokenHash: "src-use"})
	if sl.SourceTokenLastUsedAt != nil || sl.SourceTokenLastSourceIP != nil {
		t.Fatalf("new slot should have nil last-used metadata: %+v", sl)
	}

	if err := st.RecordSlotTokenUse(ctx, sl.ID, "203.0.113.7"); err != nil {
		t.Fatalf("RecordSlotTokenUse: %v", err)
	}
	got, _ := st.GetSlot(ctx, sl.ID)
	if got.SourceTokenLastUsedAt == nil || *got.SourceTokenLastUsedAt == 0 {
		t.Errorf("last_used_at not recorded: %v", got.SourceTokenLastUsedAt)
	}
	if got.SourceTokenLastSourceIP == nil || *got.SourceTokenLastSourceIP != "203.0.113.7" {
		t.Errorf("last_source_ip = %v, want 203.0.113.7", got.SourceTokenLastSourceIP)
	}

	if err := st.RecordSlotTokenUse(ctx, "missing", "1.2.3.4"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RecordSlotTokenUse(missing) = %v, want ErrNotFound", err)
	}
}

func TestPassRepo_OneActiveOccupantPerSlot(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	h := seedHost(t, st, "host-occ")
	stream, _ := st.CreateStream(ctx, CreateStreamParams{HostID: h.ID, Title: "S"})
	slot, _ := st.CreateSlot(ctx, CreateSlotParams{HostID: h.ID, Kind: SlotCam, Idx: i64(1), SourceTokenHash: "src-occ"})

	p1, _ := st.CreatePass(ctx, CreatePassParams{StreamID: stream.ID, TokenHash: "occ-1"})
	p2, _ := st.CreatePass(ctx, CreatePassParams{StreamID: stream.ID, TokenHash: "occ-2"})
	if err := st.AssignPassSlot(ctx, p1.ID, slot.ID); err != nil {
		t.Fatalf("assign p1: %v", err)
	}
	// Assigning a second active pass to the same (stream, slot) DISPLACES the first (the DoD
	// "swap a slot occupant"), atomically — so at most one active occupant remains (RF-2) and the
	// partial unique index is never violated.
	if err := st.AssignPassSlot(ctx, p2.ID, slot.ID); err != nil {
		t.Fatalf("swap assign p2: %v", err)
	}
	if got, _ := st.GetPass(ctx, p2.ID); got.SlotID == nil || *got.SlotID != slot.ID {
		t.Fatalf("p2 not bound to the slot after swap: %v", got.SlotID)
	}
	if got, _ := st.GetPass(ctx, p1.ID); got.SlotID != nil {
		t.Fatalf("p1 not displaced by the swap: %v", got.SlotID)
	}
}

// TestHostSourceTokenUniquePerStreamRole exercises the (stream_id, role) unique index:
// a host/obs/obs_screen source token is one-active-value-per-role per stream (EN-5), so
// reissuing must replace, not append a second valid token. host_source_tokens has no
// repo in step 2 (D-18 routing lands in M2/M4), so this is a schema-level raw-SQL test.
func TestHostSourceTokenUniquePerStreamRole(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	h := seedHost(t, st, "host-hst")
	stream, _ := st.CreateStream(ctx, CreateStreamParams{HostID: h.ID, Title: "S"})

	insert := func(id, role, tokenHash string) error {
		_, err := st.writer.ExecContext(ctx,
			"INSERT INTO host_source_tokens (id, stream_id, role, token_hash) VALUES (?, ?, ?, ?)",
			id, stream.ID, role, tokenHash)
		return err
	}
	if err := insert("hst-1", "host", "hash-1"); err != nil {
		t.Fatalf("first host token: %v", err)
	}
	// A second token for the same (stream, role) must be rejected (one active value).
	if err := insert("hst-2", "host", "hash-2"); err == nil {
		t.Fatal("expected unique violation for a second host-source token on (stream, role), got nil")
	}
	// A different role on the same stream is fine.
	if err := insert("hst-3", "obs_screen", "hash-3"); err != nil {
		t.Fatalf("obs_screen token should be allowed: %v", err)
	}
}

// TestOneLiveSessionPerHost exercises the partial unique index idx_sessions_one_live
// (EN-2/RF-2) at the schema level: a host may have at most one active session.
func TestOneLiveSessionPerHost(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	h := seedHost(t, st, "host-session")
	stream, _ := st.CreateStream(ctx, CreateStreamParams{HostID: h.ID, Title: "S"})

	insertSession := func(id, status string) error {
		_, err := st.writer.ExecContext(ctx,
			"INSERT INTO sessions (id, stream_id, host_id, started_at, status) VALUES (?, ?, ?, ?, ?)",
			id, stream.ID, h.ID, 0, status)
		return err
	}
	if err := insertSession("sess-1", "active"); err != nil {
		t.Fatalf("first active session: %v", err)
	}
	if err := insertSession("sess-2", "active"); err == nil {
		t.Fatal("expected unique-index violation for a second active session, got nil")
	}
	// An ended session does not count against the live-session uniqueness.
	if err := insertSession("sess-3", "ended"); err != nil {
		t.Fatalf("ended session should be allowed: %v", err)
	}
}
