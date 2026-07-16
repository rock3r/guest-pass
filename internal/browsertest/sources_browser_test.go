//go:build browser

package browsertest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/store"
)

// slotByLabel finds a host's slot by kind + cam index (idx ignored for non-cam kinds).
func slotByLabel(t *testing.T, st *store.Store, hostID, kind string, idx int64) *store.Slot {
	t.Helper()
	slots, err := st.ListSlotsByHost(context.Background(), hostID)
	if err != nil {
		t.Fatalf("ListSlotsByHost: %v", err)
	}
	for _, sl := range slots {
		if sl.Kind != kind {
			continue
		}
		if kind != store.SlotCam || (sl.Idx != nil && *sl.Idx == idx) {
			return sl
		}
	}
	t.Fatalf("no slot kind=%s idx=%d", kind, idx)
	return nil
}

// T-4 / AC-4: the read-only Sources tab provisions the slot pool, shows per-slot cards
// (URL + occupant + on-air pill), and carries no editable controls (EN-26).
func TestHostApp_SourcesTab(t *testing.T) {
	s := seedHostApp(t)
	ctxBg := context.Background()
	stream, err := s.store.CreateStream(ctxBg, store.CreateStreamParams{HostID: s.hostID, Title: "Wired Show"})
	if err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	sources := s.base + "/app/streams/" + stream.ID + "/sources"

	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCkie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})

	Chrome(t, 60*time.Second, func(ctx context.Context) {
		// Opening the Sources tab provisions the pool and shows each slot's OBS URL (always
		// visible now — stored plaintext alongside the hash, not a one-time reveal).
		var cardCount int
		var slotURLs string
		if err := chromedp.Run(ctx,
			network.Enable(),
			setHostCookie,
			chromedp.Navigate(sources),
			chromedp.WaitVisible(`.slot-list`, chromedp.ByQuery),
			chromedp.WaitVisible(`.app-links a[aria-current="page"]`, chromedp.ByQuery),
			chromedp.Evaluate(`document.querySelectorAll('.slot-card').length`, &cardCount),
			chromedp.Evaluate(`[...document.querySelectorAll('.slot-card .issued-link')].map(i=>i.value).join('\n')`, &slotURLs),
		); err != nil {
			t.Fatalf("first sources open: %v", err)
		}
		if cardCount != 10 {
			t.Fatalf("slot cards = %d, want 10 (cam 1–8 + host + screenshare)", cardCount)
		}
		if !strings.Contains(slotURLs, "/s/cam-1?token=") || !strings.Contains(slotURLs, "/s/host?token=") || !strings.Contains(slotURLs, "/s/screen?token=") {
			t.Fatalf("slot cards did not show the cam-1 + host + screen OBS URLs; got:\n%s", slotURLs)
		}
		// The on-air pill is present (three-state; status-unavailable on the no-JS page).
		var onairCount int
		if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelectorAll('.slot-onair[data-onair]').length`, &onairCount)); err != nil {
			t.Fatalf("count on-air pills: %v", err)
		}
		if onairCount != 10 {
			t.Fatalf("on-air pills = %d, want 10", onairCount)
		}
		// EN-26: no editable production controls; the card links to the greenroom.
		var hasGreenroomLink, hasForbiddenControl bool
		if err := chromedp.Run(ctx,
			chromedp.Evaluate(`!!document.querySelector('.sources a[href="/greenroom"]')`, &hasGreenroomLink),
			chromedp.Evaluate(`!!document.querySelector('.sources [name="slot_id"], .sources [name="nameplate"], .sources [name="max_res"]')`, &hasForbiddenControl),
		); err != nil {
			t.Fatalf("EN-26 checks: %v", err)
		}
		if !hasGreenroomLink {
			t.Fatal("Sources tab does not link to the greenroom (EN-26)")
		}
		if hasForbiddenControl {
			t.Fatal("Sources tab exposes an editable production control (EN-26)")
		}
	})

	// Bind a guest to cam-1, then re-open: the occupant shows and the URLs are NOT re-revealed.
	cam1 := slotByLabel(t, s.store, s.hostID, store.SlotCam, 1)
	gname := "Sam Guest"
	pass, err := s.store.CreatePass(ctxBg, store.CreatePassParams{StreamID: stream.ID, Name: &gname, TokenHash: "occ-src", Status: store.PassSent})
	if err != nil {
		t.Fatalf("CreatePass: %v", err)
	}
	if err := s.store.AssignPassSlot(ctxBg, pass.ID, cam1.ID); err != nil {
		t.Fatalf("AssignPassSlot: %v", err)
	}

	Chrome(t, 60*time.Second, func(ctx context.Context) {
		var occText, reopenURLs string
		if err := chromedp.Run(ctx,
			network.Enable(),
			setHostCookie,
			chromedp.Navigate(sources),
			chromedp.WaitVisible(`.slot-list`, chromedp.ByQuery),
			chromedp.Text(`.slot-card .slot-occupant`, &occText, chromedp.ByQuery),
			chromedp.Evaluate(`[...document.querySelectorAll('.slot-card .issued-link')].map(i=>i.value).join('\n')`, &reopenURLs),
		); err != nil {
			t.Fatalf("re-open sources: %v", err)
		}
		if !strings.Contains(occText, "Sam Guest") {
			t.Fatalf("cam-1 occupant = %q, want Sam Guest", occText)
		}
		if !strings.Contains(reopenURLs, "/s/cam-1?token=") {
			t.Fatal("re-opening the Sources tab no longer shows the slot URL (URLs are always visible now)")
		}
	})
}
