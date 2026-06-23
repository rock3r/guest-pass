# Performance & bandwidth

GuestPass sends media **peer-to-peer** — browser to browser, never through our server. That's what makes it cheap to run and private. It also means the limits live on *your* and your *guests'* connections, not on a server you're paying for. Here's how to stay smooth.

## How the mesh works

There's no media server mixing everyone together. Instead, each person's camera is sent **directly to the people who need to see it** — the others backstage in the greenroom, and your OBS source for that guest. The flip side: the more participants, the more copies of their video each person's connection has to upload.

So bandwidth scales with **guest count**, and the limiting factor is usually a participant's **upload** speed, not download.

## Rough guide by guest count

At 720p30 (a sensible default), plan for roughly **1–2 Mbps of upload per outgoing copy** of a stream. Real numbers vary with motion and codec, but as a planning aid:

| Guests on at once | Experience | Notes |
| --- | --- | --- |
| 1–3 | Effortless | Works comfortably on typical home broadband. |
| 4–6 | Good | Fine on solid connections; cap resolution if anyone wobbles. |
| 7–8 | Demanding | Everyone needs strong upload; lower the ceiling and trim simultaneous cameras. |

GuestPass gives you 8 camera slots. Eight people *all on camera at once* is a genuine workout for everyone's uplink — totally doable on good connections, but it's the scenario to plan for.

## Keep it smooth

- **Set a quality ceiling.** In the [greenroom](the-greenroom), cap resolution, frame rate, and bitrate for the whole session. 720p30 looks great in a stream layout and costs far less than 1080p60.
- **Per-source caps.** A slot URL takes a `?res=` hint (e.g. `?res=540`) to hold one source lower — useful for a small picture-in-picture guest.
- **Don't show everyone at once.** Bring guests in and out of your OBS scene as they speak. A guest who isn't in any source costs less.
- **Wired beats Wi-Fi.** Ask key guests to plug in if they can; Wi-Fi is the most common cause of a wobbly tile.
- **Close the hungry apps.** Other video calls, big downloads, and screen recorders compete for the same uplink.

## When the network fights back

GuestPass watches each connection and **adapts automatically** — if a guest's CPU or network is struggling, it quietly lowers their video and eases it back up once things recover. You'll see a "degrading/recovering" hint on their tile. If you want to skip the gradual recovery, **Bump quality now** in the greenroom asks everyone to jump back to full quality at once.

If a guest's network blocks peer-to-peer entirely (strict corporate or campus firewalls), they'll get a clear heads-up rather than a silent hang. For those cases a **TURN relay** can carry them through — it forwards the encrypted media it still can't read, at the cost of an extra hop. Whether you can enable one (or just need to move the guest to another network) depends on who runs your instance — see [Connections & networking](connections-and-networking).
