# PR #1 Risk-First Review Design

**Date:** 2026-06-01  
**Status:** Approved

## Problem

PR #1 is large (31 files, 4,723 additions) and introduces multiple executable shell scripts, git hook logic, and supporting docs/config. A broad unfocused review risks missing high-impact defects in safety-critical paths.

## Goal

Produce a **blocking/high-confidence-only** review for PR #1 with a prioritized finding list. Each finding must include:

- exact evidence (`file:line`)
- clear impact
- concrete fix suggestion

## Design

### Architecture

Use a **risk-first two-pass review**:

1. **Pass A (critical path):** deeply review executable shell scripts and hook logic (`bin/*`, `git-hooks/*`) for safety, correctness, and branch protection behavior.
2. **Pass B (broad path):** review docs/config additions for behavior mismatches, setup inconsistencies, and operational risk.
3. Emit only blocking, high-confidence findings in prioritized order.

### Components

- **Scope mapper:** classifies changed files into risk tiers.
- **Risk checklist engine:** applies shell and hook-specific checks (quoting, unsafe deletes, unchecked variables, bypass vectors, path assumptions).
- **Evidence extractor:** captures exact source locations for each confirmed issue.
- **Finding formatter:** outputs `Finding -> Evidence -> Impact -> Fix`.

### Data Flow

1. Read PR metadata and changed file list.
2. Partition files into critical path and broad path.
3. Execute deep review on critical path files first.
4. Execute consistency review on docs/config.
5. Consolidate and prioritize only blocking/high-confidence findings.

### Error Handling and Confidence Gates

- If PR data retrieval fails, stop and surface the exact failure.
- If evidence is weak or ambiguous, do not report the item as a finding.
- If line mapping is imperfect, cite the nearest stable location and state the limitation.
- No speculative or low-confidence warnings.

### Review Quality Checks

- Every finding contains `file:line` evidence.
- Every finding contains a concrete fix suggestion.
- Every finding meets blocking/high-confidence bar.
- Final order is risk-prioritized, not file-order.

## Alternatives Considered

### Exhaustive full-file review

- **Pros:** Maximum coverage.
- **Cons:** Slower and noisier for a large PR; higher risk of diluting critical issues.
- **Decision:** Rejected for this request due to strict blocking-only focus.

### Automated checklist first, manual deep dive second

- **Pros:** Fast initial signal.
- **Cons:** Script-only heuristics can miss context-dependent logic flaws.
- **Decision:** Rejected as primary method; manual risk-first review remains primary.

## Success Criteria

- [ ] Review output includes only blocking/high-confidence findings.
- [ ] Each finding includes evidence, impact, and fix.
- [ ] Critical script/hook paths are covered before docs/config.
- [ ] Findings are prioritized by operational risk.
