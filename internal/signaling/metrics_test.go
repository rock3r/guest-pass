package signaling

import (
	"context"
	"sync"
	"testing"
	"time"
)

type metricCall struct {
	key   string
	value int64
}

type metricsRecorder struct {
	mu       sync.Mutex
	adds     []metricCall
	maxes    []metricCall
	addEvent chan struct{}
}

func newMetricsRecorder() *metricsRecorder {
	return &metricsRecorder{addEvent: make(chan struct{}, 16)}
}

func (m *metricsRecorder) AddCounter(_ context.Context, key string, value int64) error {
	m.mu.Lock()
	m.adds = append(m.adds, metricCall{key: key, value: value})
	m.mu.Unlock()
	m.addEvent <- struct{}{}
	return nil
}

func (m *metricsRecorder) BumpMax(_ context.Context, key string, value int64) error {
	m.mu.Lock()
	m.maxes = append(m.maxes, metricCall{key: key, value: value})
	m.mu.Unlock()
	return nil
}

func (m *metricsRecorder) guestDurationCalls() (count, seconds int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, call := range m.adds {
		if call.key == counterGuestConnectedSeconds {
			count++
			seconds += int(call.value)
		}
	}
	return count, seconds
}

func (m *metricsRecorder) peak() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	var peak int64
	for _, call := range m.maxes {
		if call.key == counterPeakConcurrent && call.value > peak {
			peak = call.value
		}
	}
	return peak
}

func (m *metricsRecorder) counterTotal(key string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	var total int64
	for _, call := range m.adds {
		if call.key == key {
			total += call.value
		}
	}
	return total
}

func agePeer(t *testing.T, r *Room, id PeerID, age time.Duration) {
	t.Helper()
	done := make(chan struct{})
	r.post(func(_ *roomState, conns map[PeerID]*peerConn) {
		conns[id].joinedAt = time.Now().Add(-age)
		close(done)
	})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out aging peer")
	}
}

func waitForGuestDuration(t *testing.T, recorder *metricsRecorder, want int) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		count, _ := recorder.guestDurationCalls()
		if count == want {
			return
		}
		select {
		case <-recorder.addEvent:
		case <-deadline:
			t.Fatalf("guest duration calls = %d, want %d", count, want)
		}
	}
}

// T-CAP: every actual guest connection contributes its duration exactly once,
// including terminal teardown paths that bypass a websocket Leave callback.
func TestRoomMetricsCapturesGuestDurationOnEveryDeparturePath(t *testing.T) {
	cases := []struct {
		name   string
		depart func(*Room, chan Frame)
	}{
		{"leave", func(r *Room, out chan Frame) { r.Leave("guest", out) }},
		{"kick", func(r *Room, _ chan Frame) { r.Kick("host", "guest", nil) }},
		{"evict", func(r *Room, _ chan Frame) { r.EvictPeers(TerminateRevoked, []PeerID{"guest"}) }},
		{"terminate", func(r *Room, _ chan Frame) { r.Terminate(TerminateSessionEnded) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := newMetricsRecorder()
			r := newRoom("metrics", nil, nil, 0, recorder)
			go r.run()
			defer r.Close()

			if tc.name == "kick" {
				r.Join("host", "host", "", "", make(chan Frame, 8))
			}
			out := make(chan Frame, 8)
			r.Join("guest", "guest", "", "", out)
			agePeer(t, r, "guest", 2*time.Second)
			tc.depart(r, out)
			waitForGuestDuration(t, recorder, 1)
			if _, seconds := recorder.guestDurationCalls(); seconds < 2 {
				t.Fatalf("guest duration = %ds, want at least 2s", seconds)
			}
		})
	}
}

func TestRoomMetricsCountsDisplacedConnectionOnceAndTracksParticipantPeak(t *testing.T) {
	recorder := newMetricsRecorder()
	r := newRoom("metrics", nil, nil, 0, recorder)
	go r.run()
	defer r.Close()

	r.Join("host", "host", "", "", make(chan Frame, 8))
	first := make(chan Frame, 8)
	r.Join("guest", "guest", "", "", first)
	agePeer(t, r, "guest", 2*time.Second)

	second := make(chan Frame, 8)
	r.Join("guest", "guest", "", "", second) // displacement closes and records first
	agePeer(t, r, "guest", 2*time.Second)
	r.Leave("guest", second)

	waitForGuestDuration(t, recorder, 2)
	if count, seconds := recorder.guestDurationCalls(); count != 2 || seconds < 4 {
		t.Fatalf("displaced guest metrics = %d calls, %ds; want exactly 2 calls and at least 4s", count, seconds)
	}
	if peak := recorder.peak(); peak != 2 {
		t.Fatalf("peak participant count = %d, want 2 (host + guest)", peak)
	}
}

func TestRoomMetricsCountsEachMediaLinkOnceAndOnlyWhenBothPeersExist(t *testing.T) {
	recorder := newMetricsRecorder()
	r := newRoom("metrics", nil, nil, 0, recorder)
	go r.run()
	defer r.Close()

	r.Join("host", "host", "", "", make(chan Frame, 8))
	r.Join("guest", "guest", "", "", make(chan Frame, 8))
	r.RecordConnection("host", "guest", "", false)
	r.RecordConnection("guest", "host", "", true) // duplicate peer pair; first sample wins
	r.RecordConnection("host", "missing", "screen", true)

	deadline := time.After(time.Second)
	for recorder.counterTotal(counterConnectionsTotal) != 1 {
		select {
		case <-recorder.addEvent:
		case <-deadline:
			t.Fatalf("connection samples = %d, want 1", recorder.counterTotal(counterConnectionsTotal))
		}
	}
	if got := recorder.counterTotal(counterConnectionsRelayed); got != 0 {
		t.Fatalf("relayed samples = %d, want 0", got)
	}
}
