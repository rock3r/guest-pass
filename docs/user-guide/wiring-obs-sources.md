# Wiring OBS sources

GuestPass guests show up in your broadcaster as ordinary **Browser sources**. There's no plugin and no special integration — if your software can load a URL, it can show a guest.

## The slot model

Every host has a fixed pool of slots: **`cam-1` through `cam-8`** for cameras, plus a **`screen`** slot for screen-shares. Each slot has a permanent URL.

The key idea: **URLs are tied to the slot, not the guest.** You wire OBS to `cam-1`, `cam-2`, and so on *once*. Each stream you simply decide which guest sits in which slot — in the [greenroom](the-greenroom) — and the right camera flows to the source you already set up. Build your scene a single time and reuse it forever.

## Add a Browser source

In OBS: **Sources → + → Browser**, then:

| Field | Value |
| --- | --- |
| URL | the slot URL from your **OBS & sources** tab |
| Width | `1280` |
| Height | `720` |
| FPS | `30` |

Leave **"Shutdown source when not visible"** off so the connection stays warm, and keep **"Refresh browser when scene becomes active"** off so you don't drop a live guest when you cut to the scene.

### Turn on "Control audio via OBS"

This one is easy to miss and matters most: in the Browser source properties, enable **"Control audio via OBS."** Without it, the guest's microphone plays on your machine but **never reaches your broadcast** — a classic "why can't anyone hear my guest?" moment.

With it on, each guest source shows up as its own channel in the OBS **Audio Mixer**, so you can set their level, mute them, add filters, and route them independently — exactly like any other audio source.

The page renders with a **transparent background**, so you can layer it over a scene, crop it, or drop it into a frame. It **auto-reconnects** if the network blips, and shows a small nameplate you can hide.

## Getting your URLs

Open a stream's **OBS & sources** tab. Every slot's URL is shown there, and you can copy it any time — they don't disappear after setup. If a URL ever leaks, hit **Regenerate** on that slot (or **Regenerate all**) to rotate it; the old URL stops working immediately and any live source on it is dropped.

> The URLs are a host-only secret — anyone with a slot URL can publish to that source. Don't paste them into a public chat or commit them anywhere.

## Resolution and quality

A slot's URL accepts a `?res=` hint (for example `?res=1080`) to cap that source's resolution. The stream-wide ceiling — max resolution, frame rate, and bitrate — is set per stream in the greenroom while you're live. See [The greenroom](the-greenroom).
