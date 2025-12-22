# Swarm TUI Theme System

This document defines the baseline visual system for Swarm's TUI: color roles,
state semantics, spacing rules, and palette variants. It is intended to keep
TUI components visually consistent and premium.

## Goals

- Clear state semantics at a glance.
- Consistent layout and spacing.
- High-contrast option for accessibility.
- Terminal-safe colors (no reliance on font features).

## Color roles (tokens)

Use semantic roles instead of hard-coded colors:

- `bg`: primary background
- `panel`: panel background
- `text`: primary text
- `textMuted`: secondary text
- `border`: neutral border
- `accent`: primary action highlight
- `focus`: focus/selection highlight
- `success`: positive state
- `warning`: caution state
- `error`: error state
- `info`: informational state

## State mapping

- Idle: `success`
- Working: `accent`
- AwaitingApproval: `warning`
- RateLimited: `warning`
- Paused: `info`
- Error: `error`
- Starting: `info`
- Stopped: `textMuted`

## Spacing and layout

- Use 1-space padding inside cards; 1-line gap between sections.
- Avoid dense walls of text; prefer short labels and aligned columns.
- Selection should be obvious without relying on color alone (underline or
  reversed background).

## Typography

- Use simple ASCII glyphs for status markers in terminals by default.
- Avoid heavy ASCII art; keep labels concise.

## Palettes

Two baseline palettes are required:

1. **Default**: balanced contrast with a modern feel.
2. **High-contrast**: for accessibility and low-contrast terminals.

See `internal/tui/styles` for the concrete palette definitions.
