# Troubleshooting

The fixes for the problems hosts actually hit. Most of them are a wrong OBS setting or a guest's network — rarely anything deeper.

## My guest's camera isn't audible in the stream

The single most common one. In the OBS Browser source properties, enable **"Control audio via OBS"** — without it the guest's mic plays on your machine but never reaches the broadcast. Then check the source's channel in the **Audio Mixer** isn't muted or pulled to zero, and that you haven't muted the guest in the [greenroom](the-greenroom). See [Wiring OBS sources](wiring-obs-sources).

## My OBS source is blank or shows a placeholder

A slot source only shows video when a guest is **bound to that slot and connected**. Check, in order:

1. The guest has opened their pass and allowed their camera (they appear in the greenroom).
2. You've **assigned that guest to that slot** in the [greenroom](the-greenroom) — the placeholder means the slot is empty or its guest is offline.
3. The Browser source URL matches the slot and is current (see the next item).

## I regenerated a URL and now that source is black

Regenerating **rotates the token** — the old URL stops working immediately. Copy the fresh URL from the **OBS & sources** tab and paste it into the Browser source. (If you hit "Regenerate all," every source needs its new URL.)

## A guest can't connect at all

Almost always their network. Have them open the link in a **real browser** (not an in-app webview), allow camera/mic, and if a corporate or campus firewall is in the way, try a **phone hotspot** — that fixes most cases on the spot. The lasting fix is their IT allowing WebRTC, or a **TURN relay** (which you can enable if you run the instance, or request if someone else does). See [Connections & networking](connections-and-networking).

## A guest's video is frozen, pixelated, or stuttering

That's their connection or CPU under strain. GuestPass already steps their quality down and back up automatically. You can help by **lowering the program quality ceiling** in the greenroom, hitting **Bump quality now** once the strain passes, or asking the guest to go wired and close other apps. See [Performance & bandwidth](performance-and-bandwidth).

## I can't go live — it says I'm already live

You can have **one live session at a time**. End the other stream's session first, then start this one.

## The greenroom said "reconnecting"

Your own connection blipped. The greenroom reconnects on its own and your live session keeps running server-side — guests stay on the air through the gap. If it can't recover after several tries, reload the page.

## A guest got an "unexpected invite" warning, or their link expired

Re-issue the pass from the **Invites** tab — it mints a fresh link and the old one stops working. See [Inviting guests](inviting-guests). A re-issued pass starts unbound, so re-assign its slot in the greenroom.

## Still stuck?

If something looks like an actual bug rather than a setting, your instance's source link (in the footer of every page) points to the repository where you can file an issue.
