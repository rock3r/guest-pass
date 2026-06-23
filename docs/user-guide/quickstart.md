# Quickstart

Get a guest on your stream in about a minute. No accounts for them, no installs, no fiddling with codecs.

## What you need

- An [OBS](https://obsproject.com/)-style broadcaster that supports **Browser sources** (OBS Studio, Streamlabs, vMix, and most others).
- A Google account to sign in as the host. Your guests need **nothing** — just a browser and a camera.

## 1. Sign in and create a stream

Sign in at **/signin**, then hit **New stream** on your dashboard. Give it a name and, if you know it, a date and time. You can change all of this later — a stream is just a room with a set of source URLs attached.

You don't have to schedule anything to start testing. A draft stream works fine for a dry run.

## 2. Invite your guests

Open the stream's **Invites** tab and add a guest by name and email. Each one gets a **magic link** that signs them straight in — no password, no app, no account. Links expire when you say, and you can revoke or re-issue them at any time.

Tell your guest to open the link, allow their camera and mic, and wait in the greenroom. That's the whole guest experience.

## 3. Wire OBS once

Open the **OBS & sources** tab. You'll see a fixed set of slots — `cam-1` through `cam-8` plus a screen-share slot — each with its own URL. Add a **Browser source** in OBS for each slot you'll use and paste its URL.

The URLs are keyed by **slot, not guest**, so you wire OBS once and reuse the same scene every stream. See [Wiring OBS sources](wiring-obs-sources) for the exact settings.

## 4. Go live in the greenroom

The [greenroom](the-greenroom) is your private control room. Guests appear as they join. Soundcheck, assign each guest to a slot, and when you're ready, the matching OBS Browser source lights up with their camera. Kick, mute, or re-bind anyone mid-stream.

That's it. Media flows **browser-to-browser** — our server never sees it.
