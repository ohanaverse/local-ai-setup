# Design: Last-Model Indicator in the `wt` Model Picker

Date: 2026-09-12
Issue: #35
Context: `wt` model picker (`internal/tui`), rotation state (`internal/rotation`).

## Goal

In the `wt` model picker, visually mark which model was used in the previous session, so the user can see where they left off. The indicator answers "which row was my last launch" without changing any picker behavior.

## Current State

- `rotation.state` (`~/.config/agent-wt/rotation.state`) already persists the last-launched model ID. `rotation.Last()` reads it; `Record()` writes it at the single commit point every genuine launch passes through (TUI `launchAndRecord`, CLI `launchFiltered` — both after the ollama check and resume prompt are satisfied). Cancelled prompts and failed checks never advance it.
- The model picker (`enterModelPhase` in `internal/tui/app.go`) already consults rotation state, but only to position the **cursor** on the rotation's *next-to-use* model (the eligible model *after* the last launched). It never shows the user which model was last used.
- Each picker row is a compact one-line string baked by `buildModelItems` (`internal/tui/model_list.go`): family / 30-day count / ID / location / usage counts / pricing / tags, rune-aligned via `fmt` width verbs.

## Decision Record

Two questions were settled during brainstorming:

1. **Semantic of "last"** — reuse `rotation.state` (last genuinely-launched model), *not* a new "last picked in picker" state file. The roadmap's PR E1 sketch proposed a new `~/.config/agent-wt/last-model` file; that predates reading the current code — `rotation.state` already holds the right data with better-defined semantics. No new state, no writes.
2. **Indicator form** — a fixed-width prefix marker on the row, *not* row styling (collides with cursor highlight; the line is a pre-baked plain string) and *not* a footer line (does not pinpoint the row).

## Design

### Behavior

When the model picker opens, the row whose model ID equals `rotation.Last()` carries a `▶` prefix marker. All other rows carry a blank prefix. Everything else about the picker is unchanged — in particular the cursor still lands on the rotation's next-to-use model, which is typically one row away from the marker. The two coincide only when the last model is the sole eligible model and rotation wraps to itself.

No prior launch (`rotation.state` absent, empty, or unreadable) → no marker anywhere, identical to today's display.

### Rendering

`buildModelItems` gains a `lastID string` parameter. Every row's line gets a fixed 2-rune prefix via `%-2s`: `"▶"` on the matching row (padded to 2 runes), an empty string (two spaces) elsewhere. Go's `fmt` width counts runes for strings, matching the existing `utf8.RuneCountInString` width logic — columns stay aligned. The match is by exact model ID against the eligible slice.

Because the marker is part of the pre-baked line, it is also part of `FilterValue()`. Typing `▶` in the fuzzy filter would match only the marked row — a harmless quirk; decoupling the marker from the filter value is not worth the added plumbing.

### Data Flow

`enterModelPhase` already constructs `rotation.New()` to compute the cursor position; the same site fetches `lastID, _ := rotation.New().Last()` and passes it into `buildModelItems`. The feature is read-only: no state is written anywhere in the picker path.

### Edge Cases

- `rotation.state` contains an ID not in the eligible list (different agent, `-T`/`-F` filter active, model deleted from registry) → no row is marked.
- `rotation.state` corrupt or empty → `Last()` returns `false` → no marker (today's behavior).
- Empty eligible list → the picker is not opened (existing guard, unchanged).

## Code Shape

| File | Change |
|---|---|
| `wt/internal/tui/model_list.go` | `buildModelItems(models, familyOf, store, lastID)` — new trailing param; 2-rune prefix inline in the line format |
| `wt/internal/tui/app.go` | `enterModelPhase` fetches `rotation.New().Last()` and threads it into `buildModelItems` |
| `wt/CLAUDE.md` | One-line note in the Rotation section: the picker marks the last-launched row with `▶` |

No new packages, no contract fixtures, no registry/state schema changes, no `wt rotate` output change.

## Verification Plan

1. **Unit tests (`wt/internal/tui`)**:
   - `buildModelItems` with `lastID` matching one model → exactly that row's line starts with `▶`; all other rows start with two spaces.
   - All emitted lines have equal rune length (alignment preserved).
   - `lastID` empty or not present in the eligible slice → all rows unmarked.
2. **Picker-level test**: with a rotation fixture recording model X, `enterModelPhase` leaves the cursor on rotation's next-to-use model (≠ X's row when >1 eligible model) and marks X's row.
3. **Regression**: existing `buildModelItems` call sites and tests updated for the new parameter; `go test ./...` and `go vet ./...` pass from `wt/`.

Manual smoke: launch `wt`, pick a model, quit, reopen the picker — the previously used model's row shows `▶` and the cursor rests on the next rotation candidate.