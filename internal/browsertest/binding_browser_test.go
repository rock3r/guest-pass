//go:build browser

package browsertest

import (
	"context"
	"fmt"
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
