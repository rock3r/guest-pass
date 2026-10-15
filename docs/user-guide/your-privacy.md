# Your privacy & data

You can join GuestPass with an invitation. You do not need a guest account, password, or profile.

## What's kept

- GuestPass stores your **invitation name and email** so the host can invite you.
- GuestPass stores **session and peer records**, such as connection times. The invitation-data purge does not delete those records.
- If you report an invite, GuestPass stores your email and report message for the instance admins to review. It clears those fields after the configured review period, which defaults to 30 days. It keeps the host, category, and time as an anonymous report record.

Check your instance's privacy policy for its retention settings and how to contact its operator about retained records.

## What's never kept

- **GuestPass does not record your camera, microphone, or screen.** Browsers exchange encrypted media. A configured TURN relay can forward encrypted packets. The GuestPass application server does not receive media.
- **GuestPass does not save backstage chat.** The server relays messages over encrypted connections without saving them to disk or logs. Chat has no end-to-end encryption. Do not send secrets.
- **GuestPass has no third-party trackers or ads.**

Participants can record media or copy chat on their own devices. A host can record or broadcast media through OBS.

## Invitation data deletion

Under the default retention settings, GuestPass deletes your invitation name and email **within 24 hours** of the stream ending. Session records and abuse reports have separate retention rules. Self-hosted instances can change retention settings; check the instance's privacy policy.

Hosts have account export and deletion controls. Guests have no account or self-service deletion page.

## If something feels off

If you received an unwanted invite, see [Reporting an invite](reporting-an-invite). Opening the invite page alone does not start capture. The device check starts local capture after you allow camera or microphone access. Entering the greenroom sends media to other participants before your source appears in the broadcast.
