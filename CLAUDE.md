# Miqyas Landing Page

Marketing landing page for **Miqyas** — AI that measures construction progress by comparing
360° site video to a BIM model and schedule, element by element.

## Architecture

- **Single file: `index.html`** (~2,600 lines). Everything lives here: inline `<style>`,
  inline vanilla JS, and a Three.js BIM scene loaded from a CDN importmap. No build step,
  no framework, no node_modules.
- **`assets/logo.jpeg`** — the only asset.
- Deploys to **Vercel automatically on push to `main`**. Repo: `AsylkhanKali/miqyas_landing`.

## Run / preview

```bash
python3 -m http.server 8123 --directory /Users/asylkhan/Projects/Landing_page
# then open http://localhost:8123
```

## Deploy

Push to `main`. Vercel redeploys. Always commit + push when the user asks; this is a live site.

## Design system (tokens in `:root`, top of `<style>`)

- **Theme is locked dark.** Never introduce a light section mid-page (the CTA was deliberately
  converted from white to dark for this reason).
- **Fonts:** `Fraunces` (serif display, headings + numerals, uses `opsz` optical axis),
  `DM Sans` (body, nav, buttons), `Syne` (uppercase labels). Loaded via Google Fonts `<link>`.
- **Colors:** `--bg #03060f`, `--accent #5b8cff` (blue — the single brand accent), `--gold #e8a94a`
  (third color: alerts / step numbers / "behind schedule"). Status: green on-track, amber/gold
  behind, blue ahead, slate not-started. One accent locked across the whole page.
- **Muted text is `--muted #8894a4`** (raised for contrast — do not darken it again).

## Page sections (in order)

nav → hero (full-bleed UAE construction photo + overlay + stats strip) → 3D BIM scene →
How It Works (3-step pipeline) → Why Miqyas (3 cards) → Features (numbered list) → FAQ
(accordion) → CTA → footer.

## The 3D scene (Three.js, bottom of file)

- Each floor's full element set (slab, beams, curtain wall, scaffolding, label) is parented into
  a per-floor `THREE.Group` (`floorGroups[]`) so it animates as a unit.
- **Building assembly:** on first scroll into view, floor groups rise from underground
  (`GROUND_OFFSET`) bottom-to-top with a spring lerp, then a white scan-flash cascades up.
- Structural materials are blue-grey (`0x303848`–`0x586070`) so they read against the dark bg.
  Do not darken them back toward black — they vanish.
- Clicking a floor opens the side panel (`#panel`, ids `p-iou`, `p-conf`, `p-crit`, etc.).
  The `FLOORS[]` array holds the demo data.

## Animations

Plenty, all construction-themed: hero h1 clip-path "drafting" reveal, stat count-up, pipeline
data-packet dots + flow shimmer, blueprint grid fade-in, file-card progress fills, Gantt-fill
section dividers, pulsing live-status kicker dots, cycling console ticker, CTA floating
measurement glyphs, building assembly. **All gated behind `prefers-reduced-motion`** — keep it
that way when adding more.

## Hard rules (do not regress)

- **ZERO em-dashes (`—`) and zero en-dashes (`–`) in user-visible text.** Use periods, commas,
  colons, parens, or a hyphen for ranges (`3-5×`, `1-10`). This is the most-checked AI tell; the
  whole page was scrubbed once already.
- **Eyebrow restraint:** max ~1 uppercase section label per 3 sections. Currently only
  "How It Works" and "Why Miqyas" have labels; headlines carry the rest.
- **Copy is honest** (`docs/LANDING_PAGE_BRIEF.md` in the `miqyas` product repo is the
  source of truth). No fabricated testimonials, no invented ROI/accuracy numbers. Demo numbers
  in the 3D panel / pipeline mockup are illustrative product output, which is fine.
- **Preserve user-approved compositions:** the hero stats strip, the live-status dots, the
  console ticker. The user likes these; don't strip them in a "cleanup."
- **Preserve JS hooks:** `openModal`/`closeModal`/`closePanel`, Formspree id `mojrjapo`, and all
  the `#panel` / scene element ids. The demo modal posts to Formspree.

## When making changes

Surgical edits to `index.html` only. Match the existing token system and dark theme. Verify in
the preview (no console errors, the 3D scene still loads and floors are clickable) before pushing.
