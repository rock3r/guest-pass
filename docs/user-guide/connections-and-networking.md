# Connections & networking

You don't need to be a network engineer to run a show — most guests "just connect." But when someone *can't*, it's almost always their network, and knowing the shape of how GuestPass connects helps you fix it fast. Here's the high-level picture.

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

When this happens the affected guest gets a **clear heads-up** rather than a silent hang, so you know it's a network issue and not a bug.

## TURN: the relay fallback

For those cases there's **TURN** — a relay server that sits in the middle and forwards the media when a direct path is impossible. Two things to know:

- It still **can't read your media** — it forwards the same encrypted packets, it just can't decrypt them. Privacy holds; you only lose the "direct" part.
- It costs an extra hop (a little more latency) and bandwidth on the relay, which is why it's a fallback, not the default.

**GuestPass is STUN-only out of the box** — no relay, nothing extra to run, which keeps the cost near zero. What to do when a guest actually needs a relay depends on your situation:

- **On the public `guest-pass.link` instance.** There's no shared TURN relay, and the instance won't add one — relaying everyone's media is exactly the cost the public instance avoids to stay free. So **today**, a blocked guest's fix is their **network**: a phone hotspot or any less-restricted connection almost always connects. **Longer term, the answer is your own relay** — a host attaches their own TURN credentials to their account (look for **Settings → Connection**), so *you* cover *your* guests without the instance relaying for everybody. That's the intended path; it's on the roadmap rather than live today.
- **On a team or company instance someone else runs.** Ask whoever runs it — they can enable a relay server-side for everyone on that instance.
- **If you self-host.** You're in control: point GuestPass at the bundled relay or a bring-your-own / third-party one, ideally over **TLS on port 443** so it sails through corporate firewalls. The settings are in your copy of `docs/DEPLOYMENT.md`.

## Supported browsers

GuestPass is built on standard WebRTC, so it *should* run on any current major browser with no extension and no app. Being honest about what we actually test:

- **Chrome on desktop is what we actively test.** For an important show, that's the safe choice — for you and your guests alike.
- **Other modern desktop browsers** — Edge and other Chromium browsers, Firefox, Safari — run on the same standards and should work fine, but we don't verify every release. If something misbehaves, switching to Chrome is the first thing to try.
- **Mobile works, but isn't optimised yet.** We haven't done any mobile-specific tuning — layouts and camera handling are built for desktop — so **your mileage may vary** on a phone or tablet. It's fine for a guest in a pinch; we wouldn't run the host side from one.

One thing to actively avoid: **in-app browsers** (opening the link inside another app's built-in webview, like some chat apps), which often can't get camera or mic access. "Open this in Chrome" fixes a surprising number of problems.

## Helping a guest who can't connect

A quick checklist to hand a struggling guest:

1. **Open the link in a real browser** (not an in-app webview), recent Chrome/Edge/Firefox/Safari.
2. **Allow camera and mic** when prompted — check the address-bar permission icon if you dismissed it.
3. **Try a different network** — a phone hotspot famously dodges restrictive office/campus firewalls.
4. If it's a recurring problem on a managed network, the lasting fix is their **IT allowing WebRTC/UDP**, or a **TURN relay** — see the section above for who can switch that on for your instance.
