package livecheck

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"time"
)

// SSRF guard (§7.4 / RF-9). Live-verify fetches a public platform page on behalf of a host, so it
// is a textbook SSRF surface. It is closed by construction:
//
//   - The fetch host is a FIXED platform domain (never host-supplied) — the channel identifier is
//     only ever a sanitized path component (see twitch.go), so URL-host encoding evasions
//     (decimal/octal/hex IPv4, IPv6 brackets) can't reach the dialer.
//   - A validating dialer Control hook checks the ACTUAL resolved IP right before connect, on every
//     dial including redirects — so DNS rebinding (resolve→dial TOCTOU) is closed: the IP checked is
//     the IP dialed, and a blocked IP is refused.
//   - Redirects are http(s)-only, capped, and refused off the original registrable domain.
//   - Tight timeout + a response-size cap bound the work; no ambient proxy is consulted.

var (
	// errBlockedAddress means a dial targeted a private/loopback/link-local/metadata/etc. IP.
	errBlockedAddress = errors.New("livecheck: blocked address (SSRF guard)")
	// errOffDomainRedirect means a redirect pointed off the original registrable domain.
	errOffDomainRedirect = errors.New("livecheck: off-domain redirect refused")
	// errBadRedirect means a redirect used a non-http(s) scheme or exceeded the hop limit.
	errBadRedirect = errors.New("livecheck: disallowed redirect")
)

const (
	fetchTimeout = 10 * time.Second // total request budget (connect + read), best-effort + polite
	dialTimeout  = 5 * time.Second
	maxRedirects = 3
)

// extraBlockedPrefixes covers ranges the netip.Addr helpers don't classify but which an SSRF must
// still never reach: CGNAT (RFC6598 — used by some cloud metadata, e.g. 100.100.100.200) and the
// IETF special-purpose / documentation ranges.
var extraBlockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),   // CGNAT (RFC6598)
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // TEST-NET-1
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // TEST-NET-2
	netip.MustParsePrefix("203.0.113.0/24"),  // TEST-NET-3
	netip.MustParsePrefix("64:ff9b::/96"),    // NAT64 (could map to a blocked v4)
}

// isBlockedIP reports whether ip is in a range the live-verify fetcher must never connect to (the
// §7.4 deny-list). IPv4-mapped IPv6 (::ffff:a.b.c.d) is unmapped first so it is classified as the
// IPv4 address it is — closing the ::ffff:169.254.169.254 metadata evasion (RF-9).
func isBlockedIP(ip netip.Addr) bool {
	if !ip.IsValid() {
		return true // unparseable → fail closed
	}
	ip = ip.Unmap()                         // ::ffff:a.b.c.d → a.b.c.d
	if ip.IsLoopback() || ip.IsPrivate() || // private = RFC1918 + ULA fc00::/7
		ip.IsLinkLocalUnicast() || // 169.254.0.0/16 (incl. 169.254.169.254 cloud metadata) + fe80::/10
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, p := range extraBlockedPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// safeControl is the net.Dialer Control hook: it runs after DNS resolution, right before connect,
// with the actual resolved "ip:port" — so it validates the IP being dialed (closing DNS rebinding)
// and refuses a blocked one. It also rejects a non-TCP network.
func safeControl(network, address string, _ syscall.RawConn) error {
	if network != "tcp4" && network != "tcp6" && network != "tcp" {
		return fmt.Errorf("%w: network %s", errBlockedAddress, network)
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: %v", errBlockedAddress, err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("%w: %q is not a literal IP", errBlockedAddress, host) // Control gets a resolved IP
	}
	if isBlockedIP(ip) {
		return fmt.Errorf("%w: %s", errBlockedAddress, ip)
	}
	return nil
}

// registrableDomain returns the last two dot-labels of host (e.g. www.twitch.tv → twitch.tv). This
// is a deliberately simple heuristic that is correct for the fixed v1 platform domains (twitch.tv,
// youtube.com — both eTLD+1 = two labels); a multi-part-TLD platform would need a public-suffix
// list, but none is in scope (DEF-4).
func registrableDomain(host string) string {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	labels := strings.Split(host, ".")
	if len(labels) <= 2 {
		return host
	}
	return strings.Join(labels[len(labels)-2:], ".")
}

// allowedRedirectHost reports whether a redirect from origHost to targetHost stays on the original
// registrable domain (same host, the bare domain, or a subdomain of it) — so www.twitch.tv may
// redirect to twitch.tv / m.twitch.tv but never to evil.com or twitch.tv.evil.com.
func allowedRedirectHost(origHost, targetHost string) bool {
	origHost, targetHost = strings.ToLower(origHost), strings.ToLower(targetHost)
	if targetHost == origHost {
		return true
	}
	dom := registrableDomain(origHost)
	return targetHost == dom || strings.HasSuffix(targetHost, "."+dom)
}

// newSafeClient builds the SSRF-closed HTTP client used by every verifier: a validating dialer
// (safeControl), redirects that are http(s)-only / hop-capped / on-domain only, a total timeout,
// and no proxy from the environment (a successful SSRF can't be relayed through an attacker proxy).
func newSafeClient() *http.Client {
	dialer := &net.Dialer{Timeout: dialTimeout, Control: safeControl}
	tr := &http.Transport{
		DialContext:           dialer.DialContext,
		Proxy:                 nil, // never consult HTTP(S)_PROXY
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   dialTimeout,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   fetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("%w: too many hops", errBadRedirect)
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("%w: scheme %q", errBadRedirect, req.URL.Scheme)
			}
			if !allowedRedirectHost(via[0].URL.Hostname(), req.URL.Hostname()) {
				return fmt.Errorf("%w: %s → %s", errOffDomainRedirect, via[0].URL.Hostname(), req.URL.Hostname())
			}
			return nil // the dialer's Control still re-checks the redirect target's IP
		},
	}
}
