//go:build browser

package browsertest

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"github.com/rock3r/guest-pass/internal/auth"
)

// T-6 / AC-6 — the DoD core, driven through the greenroom People controls (extends the M2
// rebind tracer). Two guests publish; the host opens the greenroom and uses the host-only
// slot picker to bind cam-1 to guest A — the OBS source renders A — then reassigns the slot
// LIVE to guest B via the picker; the OBS source re-routes to B's distinct stream with NO
// page reload and no OBS edit (the picker PUTs /api/passes/{id}/slot → persist + slot-rebind,
// EN-3). The reducer-level boundSlot projection + epoch invariants are in the Go tests.
func TestBinding_GreenroomPickerReroutesSource(t *testing.T) {
	s := seedDeviceCheck(t)
	// The host is live for this stream so the picker's live reroute is in-scope (EN-2/D-20);
	// without an active session the (re)bind would persist DB-only and never reach the source.
	if _, err := s.store.StartSession(context.Background(), s.streamID, s.hostID); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelAlloc()

	// Each peer is its OWN headless Chrome (own fake camera); they connect P2P over loopback.
	rootCtx, cancelRoot := chromedp.NewContext(allocCtx)
	defer cancelRoot()
	rootCtx, cancelDeadline := context.WithTimeout(rootCtx, 240*time.Second)
	defer cancelDeadline()
	guestACtx := rootCtx
	guestBCtx, cancelB := chromedp.NewContext(rootCtx)
	defer cancelB()
	obsCtx, cancelOBS := chromedp.NewContext(rootCtx)
	defer cancelOBS()
	hostCtx, cancelHost := chromedp.NewContext(rootCtx)
	defer cancelHost()

	publishGuest(t, guestACtx, s.base, s.rawToken, "A")
	publishGuest(t, guestBCtx, s.base, s.rawTokenB, "B")

	// OBS cam source page: subscribed to cam-1, no occupant bound yet.
	if err := chromedp.Run(obsCtx,
		chromedp.Navigate(s.base+"/s/"+s.slotLabel+"?token="+s.srcToken),
		chromedp.WaitVisible(`#obs-video`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("obs source page did not load: %v", err)
	}

	// Host greenroom (host cookie): wait until it is live and both guest tiles (with their slot
	// pickers) have rendered over P2P.
	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCookie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})
	if err := chromedp.Run(hostCtx,
		network.Enable(),
		setHostCookie,
		chromedp.Navigate(s.base+"/greenroom"),
		chromedp.WaitVisible(`.greenroom[data-state="live"]`, chromedp.ByQuery),
		chromedp.WaitVisible(fmt.Sprintf(`.gr-tile[data-guest=%q] .gr-slot`, s.passID), chromedp.ByQuery),
		chromedp.WaitVisible(fmt.Sprintf(`.gr-tile[data-guest=%q] .gr-slot`, s.passIDB), chromedp.ByQuery),
	); err != nil {
		t.Fatalf("greenroom did not render both guest slot pickers: %v", err)
	}

	// bindViaPicker sets a guest tile's slot picker and fires change (the host action) — which
	// PUTs /api/passes/{id}/slot from the greenroom page over its host cookie.
	bindViaPicker := func(passID, slot string) {
		t.Helper()
		var ok bool
		expr := fmt.Sprintf(`(() => {
			const sel = document.querySelector('.gr-tile[data-guest=%q] .gr-slot');
			if (!sel) return false;
			sel.value = %q;
			sel.dispatchEvent(new Event('change', { bubbles: true }));
			return true;
		})()`, passID, slot)
		if err := chromedp.Run(hostCtx, chromedp.Evaluate(expr, &ok)); err != nil {
			t.Fatalf("bind %s→%s via picker: %v", passID, slot, err)
		}
		if !ok {
			t.Fatalf("slot picker for %s not found", passID)
		}
	}

	// Bind cam-1 → guest A via the picker; the OBS source renders A. Stamp the OBS document so a
	// later check proves no reload happened.
	bindViaPicker(s.passID, s.slotLabel)
	var idA string
	if err := chromedp.Run(obsCtx,
		chromedp.Poll(`!!document.querySelector('#obs-video') && document.querySelector('#obs-video').videoWidth > 0`,
			nil, chromedp.WithPollingTimeout(60*time.Second)),
		chromedp.Evaluate(`document.querySelector('#obs-video').srcObject.id`, &idA),
		chromedp.Evaluate(`(window.__gpObsLoadMark = "m1")`, nil),
	); err != nil {
		t.Fatalf("obs source did not render guest A after the picker bind: %v", err)
	}
	if idA == "" {
		t.Fatal("guest A stream id empty — cannot prove a re-route")
	}

	// Reassign cam-1 → guest B LIVE via the picker; the OBS source re-routes to B's DISTINCT
	// stream (the synthetic frames are identical, so the MediaStream id is the witness).
	bindViaPicker(s.passIDB, s.slotLabel)
	rerouted := fmt.Sprintf(`(() => {
		const v = document.querySelector('#obs-video');
		return !!v && v.videoWidth > 0 && v.srcObject && v.srcObject.id !== %q;
	})()`, idA)
	if err := chromedp.Run(obsCtx,
		chromedp.Poll(rerouted, nil, chromedp.WithPollingTimeout(60*time.Second)),
	); err != nil {
		t.Fatalf("OBS source did not re-route to guest B after the picker reassign: %v", err)
	}

	// The re-route happened with NO page reload (the one-shot stamp survives).
	var sameDoc bool
	if err := chromedp.Run(obsCtx, chromedp.Evaluate(`window.__gpObsLoadMark === "m1"`, &sameDoc)); err != nil {
		t.Fatalf("read OBS document stamp: %v", err)
	}
	if !sameDoc {
		t.Fatal("OBS source page reloaded across the rebind — AC-6 requires re-route with NO reload/OBS edit")
	}
}

