package store

import (
	"context"
	"errors"
	"testing"
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
	if _, err := st.CreateSlot(ctx, CreateSlotParams{HostID: h.ID, Kind: SlotCam, SourceTokenHash: "dup"}); err != nil {
		t.Fatalf("first slot: %v", err)
	}
	if _, err := st.CreateSlot(ctx, CreateSlotParams{HostID: h.ID, Kind: SlotCam, SourceTokenHash: "dup"}); err == nil {
		t.Fatal("expected UNIQUE violation on duplicate source_token_hash, got nil")
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

func TestPassRepo_AssignSlotSameHostInvariant(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	hostA := seedHost(t, st, "host-A")
	hostB := seedHost(t, st, "host-B")
	streamA, _ := st.CreateStream(ctx, CreateStreamParams{HostID: hostA.ID, Title: "A"})
	slotA, _ := st.CreateSlot(ctx, CreateSlotParams{HostID: hostA.ID, Kind: SlotCam, SourceTokenHash: "src-A"})
	slotB, _ := st.CreateSlot(ctx, CreateSlotParams{HostID: hostB.ID, Kind: SlotCam, SourceTokenHash: "src-B"})

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
	sl, _ := st.CreateSlot(ctx, CreateSlotParams{HostID: h.ID, Kind: SlotCam, SourceTokenHash: "src-use"})
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
	slot, _ := st.CreateSlot(ctx, CreateSlotParams{HostID: h.ID, Kind: SlotCam, SourceTokenHash: "src-occ"})

	p1, _ := st.CreatePass(ctx, CreatePassParams{StreamID: stream.ID, TokenHash: "occ-1"})
	p2, _ := st.CreatePass(ctx, CreatePassParams{StreamID: stream.ID, TokenHash: "occ-2"})
	if err := st.AssignPassSlot(ctx, p1.ID, slot.ID); err != nil {
		t.Fatalf("assign p1: %v", err)
	}
	// A second active pass in the same (stream, slot) violates the partial unique index.
	if err := st.AssignPassSlot(ctx, p2.ID, slot.ID); err == nil {
		t.Fatal("expected unique-index violation for second active occupant, got nil")
	}
	// Revoking p1 frees the slot for p2 (index excludes revoked/expired).
	if err := st.SetPassStatus(ctx, p1.ID, PassRevoked); err != nil {
		t.Fatalf("revoke p1: %v", err)
	}
	if err := st.AssignPassSlot(ctx, p2.ID, slot.ID); err != nil {
		t.Fatalf("assign p2 after revoke: %v", err)
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
