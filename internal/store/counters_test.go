package store

import (
	"context"
	"sync"
	"testing"
)

func TestCounters_AddCounterTracksLifetimeAndUTCDay(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	if err := st.AddCounter(ctx, CounterStreamsRun, 2); err != nil {
		t.Fatalf("AddCounter: %v", err)
	}
	if err := st.AddCounter(ctx, CounterStreamsRun, 3); err != nil {
		t.Fatalf("AddCounter: %v", err)
	}
	if got, err := st.Counter(ctx, CounterStreamsRun); err != nil || got != 5 {
		t.Fatalf("Counter = %d, %v; want 5, nil", got, err)
	}
	series, err := st.CounterSeries(ctx, CounterStreamsRun, "")
	if err != nil || len(series) != 1 || series[0].Value != 5 {
		t.Fatalf("CounterSeries = %+v, %v; want one daily bucket with 5", series, err)
	}
}

func TestCounters_BumpMaxNeverDecreases(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	for _, value := range []int64{3, 1, 7, 5} {
		if err := st.BumpMax(ctx, CounterPeakConcurrent, value); err != nil {
			t.Fatalf("BumpMax(%d): %v", value, err)
		}
	}
	if got, err := st.Counter(ctx, CounterPeakConcurrent); err != nil || got != 7 {
		t.Fatalf("Counter = %d, %v; want 7, nil", got, err)
	}
}

func TestCounters_AreAnonymousAndSurviveErasureAndPurge(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	if err := st.AddCounter(ctx, CounterInvitesSent, 4); err != nil {
		t.Fatalf("AddCounter: %v", err)
	}
	h := seedHost(t, st, "counter-erasure")
	stream, err := st.CreateStream(ctx, CreateStreamParams{HostID: h.ID, Title: "Counter show"})
	if err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	name, email := "Guest", "guest@example.com"
	if _, err := st.CreatePass(ctx, CreatePassParams{StreamID: stream.ID, Name: &name, Email: &email, TokenHash: "counter-token"}); err != nil {
		t.Fatalf("CreatePass: %v", err)
	}

	if err := st.DeleteHost(ctx, h.ID); err != nil {
		t.Fatalf("DeleteHost: %v", err)
	}
	if _, err := st.PurgeGuestPII(ctx, 1<<62, 1, 1); err != nil {
		t.Fatalf("PurgeGuestPII: %v", err)
	}
	if got, err := st.Counter(ctx, CounterInvitesSent); err != nil || got != 4 {
		t.Fatalf("counter after erasure/purge = %d, %v; want 4, nil", got, err)
	}

	for _, table := range []string{"counters", "counters_daily"} {
		rows, err := st.reader.QueryContext(ctx, "PRAGMA foreign_key_list("+table+")")
		if err != nil {
			t.Fatalf("foreign_key_list(%s): %v", table, err)
		}
		defer rows.Close()
		if rows.Next() {
			t.Fatalf("%s has a foreign key; anonymous counters must be decoupled", table)
		}
	}
}

func TestCounters_ConcurrentAddsRemainExact(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	const workers, increments = 12, 25
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range increments {
				if err := st.AddCounter(ctx, CounterReportsFiled, 1); err != nil {
					t.Errorf("AddCounter: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if got, err := st.Counter(ctx, CounterReportsFiled); err != nil || got != workers*increments {
		t.Fatalf("Counter = %d, %v; want %d, nil", got, err, workers*increments)
	}
}

func TestCounters_RecordHostReportAndCompletedSession(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	h, err := st.CreateHost(ctx, CreateHostParams{GoogleSub: "counter-host", Email: "counter@example.com", Name: "Counter", Status: HostActive})
	if err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	stream, err := st.CreateStream(ctx, CreateStreamParams{HostID: h.ID, Title: "Counter stream"})
	if err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	if _, err := st.CreateReport(ctx, CreateReportParams{HostID: h.ID, Category: ReportSpam, Message: "unwanted"}); err != nil {
		t.Fatalf("CreateReport: %v", err)
	}
	if _, err := st.StartSession(ctx, stream.ID, h.ID); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if err := st.EndActiveSession(ctx, h.ID); err != nil {
		t.Fatalf("EndActiveSession: %v", err)
	}
	// A repeated end is a lifecycle no-op, not a second completed stream.
	if err := st.EndActiveSession(ctx, h.ID); err != nil {
		t.Fatalf("second EndActiveSession: %v", err)
	}

	for _, want := range []struct {
		key   string
		value int64
	}{
		{CounterTotalHosts, 1},
		{CounterReportsFiled, 1},
		{CounterStreamsRun, 1},
	} {
		if got, err := st.Counter(ctx, want.key); err != nil || got != want.value {
			t.Errorf("%s = %d, %v; want %d, nil", want.key, got, err, want.value)
		}
	}
}
