package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/rock3r/guest-pass/internal/store"
)

func TestStatsPageRendersAnonymousTotalsAndTrend(t *testing.T) {
	a := newAPIHarness(t)
	for key, value := range map[string]int64{
		store.CounterStreamsRun:            7,
		store.CounterGuestConnectedSeconds: 540,
		store.CounterInvitesSent:           11,
	} {
		if err := a.store.AddCounter(context.Background(), key, value); err != nil {
			t.Fatalf("AddCounter(%s): %v", key, err)
		}
	}
	rec := a.req(t, http.MethodGet, "/stats", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /stats = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Anonymous instance totals", "7", "9", "guest-minutes", "Daily streams"} {
		if !strings.Contains(body, want) {
			t.Errorf("stats page missing %q:\n%s", want, body)
		}
	}
	if !strings.Contains(body, "never tied to a host, stream, guest, or IP") {
		t.Error("stats page must state its anonymity boundary")
	}
}