// A DB-only (pre-live) picker selection must STICK across re-renders, not snap back (codex P2):
// with no active session the bind persists but produces no roster frame (boundSlot is
// live-occupancy derived), so the controlled <select> would reset to "" on the next grid
// re-render unless the optimistic override holds the host's choice until Go live replays it.
func TestBinding_PreLivePickerKeepsSelection(t *testing.T) {
	s := seedDeviceCheck(t) // NO StartSession → the bind is DB-only (pre-live)

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelAlloc()
	rootCtx, cancelRoot := chromedp.NewContext(allocCtx)
	defer cancelRoot()
	rootCtx, cancelDeadline := context.WithTimeout(rootCtx, 200*time.Second)
	defer cancelDeadline()
	guestACtx := rootCtx
	guestBCtx, cancelB := chromedp.NewContext(rootCtx)
	defer cancelB()
	hostCtx, cancelHost := chromedp.NewContext(rootCtx)
	defer cancelHost()

	publishGuest(t, guestACtx, s.base, s.rawToken, "A")

	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCookie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})
	if err := chromedp.Run(hostCtx,
		network.Enable(),
		setHostCookie,
		chromedp.Navigate(s.base+"/greenroom"),
		chromedp.WaitVisible(`.greenroom[data-state="live"]`, chromedp.ByQuery),
		chromedp.WaitVisible(fmt.Sprintf(`.gr-tile[data-guest=%q] .gr-slot`, s.passID), chromedp.ByQuery),
	); err != nil {
		t.Fatalf("greenroom did not render guest A's slot picker: %v", err)
	}

	// Pick cam-1 for A pre-live: the PUT persists DB-only (no session → no reroute, no roster frame).
	var ok bool
	expr := fmt.Sprintf(`(() => {
		const sel = document.querySelector('.gr-tile[data-guest=%q] .gr-slot');
		if (!sel) return false;
		sel.value = "cam-1";
		sel.dispatchEvent(new Event('change', { bubbles: true }));
		return true;
	})()`, s.passID)
	if err := chromedp.Run(hostCtx, chromedp.Evaluate(expr, &ok)); err != nil || !ok {
		t.Fatalf("set A's picker to cam-1: ok=%v err=%v", ok, err)
	}

	// Guest B joins → the grid re-renders, which RECONCILES A's controlled <select>. Without the
	// override that reconciliation resets A's picker to "" (entry.boundSlot is still empty pre-live);
	// with it, A stays on cam-1. Waiting for B's picker proves the re-render happened.
	publishGuest(t, guestBCtx, s.base, s.rawTokenB, "B")
	if err := chromedp.Run(hostCtx,
		chromedp.WaitVisible(fmt.Sprintf(`.gr-tile[data-guest=%q] .gr-slot`, s.passIDB), chromedp.ByQuery),
	); err != nil {
		t.Fatalf("guest B did not render (no grid re-render to reconcile against): %v", err)
	}

	var aVal string
	if err := chromedp.Run(hostCtx,
		chromedp.Evaluate(fmt.Sprintf(`document.querySelector('.gr-tile[data-guest=%q] .gr-slot').value`, s.passID), &aVal),
	); err != nil {
		t.Fatalf("read A's picker value: %v", err)
	}
	if aVal != "cam-1" {
		t.Fatalf("A's pre-live picker selection reverted to %q after a re-render — must stay cam-1", aVal)
	}
}

