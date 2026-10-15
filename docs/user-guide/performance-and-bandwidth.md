# Performance & bandwidth

GuestPass sends encrypted media between browsers. Each participant needs enough upload bandwidth and processing capacity for their outgoing media connections.

## How the mesh works

Each participant sends their camera to other greenroom participants and to their assigned OBS sources. Each additional receiver needs another outgoing copy of the video.

Bandwidth use grows with **guest count**. Check each participant's **upload** speed when planning the stream.

## Rough guide by guest count

At 720p30 (a sensible default), plan for roughly **1–2 Mbps of upload per outgoing copy** of a stream. Real numbers vary with motion and codec, but as a planning aid:

| Guests on at once | Experience | Notes |
| --- | --- | --- |
| 1–3 | Lower load | Test each guest's upload before the show. |
| 4–6 | Moderate load | Use stable connections; cap resolution if video stutters. |
| 7–8 | Demanding | Everyone needs strong upload; lower the ceiling and trim simultaneous cameras. |

GuestPass gives you 8 camera slots. Test an eight-camera session on the devices and networks you plan to use. Slot capacity alone does not guarantee enough upload bandwidth.

## Keep it smooth

- **Set a quality ceiling.** In the [greenroom](the-greenroom), cap resolution, frame rate, and bitrate for the whole session. 720p30 looks great in a stream layout and costs far less than 1080p60.
- **Per-source caps.** A slot URL takes a `?res=` hint (e.g. `?res=540`) to hold one source lower — useful for a small picture-in-picture guest.
- **Don't show everyone at once.** Bring guests in and out of your OBS scene as they speak. A guest who isn't in any source costs less.
- **Use Ethernet when available.** An unstable Wi-Fi connection can cause video to freeze or stutter.
- **Close other calls and downloads.** Video calls and downloads use bandwidth. Screen recorders also use processing capacity.

## When quality drops

GuestPass lowers video quality when a guest's CPU or network cannot keep up. It restores quality gradually when conditions improve. Their tile shows a degrading or recovering hint. **Bump quality now** requests immediate recovery up to the stream's configured quality limits. Quality drops again if the problem continues.

If no attempted media connection succeeds, GuestPass shows a network warning. A **TURN relay** can forward encrypted media when direct connections fail. Relaying can increase latency. See [Connections & networking](connections-and-networking) for relay setup and other network options.
