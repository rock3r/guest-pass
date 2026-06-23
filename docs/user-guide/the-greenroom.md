# The greenroom

The greenroom is your private, invite-only control room — the place you and your guests meet *before* and *during* the broadcast. Guests see each other's cameras here; your audience does not. Nothing in the greenroom goes to air until you put it there.

## What you see

Every guest who has opened their pass and allowed their camera shows up as a tile. Each tile carries their name, a connection-health signal, and an on-air indicator, plus moderation controls scaled to your role.

## Backstage chat

The greenroom has a text chat for quick coordination — "you're muted," "go to you next," that sort of thing. Two things to be clear about, so you can set expectations with guests:

- **It's ephemeral.** Chat is never written to disk or logged — there's no transcript, and it's gone when the room closes. Everyone can talk freely off the air.
- **It's relayed, not peer-to-peer.** Your guests' camera and mic travel **directly between browsers** and never touch our server. Chat is different: it passes **through the server** to fan out to everyone, so while it's encrypted in transit it is **not end-to-end encrypted**. Treat it as off-the-record coordination, not a secure channel for secrets.

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

GuestPass also adapts on its own: when a guest's connection or CPU is under strain, it quietly steps their video quality down and eases it back up slowly so it doesn't flap. If you'd rather not wait for that gradual recovery, **Bump quality now** asks every guest's browser to jump straight back to full quality. It's available only while you're live, and only does something when a guest is actually degraded — at full quality there's nothing to bump.

## Reconnects

If your own connection drops, the greenroom reconnects on its own and your live session keeps running server-side — guests stay on the air through the gap.
