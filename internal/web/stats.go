package web

import (
	"net/http"
	"time"

	"github.com/rock3r/guest-pass/internal/store"
)

type publicStatsData struct {
	StreamsRun     int64
	GuestMinutes   int64
	InvitesSent    int64
	ReportsFiled   int64
	PeakConcurrent int64
	TotalHosts     int64
	DailyStreams   []store.DailyCounter
}

// statsPage is the public, no-JS transparency surface for the global anonymous
// aggregates. It intentionally reads only no-FK counter rows; operational and PII
// tables never participate in this response.
func (rd *renderer) statsPage(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		read := func(key string) (int64, error) { return st.Counter(r.Context(), key) }
		streams, err := read(store.CounterStreamsRun)
		if err != nil {
			http.Error(w, "could not load statistics", http.StatusInternalServerError)
			return
		}
		seconds, err := read(store.CounterGuestConnectedSeconds)
		if err != nil {
			http.Error(w, "could not load statistics", http.StatusInternalServerError)
			return
		}
		invites, err := read(store.CounterInvitesSent)
		if err != nil {
			http.Error(w, "could not load statistics", http.StatusInternalServerError)
			return
		}
		reports, err := read(store.CounterReportsFiled)
		if err != nil {
			http.Error(w, "could not load statistics", http.StatusInternalServerError)
			return
		}
		peak, err := read(store.CounterPeakConcurrent)
		if err != nil {
			http.Error(w, "could not load statistics", http.StatusInternalServerError)
			return
		}
		hosts, err := read(store.CounterTotalHosts)
		if err != nil {
			http.Error(w, "could not load statistics", http.StatusInternalServerError)
			return
		}
		series, err := st.CounterSeries(r.Context(), store.CounterStreamsRun, time.Now().UTC().AddDate(0, 0, -30).Format(time.DateOnly))
		if err != nil {
			http.Error(w, "could not load statistics", http.StatusInternalServerError)
			return
		}
		rd.render(w, r, "stats.html", pageData{Title: "Stats", Data: publicStatsData{
			StreamsRun: streams, GuestMinutes: seconds / 60, InvitesSent: invites, ReportsFiled: reports, PeakConcurrent: peak, TotalHosts: hosts, DailyStreams: series,
		}})
	}
}
