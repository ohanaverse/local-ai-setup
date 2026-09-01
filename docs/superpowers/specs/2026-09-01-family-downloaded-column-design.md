# Family Screen DOWNLOADED Column — Design

**Date:** 2026-09-01
**Status:** Approved
**Scope:** `modelman/src/modelman/screens/families.py` (+ one test)

## Problem

The family screen's READY column counts models per family that reconcile as
on-disk, but the label doesn't say "downloaded", and the count includes
**cloud** models: `ollama show <model>:cloud` exits 0, so cloud-only families
(e.g. glm-5.3) show READY=1 despite nothing consuming disk. The user wants a
column that quickly shows how many local models each family has downloaded,
to spot families accumulating many downloads.

A raw count cannot distinguish "N distinct models" from "1 model downloaded
N times" — and that is acceptable: different sizes within a family
(ornith-1.5 35b vs 9b) are legitimately kept side by side, so per-family
copy-level duplicate detection is out of scope. The count is a triage signal;
detailed inspection happens on the model screen.

## Decision

Rename the READY column to **DOWNLOADED** and count only models with a
local location. Cloud entries are never counted: they hold no disk weights
and therefore cannot be duplicates.

## Changes

All in `modelman/src/modelman/screens/families.py`:

1. `on_mount()` — header string `"READY"` → `"DOWNLOADED"`. Same position
   (between VARIANTS and SIZE).
2. `reload()` — in the per-model loop, skip models whose `ModelEntry.location`
   equals `"cloud"` before any ready/size accounting. Entries with
   `location is None` (legacy) still count. The state-fallback branch
   (`elif self.state.get(m.id).ready`) is inside the loop and therefore
   inherits the skip.

Nothing else changes:

- Reconcile worker still queries every model, including cloud ones
  (one extra `ollama show` subprocess per cloud model — negligible; keeps
  the diff minimal).
- SIZE column: a family with zero counted downloads shows `—` (existing
  behavior).
- ModelScreen's per-model STATUS column is untouched — a cloud model still
  shows ready there. The two views answer different questions (family =
  disk usage, model = usable).
- No registry/state format changes; no CLI changes.

## Testing

- New test in `tests/screens/test_families.py`: a family with one
  cloud-location model and one local downloaded model shows DOWNLOADED=1.
- The 5 existing family-screen tests keep passing (none assert the READY
  header or cloud behavior).

## Docs

No guide references the family table columns (grep across `docs/guides/`
confirmed), so no doc drift.