// A LIVE bind must NOT leave a stale local override that masks a later unbind from elsewhere
// (codex P2): a live bind is reflected by the authoritative roster, so the picker keeps no
// override for it — when another tab/actor unassigns the slot, the roster's boundSlot:"" must move
// the picker back to Unassigned. (Pre-live DB-only binds still keep an override; that's a separate
// path, covered by TestBinding_PreLivePickerKeepsSelection.)
func TestBinding_LiveUnbindElsewhereClearsPicker(t *testing.T) {
	s := seedDeviceCheck(t)
	if _, err := s.store.StartSession(context.Background(), s.streamID, s.hostID); err != nil {
		t.Fatalf("StartSession: %v", err) // LIVE → the picker bind does a live reroute (response live:true)
	}

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelAlloc()
	rootCtx, cancelRoot := chromedp.NewContext(allocCtx)
	defer cancelRoot()
	rootCtx, cancelDeadline := context.WithTimeout(rootCtx, 180*time.Second)
	defer cancelDeadline()
	guestCtx := rootCtx
	hostCtx, cancelHost := chromedp.NewContext(rootCtx)
	defer cancelHost()

	publishGuest(t, guestCtx, s.base, s.rawToken, "A")

	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCookie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})
	if err := chromedp.Run(hostCtx,
		network.Enable(),
		setHostCookie,
		chromedp.Navigate(s.base+"/greenroom"),
		chromedp.WaitVisible(`.greenroom[data-state="live"]`, chromedp.ByQuery),
		chromedp.WaitVisible(fmt.Sprintf(`.gr-tile[data-guest=%q] .gr-slot`, s.passID), chromedp.ByQuery),
	); err != nil {
		t.Fatalf("greenroom did not render the guest slot picker: %v", err)
	}

	// Bind A → cam-1 via the picker (live reroute), and wait until the roster moves the picker to it.
	var ok bool
	expr := fmt.Sprintf(`(() => {
		const sel = document.querySelector('.gr-tile[data-guest=%q] .gr-slot');
		if (!sel) return false;
		sel.value = "cam-1";
		sel.dispatchEvent(new Event('change', { bubbles: true }));
		return true;
	})()`, s.passID)
	if err := chromedp.Run(hostCtx, chromedp.Evaluate(expr, &ok)); err != nil || !ok {
		t.Fatalf("bind cam-1 via picker: ok=%v err=%v", ok, err)
	}
	onCam1 := fmt.Sprintf(`document.querySelector('.gr-tile[data-guest=%q] .gr-slot').value === "cam-1"`, s.passID)
	if err := chromedp.Run(hostCtx, chromedp.Poll(onCam1, nil, chromedp.WithPollingTimeout(30*time.Second))); err != nil {
		t.Fatalf("picker did not reflect the live bind to cam-1: %v", err)
	}

	// Unassign A from ELSEWHERE (another tab/actor): an out-of-band PUT carrying the host cookie.
	// The live unbind broadcasts a roster with boundSlot:"" — which the picker must honor.
	req, _ := http.NewRequest(http.MethodPut, s.base+"/api/passes/"+s.passID+"/slot", strings.NewReader(`{"slot":""}`))
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: s.hostCookie})
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("out-of-band unassign: %v", err)
	}
	_ = resp.Body.Close()

	// The picker must move back to Unassigned — a stale override must not keep showing cam-1.
	unassigned := fmt.Sprintf(`document.querySelector('.gr-tile[data-guest=%q] .gr-slot').value === ""`, s.passID)
	if err := chromedp.Run(hostCtx, chromedp.Poll(unassigned, nil, chromedp.WithPollingTimeout(30*time.Second))); err != nil {
		t.Fatalf("picker kept showing cam-1 after a live unbind elsewhere — stale override masked the roster: %v", err)
	}
}

