# The greenroom

The greenroom lets you and your invited guests prepare before and during a broadcast. Participants receive each other's media. You control which sources appear in the broadcast through OBS.

## What you see

Every guest who has opened their pass and allowed their camera shows up as a tile. Each tile carries their name, a connection-health signal, and an on-air indicator, plus moderation controls scaled to your role.

Use **Set up camera and microphone** to open your local preview and publish to the **Host** OBS source. Opening the greenroom alone does not request device permission. Your local preview is muted to prevent feedback. Each tile has a microphone activity meter. A zero meter can mean silence, unavailable audio analysis, or no audio track. Closing capture clears the Host source.

**OBS state unknown** means the browser source has not yet reported whether it is active in OBS. It is not a claim that the guest is off air; check the source or the linked stream until OBS reports a state.

## Backstage chat

Use text chat to coordinate with guests.

The sidebar opens on **Chat**. Switch to **People** whenever you need participant controls, OBS slot assignment, or stream-quality controls.

- GuestPass does not save chat to disk or logs. Participants can still copy or record messages.
- The server relays chat over encrypted connections. Chat has no end-to-end encryption. Do not send secrets in chat.

## Assigning guests to slots

This is the bridge between a guest and your OBS scene. Pick a guest, choose a [slot](wiring-obs-sources) (`cam-1`…`cam-8`), and their camera starts flowing to the matching Browser source you already wired. Re-bind at any time — swap two guests, move someone to a different slot, or clear a slot — and OBS follows along.

A slot holds one guest at a time. Assign before you go live and the binding is ready the moment the session starts.

## Moderation

From any tile you can, within your rank:

- **Mute or hide** a guest's mic or camera — enforced on the receiving side, so it sticks even if the guest's browser misbehaves.
- **Remove** a guest from the room.
- **Promote or demote** a co-host (host only).

## Quality controls

While a session is live, the greenroom exposes the **program quality ceiling** — the maximum resolution, frame rate, and bitrate any guest source will send. Lower it if a guest's connection is struggling and dragging down the broadcast.

GuestPass lowers video quality when a guest's connection or CPU cannot keep up. It restores quality gradually when conditions improve. **Bump quality now** requests immediate recovery up to the stream's configured quality limits. GuestPass lowers quality again if the problem continues. The control is available during a live session when at least one guest has reduced quality.

## Reconnects

If your own connection drops, the greenroom reconnects on its own and your live session keeps running server-side — guests stay on the air through the gap.
