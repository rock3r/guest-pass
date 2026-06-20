package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/store"
)

// suspendedHost / pendingHost create a host in the named state for the action tests.
func (a *apiHarness) hostInState(t *testing.T, sub, status string) *store.Host {
	t.Helper()
	h, err := a.store.CreateHost(context.Background(), store.CreateHostParams{
		GoogleSub: sub, Email: sub + "@example.com", Name: sub, Status: status,
	})
	if err != nil {
		t.Fatalf("CreateHost(%s): %v", status, err)
	}
	return h
}

func (a *apiHarness) hostStatus(t *testing.T, id string) string {
	t.Helper()
	h, err := a.store.GetHost(context.Background(), id)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	return h.Status
}

// Approve activates a pending host (D-28) and reinstates a suspended one.
func TestAdminActions_Approve(t *testing.T) {
	a := newAPIHarness(t)
	_, adminCookie := a.adminHost(t, "approve-admin")
	pending := a.hostInState(t, "pending-host", store.HostPending)

	rec := a.formPost(t, "/api/admin/hosts/"+pending.ID+"/approve", "", adminCookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("approve = %d, want 303", rec.Code)
	}
	if got := a.hostStatus(t, pending.ID); got != store.HostActive {
		t.Fatalf("after approve status = %q, want active", got)
	}

	// Reinstate a suspended host via the same action.
	susp := a.hostInState(t, "susp-host", store.HostSuspended)
	a.formPost(t, "/api/admin/hosts/"+susp.ID+"/approve", "", adminCookie)
	if got := a.hostStatus(t, susp.ID); got != store.HostActive {
		t.Fatalf("after reinstate status = %q, want active", got)
	}
}

// Suspend blocks future streams (status=suspended), and with end_live=1 force-ends the running
// session: the DB session is closed AND the in-memory room is torn down (the D-27 cascade, T-10).
func TestAdminActions_SuspendCascadeForceEndsLive(t *testing.T) {
	a := newAPIHarness(t)
	_, adminCookie := a.adminHost(t, "suspend-admin")
	hostB := a.hostInState(t, "live-host", store.HostActive)
	stream, err := a.store.CreateStream(context.Background(), store.CreateStreamParams{HostID: hostB.ID, Title: "Live Show"})
	if err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	if _, err := a.store.StartSession(context.Background(), stream.ID, hostB.ID); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	// Spawn the in-memory room (rooms are keyed by host id), so the cascade has something to tear down.
	a.hub.Room(hostB.ID)
	if a.hub.RoomIfLive(hostB.ID) == nil {
		t.Fatal("precondition: room should be live before suspend")
	}

	rec := a.formPost(t, "/api/admin/hosts/"+hostB.ID+"/suspend", "end_live=1", adminCookie)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "suspended-ended") {
		t.Fatalf("suspend+cascade = %d loc=%q, want 303 → suspended-ended", rec.Code, rec.Header().Get("Location"))
	}
	if got := a.hostStatus(t, hostB.ID); got != store.HostSuspended {
		t.Fatalf("status = %q, want suspended", got)
	}
	if _, err := a.store.ActiveSession(context.Background(), hostB.ID); err == nil {
		t.Fatal("the live session should be ended after the cascade")
	}
	if a.hub.RoomIfLive(hostB.ID) != nil {
		t.Fatal("the in-memory room should be torn down after the cascade")
	}
}

// Suspend WITHOUT the cascade leaves a running session alone (only future streams are blocked).
func TestAdminActions_SuspendWithoutCascadeKeepsSession(t *testing.T) {
	a := newAPIHarness(t)
	_, adminCookie := a.adminHost(t, "nocasc-admin")
	hostB := a.hostInState(t, "nocasc-host", store.HostActive)
	stream, _ := a.store.CreateStream(context.Background(), store.CreateStreamParams{HostID: hostB.ID, Title: "Show"})
	if _, err := a.store.StartSession(context.Background(), stream.ID, hostB.ID); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	rec := a.formPost(t, "/api/admin/hosts/"+hostB.ID+"/suspend", "", adminCookie)
	if loc := rec.Header().Get("Location"); loc != "/admin?msg=suspended" {
		t.Fatalf("suspend (no cascade) loc=%q, want exactly /admin?msg=suspended", loc)
	}
	if got := a.hostStatus(t, hostB.ID); got != store.HostSuspended {
		t.Fatalf("status = %q, want suspended", got)
	}
	if _, err := a.store.ActiveSession(context.Background(), hostB.ID); err != nil {
		t.Fatal("without the cascade the live session must remain active")
	}
}

// Promote/demote toggle the is_admin flag.
func TestAdminActions_PromoteDemote(t *testing.T) {
	a := newAPIHarness(t)
	_, adminCookie := a.adminHost(t, "role-admin")
	hostB := a.hostInState(t, "role-host", store.HostActive)

	a.formPost(t, "/api/admin/hosts/"+hostB.ID+"/promote", "", adminCookie)
	if h, _ := a.store.GetHost(context.Background(), hostB.ID); !h.IsAdmin {
		t.Fatal("promote should set is_admin")
	}
	a.formPost(t, "/api/admin/hosts/"+hostB.ID+"/demote", "", adminCookie)
	if h, _ := a.store.GetHost(context.Background(), hostB.ID); h.IsAdmin {
		t.Fatal("demote should clear is_admin")
	}
}

