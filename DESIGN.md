---
name: Parkrr Standalone Workbenches
description: Local-only, ink-green development dossiers built from garage-plan geometry.
colors:
  ink-canvas: "#0c1312"
  ink-surface: "#121b19"
  ink-surface-raised: "#182321"
  ink-surface-emphasis: "#1e2b28"
  ink-rule: "#293a36"
  ink-rule-strong: "#3a504a"
  ink-text: "#e8f0ee"
  ink-muted: "#9aada8"
  light-canvas: "#f2f6f5"
  light-surface: "#ffffff"
  light-surface-raised: "#ebf1f0"
  light-rule: "#d4e0dd"
  light-rule-strong: "#b7cbc6"
  light-text: "#14201e"
  teal-on-ink: "#5ed4c9"
  teal-strong-on-ink: "#2fb3a8"
  teal-on-light: "#0b615b"
  teal-strong-on-light: "#0f7a73"
  state-amber: "#e5b85c"
typography:
  display:
    fontFamily: "Bricolage, Hanken, sans-serif"
    fontWeight: 750
    lineHeight: 0.98
    letterSpacing: "-0.035em"
  body:
    fontFamily: "Hanken, system-ui, -apple-system, 'Segoe UI', sans-serif"
    fontSize: "15px"
    lineHeight: 1.55
  label:
    fontFamily: "JetBrains, ui-monospace, monospace"
    fontSize: "11px"
    fontWeight: 700
    lineHeight: 1.4
    letterSpacing: "0.06em"
rounded:
  row-control: "8px"
  control: "9px"
  field: "10px"
  action: "12px"
  workbench: "14px"
  pill: "999px"
---

# Design System: Parkrr Standalone Workbenches

## Overview

This system covers Parkrr's standalone security-audit and product-roadmap workbenches. Both are information-dense operational dossiers: a concise thesis and provenance lead into a summary, then a filter rail, compact register, and wide detail view. Low-opacity garage-plan linework ties the artifacts to Parkrr's spatial domain without competing with the evidence.

Dark ink-green is the default visual world, with a complete light counterpart. The artifacts are static, local-only HTML/CSS/JavaScript: they load local fonts, make no external service or CDN request, and keep triage or planning state non-authoritative and browser-local.

## Colors

Theme tokens swap by role rather than by component. Ink and off-white canvases use stepped green-neutral surfaces, crisp rules, high-contrast text, and muted secondary copy. Teal is deliberately scarce: use it for selection, active controls, links, focus, and small orientation marks.

Semantic hues are state-only. The audit assigns critical, high, medium, low, and informational colors to severity or triage; the roadmap reserves amber for genuinely deferred work and uses blue or red only for their named category, information, or danger meanings. Always pair color with a label, number, border, or state change.

**The Restrained Teal Rule.** Teal directs attention; it does not wash large decorative areas.

## Typography

All fonts are local WOFF2 assets. Bricolage carries large thesis headlines, section titles, and prominent counts; Hanken carries interface and reading text; JetBrains carries IDs, metadata, keyboard hints, compact labels, and tabular numerals.

Large page headlines use tight display leading and balanced wrapping. Dossier headings remain expressive but smaller; body and explanatory copy stay comfortably readable, usually within roughly 72–76 characters. Functional labels are compact, often uppercase, and never allowed to become too small to scan.

## Layout

The page container reaches at most 1760px, with fluid side padding. Desktop pages pair an editorial intro with snapshot metadata, then use bordered summary strips and a three-column master-detail workbench: a narrow control rail, a dense 310–430px register, and a flexible dossier with at least about 500px available.

The register and dossier scroll independently on wide screens. Around tablet width (1024–1060px), the dossier leaves the grid and opens as a focused full-screen layer below the sticky header. At the narrow breakpoint (620–700px), metadata and summaries stack, controls move above the register, labels simplify, and the dossier occupies the screen. Dialogs also become edge-to-edge on small screens.

Garage-plan geometry appears as faint, fixed CSS/data-URL SVG linework behind the content. It is environmental texture, not a content illustration.

## Elevation & Depth

Surfaces are flat by default. One-pixel borders, adjacent tonal layers, selected-row fills, and narrow state markers establish hierarchy; the principal strips and workbench do not float. Shadows are reserved for true overlays where present, while the sticky header uses a translucent background and restrained blur.

**The Border-Before-Shadow Rule.** Establish structure with rules and surface contrast before adding elevation.

## Shapes

The form language is crisp but not sharp: major workbench shells use 14px corners, fields and quiet controls use 8–10px corners, icon actions use 12px, and status chips or counters are fully pill-shaped. Most structure is drawn with 1px borders. Inline SVG icons use rounded, unfilled strokes and remain visually subordinate to labels.

## Components

### Workbench

Use the filter/register/dossier composition for large inspectable sets. Rows prioritize identifiers, titles, and concise metadata instead of equal-weight cards. Selection combines a tinted surface with a stronger leading rule. The dossier carries the full argument in labeled sections and preserves direct, URL-addressable navigation where supported.

### Controls and navigation

Use native buttons, inputs, selects, checkboxes, textareas, and dialogs, then style their surrounding system consistently. Controls inherit type, keep familiar affordances, and expose explicit labels or accessible names. A global visible focus ring uses a 3px translucent teal halo; focused fields also shift their border to teal. Skip links, live regions, keyboard hints, and real focusable row actions are part of the pattern.

Interaction motion is limited to 150–160ms color, border, and one-pixel press feedback. Under `prefers-reduced-motion: reduce`, smooth scrolling and transitions are removed.

### Print

Print forces a light, high-contrast palette. Hide filtering rails, registers, transient overlays, local action chrome, and toasts; reveal the selected dossier in normal document flow, remove unnecessary elevation and workbench borders, and keep summary material from splitting where practical.

## Do's and Don'ts

### Do

- **Do** preserve the dense master-detail hierarchy and label-first scanning behavior.
- **Do** keep all runtime dependencies, fonts, icons, and state local to the static artifact.
- **Do** use visible focus, native semantics, non-color state cues, reduced-motion behavior, and a deliberate print view.
- **Do** use flat tonal layers, 1px rules, and garage-plan linework as the recurring visual signature.

### Don't

- **Don't** turn registers into a wall of equal cards or let decoration outrank the dossier.
- **Don't** use teal or semantic colors as broad decoration.
- **Don't** add external fonts, CDNs, telemetry, hosted services, or authoritative workflow persistence.
- **Don't** introduce raster imagery into these workbenches without a new, documented need and provenance.

### Asset provenance

These workbenches ship no raster assets. Their visual assets are inline SVG icons, CSS/data-URL SVG garage-plan linework, and local fonts. Screenshot files under `.impeccable/review/` are QA evidence, not product assets, and must not be referenced by shipping workbench markup or styles.
