# Inviting guests

Guests join through a **pass** — a personal magic link that signs them straight in. No account, no password, no app on their end.

## Sending a pass

Open a stream's **Invites** tab, enter a guest's **name** and **email**, and send. GuestPass emails them the link and shows it to you **once** so you can copy and share it another way if you prefer. Each link is unique to that guest — they shouldn't forward it.

## Roles: guest vs co-host

Every pass has a role, and you can flip it any time from the Invites list:

- **Guest** — appears on camera, can be put on screen, chats backstage.
- **Co-host** — everything a guest can do, plus moderation: they can mute, hide, or remove other guests in the greenroom. Give this to a trusted co-presenter.

## Expiry, revoke, and re-issue

- **Expiry** — passes can expire so an old link can't be reused later.
- **Revoke** — turns a pass off immediately; the guest's link starts showing "link turned off." Use this if a link leaked or plans changed.
- **Re-issue** — rotates the link's token. The old link stops working and a fresh one is generated — handy if a guest lost theirs or you revoked it by mistake. A re-issued pass starts unbound, so re-assign its [slot](wiring-obs-sources) in the greenroom if needed.

## Screen sharing

Screen sharing is **permissioned and off by default**. A guest can only share their screen once you allow it, and you can revoke that mid-stream. Manage it from the [greenroom](the-greenroom) while the session is live.

## Slot binding

A pass decides *who* a guest is; a [slot](wiring-obs-sources) decides *where they appear in OBS*. Assign a guest to a slot in the [greenroom](the-greenroom) — you can do it ahead of time or live, and re-bind whenever you like.

## A note on limits

To keep the free public instance healthy, brand-new accounts start with a modest cap on invites and concurrent streams that **grows as your account ages**. If you hit a limit, it'll lift on its own over time — or, on a self-hosted instance, the operator can tune it. See [Settings & your data](settings) for what we keep about your guests (short version: their name and email, deleted within 24 hours of a stream ending).
