package store

import (
	"context"
	"errors"
	"testing"
)

func TestHostRepo_CreateGetUpdate(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	pic := "https://example.com/p.png"
	h, err := st.CreateHost(ctx, CreateHostParams{
		GoogleSub: "google-sub-1",
		Email:     "host@example.com",
		Name:      "Host One",
		Picture:   &pic,
	})
	if err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	if h.ID == "" {
		t.Fatal("CreateHost returned empty id")
	}
	if h.Status != HostPending {
		t.Errorf("default status = %q, want %q", h.Status, HostPending)
	}

	got, err := st.GetHost(ctx, h.ID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if got.GoogleSub != "google-sub-1" || got.Email != "host@example.com" || got.Name != "Host One" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.Picture == nil || *got.Picture != pic {
		t.Errorf("picture = %v, want %q", got.Picture, pic)
	}
	if got.IsAdmin {
		t.Error("IsAdmin = true, want false")
	}

	bySub, err := st.GetHostByGoogleSub(ctx, "google-sub-1")
	if err != nil || bySub.ID != h.ID {
		t.Fatalf("GetHostByGoogleSub: %v / %v", bySub, err)
	}

	// Live status + admin changes (EN-6 reads these on each request).
	if err := st.SetHostStatus(ctx, h.ID, HostActive); err != nil {
		t.Fatalf("SetHostStatus: %v", err)
	}
	if err := st.SetHostAdmin(ctx, h.ID, true); err != nil {
		t.Fatalf("SetHostAdmin: %v", err)
	}
	got, _ = st.GetHost(ctx, h.ID)
	if got.Status != HostActive || !got.IsAdmin {
		t.Errorf("after update: status=%q isAdmin=%v", got.Status, got.IsAdmin)
	}
}

func TestHostRepo_NotFound(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	if _, err := st.GetHost(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetHost(missing) = %v, want ErrNotFound", err)
	}
	if _, err := st.GetHostByGoogleSub(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetHostByGoogleSub(missing) = %v, want ErrNotFound", err)
	}
	if err := st.SetHostStatus(ctx, "missing", HostActive); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetHostStatus(missing) = %v, want ErrNotFound", err)
	}
}

func TestHostRepo_UniqueGoogleSub(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	if _, err := st.CreateHost(ctx, CreateHostParams{GoogleSub: "dup", Email: "a@x.com", Name: "A"}); err != nil {
		t.Fatalf("first CreateHost: %v", err)
	}
	if _, err := st.CreateHost(ctx, CreateHostParams{GoogleSub: "dup", Email: "b@x.com", Name: "B"}); err == nil {
		t.Fatal("expected UNIQUE violation on duplicate google_sub, got nil")
	}
}
