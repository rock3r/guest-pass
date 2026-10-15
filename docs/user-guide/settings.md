# Settings & your data

Your **Settings** page covers your account, the controls for your own data, and defaults used when you create a stream.

## Account

You sign in with Google, so your **email and sign-in are managed there** and shown read-only. You can edit your **display name** — the name shown to guests and co-hosts — any time.

## Appearance

GuestPass follows your operating system's light/dark preference by default. The theme toggle (in the sidebar, or the top bar on pages without one) lets you pin **light** or **dark**, or go back to **system**.

## Your data

GuestPass stores your Google identity, streams, guest invitations, and operational session records. It also stores abuse reports under separate retention rules. GuestPass does not record media or save backstage chat. Participants can still record or copy what they receive.

- **Export** — download everything we hold about you (account, streams, invites) as a single JSON file.
- **Delete account** — permanently removes your account and all its data. Two safeguards apply: you can't delete while a stream is **live** (end it first), and if you're the **only admin** on the instance you'll need to promote someone else first, so the instance isn't left without one.
- **Invitation data is auto-purged.** Under the default retention settings, GuestPass deletes invitation names and emails **within 24 hours** of stream end. Session records and abuse reports have separate retention rules.

## Stream defaults

Set an IANA **timezone** once and GuestPass interprets new stream times in that timezone while storing the exact UTC instant. The editor and calendar use that same timezone, so a late-night show stays on the date you chose. You can still edit a stream later.

Add your YouTube and Twitch channel names and choose one as the default live-check link for new streams. GuestPass never posts to either service. A specific stream can still use a different channel or none.

Choose default limits for resolution, frame rate, and bitrate. GuestPass copies them to new streams. The host can adjust a live stream's limits in the [greenroom](the-greenroom). GuestPass negotiates H.264 where available, with VP8 fallback.

## Bring your own TURN

Most shows use direct peer-to-peer connections with STUN alone. If your guests regularly use restrictive networks, enable **Bring your own TURN**, provide your relay URL and its coturn shared secret, and save. Use a standard ICE URL such as `turn:turn.example.com:3478` or `turns:turn.example.com:5349?transport=tcp`. GuestPass stores the secret encrypted and sends browsers only short-lived relay credentials. Leave the secret blank when editing to keep the saved value.
