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

// T-2 / AC-2: the read-only calendar renders a host's scheduled stream (in the month grid
// and the agenda) and links to its detail; no recurring/repeat control is present (D-8).
func TestHostApp_CalendarRendersScheduledStreams(t *testing.T) {
	s := seedHostApp(t)
	ctxBg := context.Background()
	sched := time.Date(2026, time.September, 10, 18, 0, 0, 0, time.UTC).Unix()
	dur := int64(60)
	stream, err := s.store.CreateStream(ctxBg, store.CreateStreamParams{
		HostID: s.hostID, Title: "Season Premiere", Status: store.StreamScheduled,
		ScheduledAt: &sched, DurationMin: &dur,
	})
	if err != nil {
		t.Fatalf("CreateStream: %v", err)
	}

	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCkie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})

	Chrome(t, 60*time.Second, func(ctx context.Context) {
		var agendaText, eventHref, bodyText string
		if err := chromedp.Run(ctx,
			network.Enable(),
			setHostCookie,
			chromedp.Navigate(s.base+"/app/calendar?month=2026-09"),
			chromedp.WaitVisible(`.cal-grid`, chromedp.ByQuery),
			// The active nav reflects the calendar page.
			chromedp.WaitVisible(`.app-links a[aria-current="page"][href="/app/calendar"]`, chromedp.ByQuery),
			// The stream shows in the month grid on its day, linking to the detail.
			chromedp.WaitVisible(`.cal-event`, chromedp.ByQuery),
			chromedp.AttributeValue(`.cal-event`, "href", &eventHref, nil),
			// …and in the agenda list.
			chromedp.Text(`.agenda-list`, &agendaText, chromedp.ByQuery),
			chromedp.Evaluate(`document.body.innerText`, &bodyText),
		); err != nil {
			t.Fatalf("calendar render: %v", err)
		}
		if !strings.Contains(agendaText, "Season Premiere") {
			t.Fatalf("agenda missing the scheduled stream; got %q", agendaText)
		}
		wantHref := "/app/streams/" + stream.ID + "/edit"
		if !strings.Contains(eventHref, wantHref) {
			t.Fatalf("calendar event href = %q, want it to link to %q", eventHref, wantHref)
		}
		// Recurring/repeat is hidden in v1 (D-8): no such control anywhere on the page.
		low := strings.ToLower(bodyText)
		if strings.Contains(low, "recurring") || strings.Contains(low, "repeat") {
			t.Fatalf("calendar must not surface a recurring/repeat control; body = %q", bodyText)
		}
	})
}
