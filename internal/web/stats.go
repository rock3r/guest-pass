package web

import (
	"net/http"
	"slices"
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
	DailyActivity  []dailyActivity
}

// dailyActivity merges the independent, anonymous daily counters for a single
// UTC day. It deliberately has no entity dimension: a row cannot be joined
// back to any host, stream, guest, pass, or connection.
type dailyActivity struct {
	Day          string
	Streams      int64
	GuestMinutes int64
	Invites      int64
	Hosts        int64
	Reports      int64
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
		activity, err := dailyActivitySeries(r, st)
		if err != nil {
			http.Error(w, "could not load statistics", http.StatusInternalServerError)
			return
		}
		rd.render(w, r, "stats.html", pageData{Title: "Stats", Data: publicStatsData{
			StreamsRun: streams, GuestMinutes: seconds / 60, InvitesSent: invites, ReportsFiled: reports, PeakConcurrent: peak, TotalHosts: hosts, DailyActivity: activity,
		}})
	}
}

func dailyActivitySeries(r *http.Request, st *store.Store) ([]dailyActivity, error) {
	since := time.Now().UTC().AddDate(0, 0, -30).Format(time.DateOnly)
	keys := []string{
		store.CounterStreamsRun,
		store.CounterGuestConnectedSeconds,
		store.CounterInvitesSent,
		store.CounterTotalHosts,
		store.CounterReportsFiled,
	}
	byDay := make(map[string]*dailyActivity)
	for _, key := range keys {
		series, err := st.CounterSeries(r.Context(), key, since)
		if err != nil {
			return nil, err
		}
		for _, point := range series {
			row := byDay[point.Day]
			if row == nil {
				row = &dailyActivity{Day: point.Day}
				byDay[point.Day] = row
			}
			switch key {
			case store.CounterStreamsRun:
				row.Streams = point.Value
			case store.CounterGuestConnectedSeconds:
				row.GuestMinutes = point.Value / 60
			case store.CounterInvitesSent:
				row.Invites = point.Value
			case store.CounterTotalHosts:
				row.Hosts = point.Value
			case store.CounterReportsFiled:
				row.Reports = point.Value
			}
		}
	}
	if len(byDay) == 0 {
		return nil, nil
	}
	days := make([]string, 0, len(byDay))
	for day := range byDay {
		days = append(days, day)
	}
	slices.Sort(days)
	out := make([]dailyActivity, 0, len(days))
	for _, day := range days {
		out = append(out, *byDay[day])
	}
	return out, nil
}
