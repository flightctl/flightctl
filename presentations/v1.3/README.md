# Flight Control v1.3 talk deck

A 20–25 minute conference talk (including a ~6 minute demo) introducing Flight Control
to an open source audience that isn't necessarily an edge audience.

## Files

| File | Purpose |
|------|---------|
| `flight-control-deck.html` | **The deliverable.** One self-contained file — CSS, JS, and logos inlined. Works offline. |
| `deck.html` / `deck.css` / `deck.js` | Sources. Edit these, then rebuild. |
| `build.py` | Inlines everything into `flight-control-deck.html`. Run `./build.py [output.html]`. |
| `logo-white.png`, `logo-color.png` | Official marks from flightctl.io. |

## Presenting

Open `flight-control-deck.html` in any browser. No server, no network.

| Key | Action |
|-----|--------|
| `→` `↓` `Space` | Next fragment / slide |
| `←` `↑` | Back |
| `N` / `P` | Skip a whole slide |
| `S` | Speaker notes panel (per-slide timing budget + coaching) |
| `F` | Fullscreen |
| `B` | Blackout |
| `1`–`9` | Jump to slide |
| `?` | Shortcut help |

Slide numbers are also in the URL hash, so `flight-control-deck.html#10` opens on slide 10.

## Export to PDF

`Ctrl+P` → destination "Save as PDF" → paper size **1280 × 720 px** (or Letter,
landscape, "Fit to page") → background graphics **on**. All fragments are revealed
automatically for print; speaker notes are omitted.

## The demo slide

Slide 14 is the placeholder for the recorded demo. It lists the five beats the
recording should cover (enroll → join a fleet → change the fleet → watch the rollout →
break something). To drop in the recording:

1. Put `demo.mp4` in this directory.
2. Replace the `<div class="demo-steps">…</div>` block on slide 14 with:
   ```html
   <video src="demo.mp4" controls style="width:100%;border-radius:12px"></video>
   ```
3. Re-run `./build.py` — the video is base64-inlined too, so the single file stays
   self-contained (it will get large; for a long recording, keep `demo.mp4` beside
   `deck.html` and present from the source deck instead of the inlined deliverable).

Clicks on a `<video>` element don't advance the deck, so you can scrub during the talk.

## Timing

Speaker notes carry a running budget per slide. The shape is roughly: 4 minutes of
problem framing (slides 2–4), 9 minutes of concepts (5–13), 6 minutes of demo (14),
3 minutes of project state and contribution (15–17), leaving a few minutes of slack for
questions. Slides 13 and 12 are the designated cuts if the clock is against you — the
notes say so where it matters.