// A pre-live (DB-only) picker override must clear when a DIFFERENT peer is later bound into that
// slot (codex P2): otherwise the host keeps seeing the displaced guest on a slot that now belongs
// to someone else. Pre-live bind A→cam-1 (override) → out-of-band rebind cam-1 to B (displaces A)
// → Go live replays B onto cam-1 → A's picker must drop back to Unassigned, not stay on cam-1.
func TestBinding_PreLiveOverrideClearedOnDisplacement(t *testing.T) {
	s := seedDeviceCheck(t) // NO session yet → the first bind is DB-only (override)

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelAlloc()
	rootCtx, cancelRoot := chromedp.NewContext(allocCtx)
	defer cancelRoot()
	rootCtx, cancelDeadline := context.WithTimeout(rootCtx, 200*time.Second)
	defer cancelDeadline()
	guestACtx := rootCtx
	guestBCtx, cancelB := chromedp.NewContext(rootCtx)
	defer cancelB()
	hostCtx, cancelHost := chromedp.NewContext(rootCtx)
	defer cancelHost()

	publishGuest(t, guestACtx, s.base, s.rawToken, "A")
	publishGuest(t, guestBCtx, s.base, s.rawTokenB, "B")

	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCookie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})
	if err := chromedp.Run(hostCtx,
		network.Enable(),
		setHostCookie,
		chromedp.Navigate(s.base+"/greenroom"),
		chromedp.WaitVisible(`.greenroom[data-state="live"]`, chromedp.ByQuery),
		chromedp.WaitVisible(fmt.Sprintf(`.gr-tile[data-guest=%q] .gr-slot`, s.passID), chromedp.ByQuery),
		chromedp.WaitVisible(fmt.Sprintf(`.gr-tile[data-guest=%q] .gr-slot`, s.passIDB), chromedp.ByQuery),
	); err != nil {
		t.Fatalf("greenroom did not render both guest pickers: %v", err)
	}

	// Pre-live bind A → cam-1 via the picker (DB-only → local override); it sticks to cam-1.
	var ok bool
	if err := chromedp.Run(hostCtx, chromedp.Evaluate(fmt.Sprintf(`(() => {
		const sel = document.querySelector('.gr-tile[data-guest=%q] .gr-slot');
		if (!sel) return false; sel.value = "cam-1"; sel.dispatchEvent(new Event('change',{bubbles:true})); return true;
	})()`, s.passID), &ok)); err != nil || !ok {
		t.Fatalf("bind A→cam-1: ok=%v err=%v", ok, err)
	}
	aCam1 := fmt.Sprintf(`document.querySelector('.gr-tile[data-guest=%q] .gr-slot').value === "cam-1"`, s.passID)
	if err := chromedp.Run(hostCtx, chromedp.Poll(aCam1, nil, chromedp.WithPollingTimeout(20*time.Second))); err != nil {
		t.Fatalf("A's pre-live override did not stick to cam-1: %v", err)
	}

	// Out-of-band, rebind cam-1 to B (displaces A in the DB; A's override lingers locally), then go
	// live — the replay routes cam-1 to B, and the roster (B on cam-1) must clear A's stale override.
	host := &http.Client{}
	bindB, _ := http.NewRequest(http.MethodPut, s.base+"/api/passes/"+s.passIDB+"/slot", strings.NewReader(`{"slot":"cam-1"}`))
	bindB.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: s.hostCookie})
	bindB.Header.Set("Content-Type", "application/json")
	if resp, err := host.Do(bindB); err != nil {
		t.Fatalf("out-of-band bind B: %v", err)
	} else {
		_ = resp.Body.Close()
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	goLive, _ := http.NewRequest(http.MethodPost, s.base+"/app/streams/"+s.streamID+"/session/start", nil)
	goLive.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: s.hostCookie})
	if resp, err := noRedirect.Do(goLive); err != nil {
		t.Fatalf("go-live: %v", err)
	} else {
		_ = resp.Body.Close()
	}

	// A's picker must drop to Unassigned (its override cleared by the displacement), not stay cam-1.
	aUnassigned := fmt.Sprintf(`document.querySelector('.gr-tile[data-guest=%q] .gr-slot').value === ""`, s.passID)
	if err := chromedp.Run(hostCtx, chromedp.Poll(aUnassigned, nil, chromedp.WithPollingTimeout(30*time.Second))); err != nil {
		t.Fatalf("A's stale override survived B being bound into cam-1: %v", err)
	}
}

