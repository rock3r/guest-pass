package mail

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestLogMailer_PrintsMagicLink(t *testing.T) {
	var buf bytes.Buffer
	m := NewLogMailer(&buf)
	err := m.SendInvite(context.Background(), Invite{
		To: "guest@example.com", GuestName: "Guest", StreamTitle: "My Show",
		MagicLink: "https://gp.example/p/abc123",
	})
	if err != nil {
		t.Fatalf("SendInvite: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "https://gp.example/p/abc123") {
		t.Errorf("log output missing magic link: %q", out)
	}
	if !strings.Contains(out, "guest@example.com") {
		t.Errorf("log output missing recipient: %q", out)
	}
}

func TestResendMailer_PostsInvite(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/emails" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"email_1"}`))
	}))
	defer srv.Close()

	m := NewResendMailer("re_test_key", "GuestPass <invites@gp.example>")
	m.baseURL = srv.URL
	if err := m.SendInvite(context.Background(), Invite{To: "g@example.com", StreamTitle: "Show", MagicLink: "https://gp.example/p/tok"}); err != nil {
		t.Fatalf("SendInvite: %v", err)
	}
	if gotAuth != "Bearer re_test_key" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if to, _ := payload["to"].([]any); len(to) != 1 || to[0] != "g@example.com" {
		t.Errorf("to = %v", payload["to"])
	}
	if payload["from"] != "GuestPass <invites@gp.example>" {
		t.Errorf("from = %v", payload["from"])
	}
}

