//go:build browser

package browsertest

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/signaling"
)

// putHostJSON issues an authenticated host PUT (the greenroom People controls run same-origin with
// the session cookie). Fails unless the response is 200.
func putHostJSON(t *testing.T, s *devSeed, path, body string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, s.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: s.hostCookie})
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT %s = %d, want 200", path, resp.StatusCode)
	}
}

// nameJSON builds a {"name":…} request body, JSON-escaping the value (so a hostile name with quotes
// or backslashes is transmitted faithfully).
func nameJSON(name string) string {
	b, _ := json.Marshal(map[string]string{"name": name})
	return string(b)
}

// T-7 / AC-7 (D-16/EN-15): the OBS nameplate. With the source URL's ?name show/hide param ON, the
// bound occupant's display name renders into #obs-nameplate. A host override (PUT
// /api/passes/{id}/name) refreshes the nameplate LIVE at the same epoch — the media link is NOT
// re-created (same MediaStream, no video flicker). A hostile name renders inert as ESCAPED
// textContent (no injected element, no script execution) and is length-capped server-side.
func TestNameplate_ShowOverrideRefreshAndEscaping(t *testing.T) {
	s := seedDeviceCheck(t)
	ctx := context.Background()
	// The host is live for this stream, so the People-control name override reroutes the live room
	// (EN-2/D-20) — the same gate the slot picker uses.
	if _, err := s.store.StartSession(ctx, s.streamID, s.hostID); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelAlloc()
	guestCtx, cancelGuest := chromedp.NewContext(allocCtx)
	defer cancelGuest()
	guestCtx, cancelGuestT := context.WithTimeout(guestCtx, 180*time.Second)
	defer cancelGuestT()
	obsCtx, cancelOBS := chromedp.NewContext(guestCtx)
	defer cancelOBS()

	// Guest (Dana) publishes.
	if err := chromedp.Run(guestCtx,
		chromedp.Navigate(s.base+"/p/"+s.rawToken),
		chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
		chromedp.Click(`.dc-start`, chromedp.ByQuery),
		chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.dc-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(30*time.Second)),
		chromedp.Click(`.dc-enter`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-entered="1"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("guest publish flow: %v", err)
	}

	// OBS source with the nameplate SHOWN (?name=1).
	injectRecorder := chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(wsRecorderJS).Do(ctx)
		return err
	})
	if err := chromedp.Run(obsCtx,
		injectRecorder,
		chromedp.Navigate(s.base+"/s/"+s.slotLabel+"?token="+s.srcToken+"&name=1"),
		chromedp.WaitVisible(`#obs-video`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("obs source page did not load: %v", err)
	}

	// Bind cam-1 to the guest via the REST People control; the source renders Dana's camera.
	putHostJSON(t, s, "/api/passes/"+s.passID+"/slot", `{"slot":"cam-1"}`)
	if err := chromedp.Run(obsCtx,
		chromedp.Poll(`document.querySelector('#obs-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(60*time.Second)),
		// The nameplate shows the bound occupant's name as textContent.
		chromedp.Poll(`document.querySelector('#obs-nameplate') && document.querySelector('#obs-nameplate').textContent === 'Dana' && !document.querySelector('#obs-nameplate').hidden`,
			nil, chromedp.WithPollingTimeout(15*time.Second)),
	); err != nil {
		t.Fatalf("nameplate did not show the bound occupant's name: %v", err)
	}

	// Stash the current MediaStream id so we can prove the name refresh does NOT re-link media.
	if err := chromedp.Run(obsCtx, chromedp.Evaluate(`window.__beforeStreamId = document.querySelector('#obs-video').srcObject.id`, nil)); err != nil {
		t.Fatalf("stash stream id: %v", err)
	}

	// Host overrides the name with a HOSTILE, over-long value. The server caps it (EN-15) and
	// refreshes the live nameplate at the same epoch.
	hostile := `<img src=x onerror="window.__pwned=1">` + strings.Repeat("Z", 120)
	wantName := signaling.CapDisplayName(hostile)
	putHostJSON(t, s, "/api/passes/"+s.passID+"/name", nameJSON(hostile))

	var domName string
	var injected, pwned, streamSame, rendering bool
	if err := chromedp.Run(obsCtx,
		// The nameplate updates to the capped name…
		chromedp.Poll(`document.querySelector('#obs-nameplate').textContent === `+jsString(wantName),
			nil, chromedp.WithPollingTimeout(15*time.Second)),
		chromedp.Evaluate(`document.querySelector('#obs-nameplate').textContent`, &domName),
		// …as ESCAPED textContent: the hostile markup created NO element inside the nameplate…
		chromedp.Evaluate(`document.querySelector('#obs-nameplate img') !== null`, &injected),
		// …and ran NO script.
		chromedp.Evaluate(`window.__pwned === 1`, &pwned),
		// The media link was NOT re-created (same MediaStream object) — a name-only refresh.
		chromedp.Evaluate(`document.querySelector('#obs-video').srcObject.id === window.__beforeStreamId`, &streamSame),
		chromedp.Evaluate(`document.querySelector('#obs-video').videoWidth > 0`, &rendering),
	); err != nil {
		t.Fatalf("nameplate override assertions: %v", err)
	}
	if domName != wantName {
		t.Fatalf("nameplate textContent = %q, want the capped %q", domName, wantName)
	}
	if len([]rune(domName)) > 60 {
		t.Fatalf("nameplate name length %d exceeds the EN-15 cap", len([]rune(domName)))
	}
	if injected {
		t.Fatalf("hostile name injected an element — must render as escaped textContent (EN-15)")
	}
	if pwned {
		t.Fatalf("hostile name executed script — must render as inert textContent (EN-15)")
	}
	if !streamSame {
		t.Fatalf("name refresh re-linked media (new MediaStream) — must be a same-epoch nameplate-only refresh")
	}
	if !rendering {
		t.Fatalf("video stopped rendering after the name refresh — the media link must be untouched")
	}
}

// T-7 / AC-7: the nameplate is HIDDEN by default — a source URL WITHOUT the ?name param renders no
// nameplate even when the bound occupant has a display name. Visibility is the host's per-source
// show/hide choice (D-16), not a property of the name.
func TestNameplate_HiddenByDefault(t *testing.T) {
	s := seedDeviceCheck(t)
	ctx := context.Background()
	if _, err := s.store.StartSession(ctx, s.streamID, s.hostID); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelAlloc()
	guestCtx, cancelGuest := chromedp.NewContext(allocCtx)
	defer cancelGuest()
	guestCtx, cancelGuestT := context.WithTimeout(guestCtx, 180*time.Second)
	defer cancelGuestT()
	obsCtx, cancelOBS := chromedp.NewContext(guestCtx)
	defer cancelOBS()

	if err := chromedp.Run(guestCtx,
		chromedp.Navigate(s.base+"/p/"+s.rawToken),
		chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
		chromedp.Click(`.dc-start`, chromedp.ByQuery),
		chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.dc-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(30*time.Second)),
		chromedp.Click(`.dc-enter`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-entered="1"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("guest publish flow: %v", err)
	}

	// OBS source WITHOUT the ?name param (nameplate hidden by default).
	if err := chromedp.Run(obsCtx,
		chromedp.Navigate(s.base+"/s/"+s.slotLabel+"?token="+s.srcToken),
		chromedp.WaitVisible(`#obs-video`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("obs source page did not load: %v", err)
	}
	putHostJSON(t, s, "/api/passes/"+s.passID+"/slot", `{"slot":"cam-1"}`)

	// The occupant renders, but the nameplate stays hidden and empty (no ?name).
	if err := chromedp.Run(obsCtx,
		chromedp.Poll(`document.querySelector('#obs-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(60*time.Second)),
	); err != nil {
		t.Fatalf("bound occupant did not render: %v", err)
	}
	var hidden bool
	var text string
	if err := chromedp.Run(obsCtx,
		chromedp.Evaluate(`document.querySelector('#obs-nameplate').hidden`, &hidden),
		chromedp.Evaluate(`document.querySelector('#obs-nameplate').textContent`, &text),
	); err != nil {
		t.Fatalf("read nameplate state: %v", err)
	}
	if !hidden || text != "" {
		t.Fatalf("nameplate must be hidden+empty without ?name; hidden=%v text=%q", hidden, text)
	}
}

// jsString renders s as a JavaScript string literal for embedding in a Poll/Evaluate expression.
func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