// An admin cannot suspend or demote their OWN account (no self-lockout); the account is unchanged.
func TestAdminActions_SelfGuards(t *testing.T) {
	a := newAPIHarness(t)
	admin, adminCookie := a.adminHost(t, "self-admin")

	rec := a.formPost(t, "/api/admin/hosts/"+admin.ID+"/suspend", "", adminCookie)
	if !strings.Contains(rec.Header().Get("Location"), "error=self") {
		t.Fatalf("self-suspend loc=%q, want error=self", rec.Header().Get("Location"))
	}
	if got := a.hostStatus(t, admin.ID); got != store.HostActive {
		t.Fatalf("self-suspend changed status to %q", got)
	}

	rec = a.formPost(t, "/api/admin/hosts/"+admin.ID+"/demote", "", adminCookie)
	if !strings.Contains(rec.Header().Get("Location"), "error=self") {
		t.Fatalf("self-demote loc=%q, want error=self", rec.Header().Get("Location"))
	}
	if h, _ := a.store.GetHost(context.Background(), admin.ID); !h.IsAdmin {
		t.Fatal("self-demote should not have cleared is_admin")
	}
}

// Authority: only an is_admin host may invoke the actions; a regular host is 403, anon is 401.
func TestAdminActions_Authority(t *testing.T) {
	a := newAPIHarness(t)
	_, hostCookie := a.host(t, "plain")
	target := a.hostInState(t, "target", store.HostActive)
	for _, action := range []string{"approve", "suspend", "promote", "demote"} {
		path := "/api/admin/hosts/" + target.ID + "/" + action
		if rec := a.formPost(t, path, "", hostCookie); rec.Code != http.StatusForbidden {
			t.Fatalf("non-admin POST %s = %d, want 403", path, rec.Code)
		}
		if rec := a.formPost(t, path, "", nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("anon POST %s = %d, want 401", path, rec.Code)
		}
	}
	// The target was never modified by the rejected calls.
	if got := a.hostStatus(t, target.ID); got != store.HostActive {
		t.Fatalf("target status changed under rejected calls: %q", got)
	}
}

// The last-admin lockout guard (D-M5.5-5 / AC-9): demote/suspend is refused while the target is
// the only active admin (the instance always retains ≥1 active admin), and allowed once a second
// active admin exists. Driven at the handler level (the acting host injected via context) because
// through the router the acting admin is always counted, so the guard's refusal can't be reached.
func TestAdminActions_LastAdminGuard(t *testing.T) {
	a := newAPIHarness(t)
	admin := &adminServer{store: a.store, hub: a.hub, binds: newBindingLocks()}

	// The sole active admin in the instance is the target.
	target := a.hostInState(t, "lone-admin", store.HostActive)
	if err := a.store.SetHostAdmin(context.Background(), target.ID, true); err != nil {
		t.Fatalf("SetHostAdmin(target): %v", err)
	}
	// A distinct acting identity (so the self-guard doesn't fire). The guard is the instance
	// invariant, independent of who acts, so the acting host need not itself be a counted admin.
	acting := a.hostInState(t, "acting", store.HostActive)

	call := func(action string) *httptest.ResponseRecorder {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", target.ID)
		ctx := context.WithValue(context.Background(), chi.RouteCtxKey, rctx)
		ctx = auth.ContextWithHost(ctx, acting)
		r := httptest.NewRequest(http.MethodPost, "/admin/x", nil).WithContext(ctx)
		w := httptest.NewRecorder()
		switch action {
		case "suspend":
			admin.suspendHost(w, r)
		case "demote":
			admin.demoteHost(w, r)
		}
		return w
	}

	for _, action := range []string{"suspend", "demote"} {
		rec := call(action)
		if !strings.Contains(rec.Header().Get("Location"), "error=last-admin") {
			t.Fatalf("%s of last admin: loc=%q, want error=last-admin", action, rec.Header().Get("Location"))
		}
	}
	// The refused actions left the target untouched.
	if h, _ := a.store.GetHost(context.Background(), target.ID); h.Status != store.HostActive || !h.IsAdmin {
		t.Fatalf("refused actions changed the target: status=%q admin=%v", h.Status, h.IsAdmin)
	}

	// With a second active admin, the (now non-last) admin can be demoted.
	other := a.hostInState(t, "other-admin", store.HostActive)
	if err := a.store.SetHostAdmin(context.Background(), other.ID, true); err != nil {
		t.Fatalf("SetHostAdmin(other): %v", err)
	}
	rec := call("demote")
	if !strings.Contains(rec.Header().Get("Location"), "msg=demoted") {
		t.Fatalf("demote with a second admin present: loc=%q, want msg=demoted", rec.Header().Get("Location"))
	}
	if h, _ := a.store.GetHost(context.Background(), target.ID); h.IsAdmin {
		t.Fatal("demote should have cleared is_admin once a second active admin existed")
	}
}

// A missing target host redirects with ?error=notfound rather than 500ing.
func TestAdminActions_TargetNotFound(t *testing.T) {
	a := newAPIHarness(t)
	_, adminCookie := a.adminHost(t, "nf-admin")
	rec := a.formPost(t, "/api/admin/hosts/does-not-exist/approve", "", adminCookie)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "error=notfound") {
		t.Fatalf("missing target = %d loc=%q, want 303 → error=notfound", rec.Code, rec.Header().Get("Location"))
	}
}
