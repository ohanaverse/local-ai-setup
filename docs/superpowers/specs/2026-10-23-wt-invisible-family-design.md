# Design: wt — invisible provider family in the others check (issue #423)

Date: 2026-10-23

Fixes #423, the follow-up from the #412 review of #421: a provider family
with no registry footprint was invisible to the start's others check, so a
start could load beside a family that was actually serving with no question
asked and no warning printed (no TTY), and the #412 silent double-load
happened anyway.

## Intent

Close the one path the #412 "others" check could not reach: a family wt
cannot see (no probe entry, because it has no registry footprint) but which
is serving a model.

## What is wrong today

- `ProbeTrusted` (`wt/internal/lifecycle/lifecycle.go:288`) treats a family
  absent from `Snapshot.Providers` as trusted. Deliberate, documented: inventory
  registers a family's key whenever the config has a local provider or model
  for it, so an absent key means nothing is being started into that family.
- `inventory` (`wt/internal/localmodels/inventory.go`, `inventory()`) builds
  its probe family set only from local provider rows (`LocationLocal`) and
  local registry models. A family with no registry footprint is therefore never
  probed, and the snapshot gets no `Providers`/`Down` entry for it.
- `residentOthers` (`wt/internal/lifecycle/evictions.go:139`) walks
  `snap.Providers` for untrusted families (skipping `family == ""` and the
  target's family) and then `snap.Entries` for running ones. An absent family
  is therefore trusted-and-empty: skipped as untrusted, and it has no entries,
  so it is not a resident either. The mtplx pidfile fallback in `residents`
  covers an mtplx that is *loading*, not one that is *serving* with no registry
  row.
- The start only asks or warns when `Others`/`OthersUnknown` are non-empty
  (`askOthers = !opts.Others.Decided && (len(others) > 0 || len(othersUnknown) > 0)`).

Consequence: a start of a second local model beside a serving-but-unregistered
family proceeds silently — no question on TTY, no warning without.

## Goals / non-goals / anti-goals

Goals:

- No silent double-load of a family that is serving.
- Keep the post-exit stop picker consistent: its use of the same
  `ProbeTrusted` rule must not offer a stop no probe confirmed.

Non-goals:

- Changing the known-family table (`familyIDs`) itself.
- Changing behavior for families wt has a backend for but that are fully
  registered (already handled by the existing probed paths).

Anti-goals:

- No new warning spam for providers nobody runs; an unprobed family that
  refuses the connection must stay silent.

## Approaches considered

### A. Probe every known local family whose origin resolves

`inventory` probes all families in `localmodels.Families()` (ollama,
omlx/omlx-6bit, mtplx, mlx_lm_server) instead of only the cfg-derived set, so
the snapshot has an entry for every family and `residentOthers` sees them all.

- Pros: real data (running, sizes) for every family; the others check works
  uniformly.
- Cons: widens probe scope on every start — extra probe rounds for families
  the user never uses; the issue flags this as a hot-path cost.

### B. Report families wt did not probe as unknown, when they answer

Keep the cfg-derived probe set; in the others check, treat families wt knows
from the backend table that are absent from the snapshot as "not probed". For
the start's others question, add such families to `OthersUnknown` when the
probe would answer (an answering family is "unknown what it holds", never
"empty").

- Pros: conservative — closes the silent double-load for the
  unregistered-family case without widening probe scope for all families;
  "absent ⇒ trusted" for the stop picker is untouched.
- Cons: requires probing the unprobed family to determine it answers; the
  stop picker must not offer stops for families it did not probe (its
  `ProbeTrusted` absent-means-trusted rule has to be reconciled with the new
  unknown signal, or the two paths disagree).

## Decisions (mine, pending confirmation)

- Take approach B first for this fix; approach A only if the owner wants real
  per-family data behind the check later.
- The fix is scoped to the start's others check; the post-exit stop picker's
  use of `ProbeTrusted` is a separate surface that must stay consistent.

## Testing

- `wt/internal/lifecycle` `TestResidentOthers` and the inventory test pinning
  the probed family set.

## Out of scope

- #399 (omlx abandoned-request memory), #393 (mtplx 507) — next in the
  safety-first order; #405, #432 — features.