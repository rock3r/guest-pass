package livecheck

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

// isBlockedIP must reject every private/loopback/link-local/metadata/CGNAT/etc. range and accept
// ordinary public IPs (§7.4 / RF-9), including IPv4-mapped-IPv6 forms of blocked addresses.
func TestIsBlockedIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.1.2.3", // loopback
		"10.0.0.1", "172.16.5.4", "192.168.1.1", // RFC1918
		"169.254.169.254", "169.254.0.1", // link-local incl. AWS/GCP metadata
		"100.64.0.1", "100.100.100.200", // CGNAT (RFC6598) incl. Alibaba metadata
		"0.0.0.0",             // unspecified
		"224.0.0.1",           // multicast
		"192.0.2.5",           // TEST-NET-1
		"198.18.0.9",          // benchmarking
		"::1",                 // IPv6 loopback
		"fe80::1",             // IPv6 link-local
		"fc00::1", "fd12::34", // IPv6 ULA (private)
		"::ffff:169.254.169.254", // IPv4-mapped metadata — must be caught as v4 link-local
		"::ffff:127.0.0.1",       // IPv4-mapped loopback
		"::",                     // unspecified v6
	}
	for _, s := range blocked {
		ip, err := netip.ParseAddr(s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		if !isBlockedIP(ip) {
			t.Errorf("isBlockedIP(%s) = false, want blocked", s)
		}
	}

	allowed := []string{"1.1.1.1", "8.8.8.8", "151.101.0.1", "2606:4700:4700::1111"}
	for _, s := range allowed {
		ip := netip.MustParseAddr(s)
		if isBlockedIP(ip) {
			t.Errorf("isBlockedIP(%s) = true, want allowed (public)", s)
		}
	}
	if !isBlockedIP(netip.Addr{}) {
		t.Error("an invalid/zero Addr must fail closed (blocked)")
	}
}

// safeControl validates the resolved dial IP — blocking private/loopback targets and accepting
// public ones. This is what closes DNS rebinding (RF-9): the pin is STRUCTURAL — Control runs
// post-resolution on the actual ip:port being dialed, so there is no check-time-resolve →
// dial-time-resolve gap for a second DNS answer to exploit (the IP checked here IS the IP
// connected to). Likewise, URL-host encoding evasions (decimal/octal/hex IPv4, bracketed IPv6)
// can't reach Control: the fetch host is the fixed twitch.tv template and the channel id is a
// sanitized path segment, so Control only ever receives a canonical literal IP from the resolver.
func TestSafeControl(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:443", "169.254.169.254:80", "10.0.0.1:443", "[::1]:443"} {
		if err := safeControl("tcp", addr, nil); !errors.Is(err, errBlockedAddress) {
			t.Errorf("safeControl(%q) = %v, want errBlockedAddress", addr, err)
		}
	}
	if err := safeControl("tcp", "1.1.1.1:443", nil); err != nil {
		t.Errorf("safeControl(public) = %v, want nil", err)
	}
	if err := safeControl("udp", "1.1.1.1:443", nil); !errors.Is(err, errBlockedAddress) {
		t.Errorf("safeControl(non-tcp) = %v, want blocked", err)
	}
}

func TestAllowedRedirectHost(t *testing.T) {
	cases := []struct {
		orig, target string
		want         bool
	}{
		{"www.twitch.tv", "twitch.tv", true},           // www → bare domain
		{"www.twitch.tv", "m.twitch.tv", true},         // subdomain on the same registrable domain
		{"www.twitch.tv", "www.twitch.tv", true},       // same host
		{"www.twitch.tv", "evil.com", false},           // off-domain
		{"www.twitch.tv", "twitch.tv.evil.com", false}, // suffix trick
		{"www.twitch.tv", "eviltwitch.tv", false},      // no dot boundary
		{"gql.twitch.tv", "passport.twitch.tv", true},
	}
	for _, c := range cases {
		if got := allowedRedirectHost(c.orig, c.target); got != c.want {
			t.Errorf("allowedRedirectHost(%q,%q) = %v, want %v", c.orig, c.target, got, c.want)
		}
	}
}

// The safe client actually wires the guard into dials: a GET to a loopback (httptest) server is
// refused at connect time — proving prod verifiers can't be pointed at internal addresses even by a
// DNS answer or redirect that resolves to loopback.
func TestSafeClient_BlocksLoopbackDial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	_, err := newSafeClient().Do(req)
	if err == nil {
		t.Fatal("safe client connected to a loopback server (SSRF guard not wired into dials)")
	}
	if !strings.Contains(err.Error(), "blocked address") {
		t.Fatalf("loopback dial error = %v, want a blocked-address error", err)
	}
}
