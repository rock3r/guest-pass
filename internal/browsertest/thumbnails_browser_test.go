//go:build browser

package browsertest

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/coder/websocket"

	"github.com/rock3r/guest-pass/internal/signaling"
)

// T-11b / AC-12 (D-10): everyone-backstage thumbnails. Two guests each enter their guest-session
// from their OWN fake-media browser; each guest meshes directly with the other (a bidirectional P2P
// connection, no host involved) and renders the other's camera as a backstage thumbnail tile. The
// guest's own camera is the self-view; the other participant is a thumbnail. The server only relays
// the opaque SDP/ICE (D-23).
func TestGuestSession_BackstageThumbnails(t *testing.T) {
	s := seedGrid(t, 2)

	ctxA := enterGuestSession(t, s.base, s.rawTokens[0], "A")
	ctxB := enterGuestSession(t, s.base, s.rawTokens[1], "B")

	// Each guest renders at least one OTHER-participant thumbnail with live frames over the mesh.
	const thumb = `document.querySelectorAll('.gr-tile .gr-video').length >= 1 && ` +
		`[...document.querySelectorAll('.gr-tile .gr-video')].some((v) => v.videoWidth > 0)`
	if err := chromedp.Run(ctxA, chromedp.Poll(thumb, nil, chromedp.WithPollingTimeout(90*time.Second))); err != nil {
		t.Fatalf("guest A did not render guest B's backstage thumbnail over the mesh: %v", err)
	}
	if err := chromedp.Run(ctxB, chromedp.Poll(thumb, nil, chromedp.WithPollingTimeout(90*time.Second))); err != nil {
		t.Fatalf("guest B did not render guest A's backstage thumbnail over the mesh: %v", err)
	}
}

// T-11b / AC-11 (co-host) + the deferred PR-10 codex finding: a co-host moderates from its
// guest-session backstage thumbnails (the host-only /greenroom isn't reachable with a pass), and
// the Release control is gated by the LOCK FLOOR — not by being strictly above the target — so a
// co-host can release a floor-level lock on a peer later promoted to EQUAL (co-host) rank. C and G
// both enter; the host promotes C to co-host; C force-mutes guest G (lock floor = co-host); the host
// promotes G to co-host; C — now equal rank to G — must still see + use Release on G's tile.
func TestGuestSession_CohostModeratesAndReleasesPromotedPeer(t *testing.T) {
	s := seedDeviceCheck(t)
	cCtx := enterGuestSession(t, s.base, s.rawToken, "C")  // guest "Dana"
	gCtx := enterGuestSession(t, s.base, s.rawTokenB, "G") // guest "Erin"

	host := dialHostWS(t, s)
	defer host.Close(websocket.StatusNormalClosure, "")

	// Promote C → co-host so its thumbnail of guest G shows the moderation controls.
	writeFrame(t, host, signaling.Frame{T: "role", PeerID: s.passID, Role: "cohost"})

	// C force-mutes G from the guest-session → G sees the live force-lock notice (lock floor = the
	// applier's rank = co-host). C's only thumbnail is G, so target the single guest tile.
	if err := chromedp.Run(cCtx,
		chromedp.WaitVisible(`.gr-tile[data-role="guest"] .gr-force[data-kind="mic"]`, chromedp.ByQuery),
		chromedp.Click(`.gr-tile[data-role="guest"] .gr-force[data-kind="mic"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("a co-host could not force-mute a guest from the guest-session (AC-11): %v", err)
	}
	if err := chromedp.Run(gCtx, chromedp.WaitVisible(`.gs-lock`, chromedp.ByQuery)); err != nil {
		t.Fatalf("the force-muted guest did not show the live force-lock notice: %v", err)
	}

	// Promote G → co-host. The lock persists (D-13), and C and G are now EQUAL rank — Release must
	// still render on C's tile for G (floor-gated, not strictly-above-gated; codex P2 fix).
	writeFrame(t, host, signaling.Frame{T: "role", PeerID: s.passIDB, Role: "cohost"})
	if err := chromedp.Run(cCtx,
		chromedp.WaitVisible(`.gr-tile[data-role="cohost"] .gr-release[data-kind="mic"]`, chromedp.ByQuery),
		chromedp.Click(`.gr-tile[data-role="cohost"] .gr-release[data-kind="mic"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("a co-host could not release a floor-level lock on an equal-rank promoted peer (codex P2): %v", err)
	}
	if err := chromedp.Run(gCtx, chromedp.WaitNotPresent(`.gs-lock`, chromedp.ByQuery)); err != nil {
		t.Fatalf("the lock did not clear after the co-host released it: %v", err)
	}
}
