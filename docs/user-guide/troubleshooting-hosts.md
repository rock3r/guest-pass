# Troubleshooting

Check OBS settings, device permissions, and network connections when a guest's media is missing or unstable.

## My guest's audio is missing from the broadcast

In the OBS Browser source properties, enable **"Control audio via OBS"**. Check the source's level, mute state, and output routing in the **Audio Mixer**. Check that the guest is unmuted in the [greenroom](the-greenroom). See [Wiring OBS sources](wiring-obs-sources).

## My OBS source is blank or shows a placeholder

A slot source only shows video when a guest is **bound to that slot and connected**. Check, in order:

1. The guest has opened their pass and allowed their camera (they appear in the greenroom).
2. You've **assigned that guest to that slot** in the [greenroom](the-greenroom) — the placeholder means the slot is empty or its guest is offline.
3. The Browser source URL matches the slot and is current (see the next item).

## I regenerated a URL and now that source is black

Regenerating **rotates the token** — the old URL stops working immediately. Copy the fresh URL from the **OBS & sources** tab and paste it into the Browser source. (If you hit "Regenerate all," every source needs its new URL.)

## A guest can't connect at all

Ask the guest to open the link in **desktop Chrome** and allow camera and microphone access. If their network blocks media, ask them to connect their computer to another network. You can also configure a **TURN relay** in Settings or ask your instance operator for one. See [Connections & networking](connections-and-networking).

## A guest's video is frozen, pixelated, or stuttering

The guest's connection or CPU may lack capacity. GuestPass lowers quality automatically and restores it when conditions improve. Lower the quality ceiling, or use **Bump quality now** to request recovery up to the configured limits. Ask the guest to use Ethernet and close other calls or downloads. See [Performance & bandwidth](performance-and-bandwidth).

## I can't go live — it says I'm already live

You can have **one live session at a time**. End the other stream's session first, then start this one.

## The greenroom said "reconnecting"

Your own connection blipped. The greenroom reconnects on its own and your live session keeps running server-side — guests stay on the air through the gap. If it can't recover after several tries, reload the page.

## A guest got an "unexpected invite" warning, or their link expired

Re-issue the pass from the **Invites** tab — it mints a fresh link and the old one stops working. See [Inviting guests](inviting-guests). A re-issued pass starts unbound, so re-assign its slot in the greenroom.

## Still stuck?

If something looks like an actual bug rather than a setting, your instance's source link (in the footer of every page) points to the repository where you can file an issue.
