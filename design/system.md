# Design system — source snapshot

Synced from `src/app/globals.css`. This is an extracted foundation, not a proposed new palette.
The old access-panel composition is not a required layout: new artboards may recompose the same primitives.

## Colors

11 semantic color tokens, each with light and dark values. Preserve the semantic pairs; use only these colors.
Source: `src/app/globals.css :root` and `@media (prefers-color-scheme: dark)`.

| Token | Light | Dark | Source |
| --- | --- | --- | --- |
| `--background` | `#f5f3ee` | `#18221d` | src/app/globals.css |
| `--surface` | `#fffefa` | `#202e26` | src/app/globals.css |
| `--foreground` | `#203c32` | `#edece4` | src/app/globals.css |
| `--muted` | `#57645b` | `#bdc9bf` | src/app/globals.css |
| `--border` | `#c9cec4` | `#607266` | src/app/globals.css |
| `--primary` | `#244d3d` | `#bfdcc8` | src/app/globals.css |
| `--on-primary` | `#fffefa` | `#182c20` | src/app/globals.css |
| `--soft` | `#e8ede5` | `#304739` | src/app/globals.css |
| `--danger` | `#8a3028` | `#ffb6a9` | src/app/globals.css |
| `--feedback` | `#f4ead7` | `#493f29` | src/app/globals.css |
| `--focus` | `#77501d` | `#f3cb89` | src/app/globals.css |

Use background for the canvas, surface for forms/cards, foreground for text, muted for secondary text,
primary/on-primary as a paired action or inverse panel, soft for quiet sections, danger for errors,
feedback for contextual notices, focus for visible focus. Border is decorative; use primary or muted
for interactive boundaries needing non-text contrast. Current color is permitted for SVG strokes.

## Typography

- Stack: `Arial, Helvetica, sans-serif` (system fonts only).
- Observed sizes: `0.75rem`, `0.875rem`, `1rem`, `1.05rem`, `1.25rem`, `1.35rem`, `1.8rem`, `2rem`.
- Explicit weights: 600 and 700; body retains normal browser weight.
- Line heights: 1.6 body, 1.2 h1, 1.35 h2.
- Letter spacing: -0.03em heading; 0.12em uppercase eyebrow.
- Source: `src/app/globals.css body, h1, h2, h3, .brand, .eyebrow, .sidebar-note, @media(max-width:800px)`.

## Spacing and geometry

- Observed spacing values: `0.25rem`, `0.5rem`, `0.6rem`, `0.65rem`, `0.75rem`, `0.85rem`,
  `1rem`, `1.25rem`, `1.5rem`, `1.75rem`, `2rem`, `2.5rem`, `3rem`, `3.5rem`.
- Radii: `--radius:0.5rem` panels; 0.3rem controls; 0.35rem navigation.
- Borders: 1px controls/panels; 3px feedback/focus; focus offset 4px.
- No shadow tokens are declared: do not invent shadows.
- Observed responsive breakpoint: 800px. One-off illustration and layout dimensions are local geometry, not new tokens.
- Source: `src/app/globals.css` control, panel, sidebar, feedback, access-panel and media rules.

## Component rules

- Input/button/link touch targets at least 44px. Associated visible labels; email and current-password autocomplete.
- Primary action pairs primary background with on-primary text. Hover uses existing semantic colors.
- Focus: 3px focus outline with 4px offset; never remove keyboard focus.
- Errors: danger text and 3px left border, generic non-enumerating message, form-level association.
- Heading hierarchy and semantic main/form/section landmarks. One h1 for the sign-in task.
- Source default is OS-aware light/dark. Design boards may explicitly preview either source theme;
  do not silently present a new color palette as existing project tokens.
- No external fonts/assets, fake operational metrics, or unsupported sign-up/SSO/password-reset interactions.

## Artboard self-check

List all literal colors and font-family declarations. Every color must match the source table and
every family must match the source stack. Check 320px/1440px layouts, visible focus, 44px controls,
error associations and both theme pairs. Reduced-motion preference must be respected if motion exists.