// A failed (re)bind must surface to the host, not be swallowed (codex, M4 PR-6). The greenroom
// picker PUTs to a cam slot the host has not provisioned (only cam-1 is wired in the seed), so
// the server answers 404 with no roster update — the controlled picker stays put AND the
// .gr-binderr alert tells the host why (open the Sources tab first). Without surfacing, the
// host sees a silent no-op and can't tell the bind failed.
func TestBinding_FailedBindSurfacesError(t *testing.T) {
	s := seedDeviceCheck(t)

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelAlloc()

	rootCtx, cancelRoot := chromedp.NewContext(allocCtx)
	defer cancelRoot()
	rootCtx, cancelDeadline := context.WithTimeout(rootCtx, 180*time.Second)
	defer cancelDeadline()
	guestCtx := rootCtx
	hostCtx, cancelHost := chromedp.NewContext(rootCtx)
	defer cancelHost()

	publishGuest(t, guestCtx, s.base, s.rawToken, "A")

	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCookie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})
	if err := chromedp.Run(hostCtx,
		network.Enable(),
		setHostCookie,
		chromedp.Navigate(s.base+"/greenroom"),
		chromedp.WaitVisible(`.greenroom[data-state="live"]`, chromedp.ByQuery),
		chromedp.WaitVisible(fmt.Sprintf(`.gr-tile[data-guest=%q] .gr-slot`, s.passID), chromedp.ByQuery),
	); err != nil {
		t.Fatalf("greenroom did not render the guest slot picker: %v", err)
	}

	// Bind to cam-2, which the seed never provisioned → 404.
	var ok bool
	expr := fmt.Sprintf(`(() => {
		const sel = document.querySelector('.gr-tile[data-guest=%q] .gr-slot');
		if (!sel) return false;
		sel.value = "cam-2";
		sel.dispatchEvent(new Event('change', { bubbles: true }));
		return true;
	})()`, s.passID)
	if err := chromedp.Run(hostCtx, chromedp.Evaluate(expr, &ok)); err != nil {
		t.Fatalf("bind to unprovisioned cam-2 via picker: %v", err)
	}
	if !ok {
		t.Fatal("slot picker not found")
	}

	// The host must see the error alert (server 404 message is surfaced, not swallowed).
	var msg string
	if err := chromedp.Run(hostCtx,
		chromedp.WaitVisible(`.gr-binderr`, chromedp.ByQuery),
		chromedp.Text(`.gr-binderr`, &msg, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("a failed bind did not surface an error to the host: %v", err)
	}
	if msg == "" {
		t.Fatal("the bind-error alert rendered empty — the host has no idea why the bind failed")
	}

	// The picker must NOT have stuck on cam-2 (no roster update happened); it snaps back.
	var pickerVal string
	if err := chromedp.Run(hostCtx,
		chromedp.Evaluate(fmt.Sprintf(`document.querySelector('.gr-tile[data-guest=%q] .gr-slot').value`, s.passID), &pickerVal),
	); err != nil {
		t.Fatalf("read picker value: %v", err)
	}
	if pickerVal == "cam-2" {
		t.Fatal("the picker stuck on the rejected slot — a failed bind must not appear to succeed")
	}
}