// AC-6 (D-37 §8): every invite email carries the "before" 24h-deletion transparency notice, both
// in the rendered body and in the Resend payload's html field.
func TestInviteEmail_CarriesPurgeNotice(t *testing.T) {
	got := inviteHTML(Invite{GuestName: "Greta", StreamTitle: "Show", MagicLink: "https://gp.example/p/tok"})
	if !strings.Contains(got, "deleted within 24 hours") {
		t.Fatalf("invite email body missing the 24h transparency notice (AC-6):\n%s", got)
	}
	// The invite email also carries the "report it" link (D-42), pointing at this invite's report form.
	if !strings.Contains(got, `href="https://gp.example/p/tok/report"`) || !strings.Contains(got, "Report it") {
		t.Fatalf("invite email body missing the D-42 report link:\n%s", got)
	}

	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"e"}`))
	}))
	defer srv.Close()
	m := NewResendMailer("re_k", "from@gp.example")
	m.baseURL = srv.URL
	if err := m.SendInvite(context.Background(), Invite{To: "g@example.com", StreamTitle: "Show", MagicLink: "https://gp.example/p/tok"}); err != nil {
		t.Fatalf("SendInvite: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if h, _ := payload["html"].(string); !strings.Contains(h, "deleted within 24 hours") {
		t.Fatalf("Resend payload html missing the 24h notice: %q", h)
	}
}

func TestResendMailer_Non2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	m := NewResendMailer("bad", "from@gp.example")
	m.baseURL = srv.URL
	if err := m.SendInvite(context.Background(), Invite{To: "g@example.com"}); err == nil {
		t.Fatal("expected an error on non-2xx response")
	}
}

// fakeSMTPServer is a minimal SMTP server for testing over plain TCP (no TLS).
// It handles one connection at a time and records envelope and DATA payload.
type fakeSMTPServer struct {
	ln   net.Listener
	addr string
	mu   sync.Mutex
	// captured per-send
	lastFrom string
	lastTo   string
	lastData string
}

func newFakeSMTPServer(t *testing.T) *fakeSMTPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeSMTPServer{ln: ln, addr: ln.Addr().String()}
	t.Cleanup(func() { _ = ln.Close() })
	go s.serveLoop(t)
	return s
}

func (s *fakeSMTPServer) serveLoop(t *testing.T) {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handleConn(t, conn)
	}
}

func (s *fakeSMTPServer) handleConn(t *testing.T, conn net.Conn) {
	t.Helper()
	defer conn.Close()
	r := bufio.NewReader(conn)

	send := func(line string) {
		if _, err := fmt.Fprintf(conn, "%s\r\n", line); err != nil && t != nil {
			t.Logf("fakeSMTP write: %v", err)
		}
	}

	send("220 localhost ESMTP test")
	for {
		raw, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(raw))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			send("250-localhost")
			send("250-AUTH PLAIN LOGIN")
			send("250 OK")
		case strings.HasPrefix(cmd, "AUTH"):
			send("235 Authentication successful")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			s.mu.Lock()
			s.lastFrom = smtpAngle(strings.TrimSpace(raw)[10:])
			s.mu.Unlock()
			send("250 OK")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			s.mu.Lock()
			s.lastTo = smtpAngle(strings.TrimSpace(raw)[8:])
			s.mu.Unlock()
			send("250 OK")
		case cmd == "DATA":
			send("354 End data with <CR><LF>.<CR><LF>")
			var body strings.Builder
			for {
				dl, derr := r.ReadString('\n')
				if derr != nil {
					return
				}
				if strings.TrimRight(dl, "\r\n") == "." {
					break
				}
				body.WriteString(dl)
			}
			s.mu.Lock()
			s.lastData = body.String()
			s.mu.Unlock()
			send("250 OK: queued")
		case cmd == "QUIT":
			send("221 Bye")
			return
		default:
			send("500 Unknown command: " + cmd)
		}
	}
}

// smtpAngle extracts the address from <addr> or returns the trimmed input.
func smtpAngle(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 0 && s[0] == '<' {
		if end := strings.Index(s, ">"); end > 0 {
			return s[1:end]
		}
	}
	return s
}

func TestSMTPMailer_SendsInvite(t *testing.T) {
	srv := newFakeSMTPServer(t)
	_, port, _ := net.SplitHostPort(srv.addr)
	m := NewSMTPMailer("127.0.0.1", port, "user@example.com", "pass", "GuestPass <noreply@example.com>")
	m.serverAddr = srv.addr // bypass TLS

	err := m.SendInvite(context.Background(), Invite{
		To: "guest@example.com", GuestName: "Alice", StreamTitle: "My Show",
		MagicLink: "https://gp.example/p/tok",
	})
	if err != nil {
		t.Fatalf("SendInvite: %v", err)
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()

	if srv.lastFrom != "noreply@example.com" {
		t.Errorf("MAIL FROM = %q, want noreply@example.com", srv.lastFrom)
	}
	if srv.lastTo != "guest@example.com" {
		t.Errorf("RCPT TO = %q, want guest@example.com", srv.lastTo)
	}
	if !strings.Contains(srv.lastData, "https://gp.example/p/tok") {
		t.Errorf("body missing magic link:\n%s", srv.lastData)
	}
	if !strings.Contains(srv.lastData, "My Show") {
		t.Errorf("body missing stream title:\n%s", srv.lastData)
	}
	if !strings.Contains(srv.lastData, "Subject:") {
		t.Errorf("message missing Subject header:\n%s", srv.lastData)
	}
}

func TestSMTPMailer_CarriesPurgeNotice(t *testing.T) {
	srv := newFakeSMTPServer(t)
	_, port, _ := net.SplitHostPort(srv.addr)
	m := NewSMTPMailer("127.0.0.1", port, "u", "p", "from@gp.example")
	m.serverAddr = srv.addr

	if err := m.SendInvite(context.Background(), Invite{
		To: "g@example.com", StreamTitle: "Show", MagicLink: "https://gp.example/p/tok",
	}); err != nil {
		t.Fatalf("SendInvite: %v", err)
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if !strings.Contains(srv.lastData, "deleted within 24 hours") {
		t.Errorf("SMTP message missing 24h purge notice (AC-6):\n%s", srv.lastData)
	}
}
