package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
