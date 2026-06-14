package web

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
)

// totalRows sums COUNT(*) across every user table — a store-spy snapshot for the EN-20 proof.
func totalRows(t *testing.T, dbPath string) int {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		names = append(names, n)
	}
	rows.Close()
	total := 0
	for _, n := range names {
		var c int
		if err := db.QueryRow(`SELECT COUNT(*) FROM "` + n + `"`).Scan(&c); err != nil {
			t.Fatalf("count %s: %v", n, err)
		}
		total += c
	}
	return total
}

// AC-6/T-6 — the EN-20 chat-purity invariant proven end to end: a backstage chat relays
// from-stamped to participants, and leaves NO trace — the message text never appears in any log
// line (sentinel), and the chat writes ZERO new DB rows (store-spy). The sender's spoofed `from`
// is ignored (EN-7).
func TestWS_ChatRelaysAndLeavesNoTrace(t *testing.T) {
	const sentinel = "SENTINEL-zx9-backstage-secret-must-not-persist-or-log"
	h := newWSHarness(t, wsHarnessOpts{})
	host, cookie := h.seedHost(t, "host1", store.HostActive)
	stream := h.seedStream(t, host.ID)
	aRaw, aPass := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, nil)
	bRaw, _ := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, nil)

	hc := h.dialOK(t, "", cookieHeader(cookie))
	defer hc.CloseNow()
	wsReadFrameOfType(t, hc, "roster")
	a := h.dialOK(t, "pass="+aRaw, nil)
	defer a.CloseNow()
	wsReadFrameOfType(t, a, "roster")
	b := h.dialOK(t, "pass="+bRaw, nil)
	defer b.CloseNow()
	wsReadFrameOfType(t, b, "roster")
	// Sync: the host learns both guests joined, so the room is fully populated before the chat.
	wsReadFrameOfType(t, hc, "peer-joined")
	wsReadFrameOfType(t, hc, "peer-joined")

	before := totalRows(t, h.dbPath)

	// Guest A chats, attempting to spoof `from`.
	wsWriteFrame(t, a, signaling.Frame{T: "chat", Text: sentinel, From: "spoofed-id"})

	// B receives it, stamped from A's real id (not the spoof); the text round-trips intact.
	if fb := wsReadFrameOfType(t, b, "chat"); fb.From != aPass.ID || fb.Text != sentinel {
		t.Fatalf("B's chat = %+v, want from=%s and the sentinel text", fb, aPass.ID)
	}
	// The host receives it too.
	if fh := wsReadFrameOfType(t, hc, "chat"); fh.From != aPass.ID || fh.Text != sentinel {
		t.Fatalf("host's chat = %+v, want from=%s and the sentinel text", fh, aPass.ID)
	}

	// EN-20: the message text never appears in ANY captured log line (handler or room).
	if logs := h.logs.String(); strings.Contains(logs, sentinel) {
		t.Fatalf("backstage chat text must NEVER be logged (EN-20) — found the sentinel in the logs")
	}
	// EN-20: the chat wrote nothing to disk.
	if after := totalRows(t, h.dbPath); after != before {
		t.Fatalf("backstage chat must leave no disk trace (EN-20); total rows %d -> %d", before, after)
	}
}

// EN-20 (structural): the schema has NO table that could persist chat — backstage chat is
// unpersistable by construction, not merely unpersisted by current code.
func TestNoChatTableInSchema(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	db, err := sql.Open("sqlite", h.dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if low := strings.ToLower(n); strings.Contains(low, "chat") || strings.Contains(low, "message") {
			t.Fatalf("the schema must have no chat/message table (EN-20), found %q", n)
		}
	}
}
