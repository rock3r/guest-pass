# Connections & networking

GuestPass uses separate connections for room coordination and media. A guest can reach the room while a firewall blocks their media connection.

## Two separate channels

A GuestPass session uses two very different connections:

- **Signaling** — a small, constant connection between each browser and the GuestPass server. It only carries coordination: who's in the room, who's bound to which slot, chat. It runs over a secure WebSocket.
- **Media** — the actual camera, microphone, and screen. This travels **directly between browsers** (peer-to-peer), end-to-end encrypted with DTLS-SRTP. Our server is never in the media path and never sees a frame.

So "is GuestPass up?" and "can two guests see each other?" are different questions — the second depends on the network *between those two people*.

## Finding a path: STUN

Most people are behind a home router that hides them behind a single public address (NAT). To set up a direct connection, each browser needs to learn how it looks from the outside. That's what a **STUN** server does — it's a quick "what's my public address?" lookup. No media flows through it.

On a typical home or office connection, STUN is all that's needed and the peer-to-peer link forms in a second or two.

## When the direct path is blocked

Some networks won't allow a direct peer-to-peer link at all — most often:

- **Corporate or campus networks** with strict firewalls that block the UDP traffic WebRTC prefers.
- **Locked-down guest Wi-Fi** (hotels, conferences) that isolates clients from each other.
- Aggressive VPNs.

If no attempted media connection succeeds, GuestPass shows a network warning. Try another network or ask the host about a relay.

## TURN: the relay fallback

For those cases there's **TURN** — a relay server that sits in the middle and forwards the media when a direct path is impossible. Two things to know:

- The relay forwards encrypted packets without decrypting them.
- Relaying uses server bandwidth and can increase latency. GuestPass tries direct connections first.

**GuestPass is STUN-only out of the box** — no relay, nothing extra to run, which keeps the cost near zero. What to do when a guest actually needs a relay depends on your situation:

- **On the public `guest-pass.link` instance.** There is no shared TURN relay. A host can configure a coturn-compatible relay under **Settings → Connection** with a `turn:host:port` or `turns:host:port` URL. Without a relay, ask a blocked guest to connect their computer to a less restricted network, such as a phone hotspot.
- **On a team or company instance someone else runs.** Ask whoever runs it — they can enable a relay server-side for everyone on that instance.
- **If you self-host.** Configure the bundled relay or a third-party relay. **TLS on port 443** can help on restrictive networks, but some firewalls still block it. See `docs/DEPLOYMENT.md`.

## Supported browsers

GuestPass v1 supports **desktop Chrome** for guests and OBS browser sources for broadcast output. Mobile devices and other guest browsers are untested and unsupported in v1. They may work, but use desktop Chrome for a supported setup.

One thing to actively avoid: **in-app browsers** (opening the link inside another app's built-in webview, like some chat apps), which often can't get camera or mic access. "Open this in Chrome" fixes a surprising number of problems.

## Helping a guest who can't connect

A quick checklist to hand a struggling guest:

1. **Open the link in desktop Chrome**, outside any app's built-in browser.
2. **Allow camera and mic** when prompted — check the address-bar permission icon if you dismissed it.
3. **Try a different network**, such as connecting your computer to a phone hotspot. The new network may allow the connection.
4. If it's a recurring problem on a managed network, the lasting fix is their **IT allowing WebRTC/UDP**, or a **TURN relay** — see the section above for who can switch that on for your instance.
