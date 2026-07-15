# Settings & your data

Your **Settings** page covers your account, the controls for your own data, and defaults used when you create a stream.

## Account

You sign in with Google, so your **email and sign-in are managed there** and shown read-only. You can edit your **display name** — the name shown to guests and co-hosts — any time.

## Appearance

GuestPass follows your operating system's light/dark preference by default. The theme toggle (in the sidebar, or the top bar on pages without one) lets you pin **light** or **dark**, or go back to **system**.

## Your data

GuestPass keeps the bare minimum: your Google identity, the streams you create, and the name + email of each guest you invite. No media is ever stored, and backstage chat is never recorded.

- **Export** — download everything we hold about you (account, streams, invites) as a single JSON file.
- **Delete account** — permanently removes your account and all its data. Two safeguards apply: you can't delete while a stream is **live** (end it first), and if you're the **only admin** on the instance you'll need to promote someone else first, so the instance isn't left without one.
- **Guest data is auto-purged.** A guest's name and email are deleted **within 24 hours** of a stream ending — automatically, with no action from you.

## Stream defaults

Set an IANA **timezone** once and GuestPass interprets new stream times in that timezone while storing the exact UTC instant. The editor and calendar use that same timezone, so a late-night show stays on the date you chose. You can still edit a stream later.

Add your YouTube and Twitch channel names and choose one as the default live-check link for new streams. GuestPass never posts to either service. A specific stream can still use a different channel or none.

Choose a default program-quality ceiling (resolution, frame rate, and bitrate). It is copied to new streams; the host can still adjust an individual live stream in the [greenroom](the-greenroom). GuestPass keeps codec negotiation automatic — H.264 where available, with VP8 fallback — rather than exposing an unreliable forced-codec switch.

## Bring your own TURN

Most shows use direct peer-to-peer connections with STUN alone. If your guests regularly use restrictive networks, enable **Bring your own TURN**, provide your relay URL and its coturn shared secret, and save. Use a standard ICE URL such as `turn:turn.example.com:3478` or `turns:turn.example.com:5349?transport=tcp`. GuestPass stores the secret encrypted and sends browsers only short-lived relay credentials. Leave the secret blank when editing to keep the saved value.
