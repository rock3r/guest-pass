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

// T-8 / AC-8: the host links a Twitch channel on the stream-detail page; the page then shows the
// linked channel and the derived public "watch live" link (the same link guests get). Server-
// rendered, no WebRTC — just the live-verify link form.
func TestStreamDetail_LinkChannelAndWatchLink(t *testing.T) {
	s := seedHostApp(t)
	stream, err := s.store.CreateStream(context.Background(), store.CreateStreamParams{HostID: s.hostID, Title: "Linked Show"})
	if err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCkie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})

	Chrome(t, 60*time.Second, func(ctx context.Context) {
		// Link a Twitch channel via the stream-detail form.
		var watchHref, linkedText string
		if err := chromedp.Run(ctx,
			network.Enable(),
			setHostCookie,
			chromedp.Navigate(s.base+"/app/streams/"+stream.ID),
			chromedp.WaitVisible(`.live-verify`, chromedp.ByQuery),
			chromedp.SetValue(`.live-verify-form input[name="channel"]`, "Ninja", chromedp.ByQuery),
			chromedp.Click(`.live-verify-form button[type="submit"]`, chromedp.ByQuery),
			// After the PRG redirect, the linked channel + watch link are shown.
			chromedp.WaitVisible(`.live-verify .watch-live`, chromedp.ByQuery),
			chromedp.AttributeValue(`.live-verify .watch-live`, "href", &watchHref, nil),
			chromedp.Text(`.live-verify-current`, &linkedText, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("link channel flow: %v", err)
		}
		if watchHref != "https://www.twitch.tv/ninja" {
			t.Fatalf("watch link href = %q, want the normalized twitch channel URL", watchHref)
		}
		if !strings.Contains(linkedText, "twitch/ninja") {
			t.Fatalf("linked-channel text = %q, want it to show twitch/ninja", linkedText)
		}
		// It persisted (normalized + lowercased) server-side.
		if got, _ := s.store.GetStream(context.Background(), stream.ID); got.TwitchYTChannel == nil || *got.TwitchYTChannel != "ninja" {
			t.Fatalf("channel not persisted: %v", got.TwitchYTChannel)
		}
	})
}
