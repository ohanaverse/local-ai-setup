# Follow-up Issues Design — local-ai-setup tracker

**Date:** 2026-08-29
**Input:** `issues.md` (follow-up issues from the 2026-08-29 final review of the
user-guide set)
**Approach (user-approved):** full sweep, one design + one plan, sequenced
security-first; one branch with one commit per phase; agent-worktree gets its
own branch + PR.

**Hard rule for this whole effort:** no secret value — old or newly generated —
may appear in any committed file. The old values below are always described,
never quoted.

---

## Findings from exploration (closes issue 1's audit item)

Verified against git history (`git rev-list --all` exact-value searches):

| Secret | Leak scope |
|---|---|
| LiteLLM master key | `docs/reference/litellm-admin-ui-setup.md` (§1, §10, "Final plist") + the quoted copy in `issues.md` |
| `LITELLM_SALT_KEY` | same reference doc, 3 spots (incl. the "e.g." example output, which is the real value) |
| UI password (`admin`) | same reference doc, 4 spots — weak default, also rotate |
| OpenRouter API key | **never committed** — plist + `~/.config` files only. No repo action required. |

All three leaked values are **locally minted** (LiteLLM reads them from the
plist env at startup; nothing external registers them), so rotation is:
generate → inject into plist → kickstart. Approved history decision:
**accept history, do not rewrite** — rotation kills the values' utility, and a
rewrite would orphan the checkout-SHA stamps recorded in guide 08 and force a
force-push. A short "rotated 2026-08-29, history accepted" note goes in the
reference doc instead.

---

## Phase 1 — Rotate secrets + redact (issue 1)

Order inside the phase: the redaction commit lands only after the machine-side
rotation verifies, so the pushed tree never carries or leads a live value.

**Pre-flight (machine):**

1. `cp ~/Library/LaunchAgents/local.litellm.proxy.plist /tmp/plist.pre-rotation.backup`
2. Salt-safety check: `pg_dump litellm > /tmp/litellm-pre-rotation.sql`; confirm
   the LiteLLM DB holds no stored provider credentials (this setup passes
   provider keys via `config.yaml` plaintext, not the DB). Only if the DB is
   clean, rotate the salt key. The dump + a copy of the old salt kept briefly in
   `/tmp` is the rollback.

**Rotation (machine):**

- Generate master key (`sk-` prefix, ~40 chars), salt key, UI password with
  `openssl rand -base64 32 | tr -d '\n/=' | head -c 40` style commands.
- Edit the three values **directly into
  `~/Library/LaunchAgents/local.litellm.proxy.plist`** — the values transit
  through nothing in the repo, the plan, or the chat log. The user reads the
  new UI password from the plist (store in a password manager if desired).
- `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`.
- Verify with the guide 08 health block: unauthed `/v1/models` → 401; authed
  with new key → 200; UI login with the new password; `~/.litellm.err.log`
  clean; `launchctl list` shows the service alive.

**Redaction (repo, same commit):**

- `docs/reference/litellm-admin-ui-setup.md`: every real value → explicit
  placeholder (`sk-litellm-<rotate-me>`, `<generated-salt>`, `<strong-password>`)
  in §1's xml block, Step 7's "e.g." line, Step 10's curl, the 401 troubleshooting
  note, and the "Final plist" block. Shapes and instruction stay intact.
- `issues.md`: redact the master key quote in §1's heading text when checking
  the item off.
- Add the history-accepted note (short, dated) to the reference doc.
- Close-out gate: repo-wide grep for the old master key and old salt → zero
  hits.

**User follow-up (manual, after the plan; checklist carried in the plan):**
OpenRouter rotation — revoke + regenerate at openrouter.ai/keys → update the
plist `OPENROUTER_API_KEY`, the four `api_key:` rows in `~/.config/litellm/config.yaml`,
and the `secret_ref` in `~/.config/local-ai/registry.toml` → kickstart → verify
one OpenRouter-routed call.

---

## Phase 2 — agent-worktree README drift (issue 2a)

**Repo:** `~/github/ohanaverse/agent-worktree` · branch
`fix/readme-drift-wt010` · one commit · PR (repo convention).

Corrections against source `bf98d14`:

1. Quick start (`README.md` ~47): `-w my-feature` → `-W my-feature`.
2. Flags table (~74): row becomes `-W <name>, --worktree <name>`; "skip worktree
   picker" semantics unchanged.
3. `--cwd` row (~75): "skip TUI and session resume" → skips the TUI picker
   **only**; a prior resume-capable session still auto-resumes at launch
   (`cmd/wt/launch.go:28-44`, `internal/agents/agents.go:287-288`).
4. Model rotation section (~113–125):
   - Remove the `d` toggle from the Flags intro sentence and the rotation bullet;
     state the real mechanism: `-T <tag>` filtering.
   - Rotation state: per-tag `rotation-<tag>.state` ("each tag has its own
     rotation state") → single global `~/.config/agent-wt/rotation.state`;
     per-tag files described as one-shot migration inputs, merged then deleted.
   - Sanity-check the `wt rotate code/design` examples against
     `internal/rotation`; rewrite only if the tag argument no longer filters.

**Scope guard:** grep the README for other instances of the same stale claims
(`-w `, `` `d` ``, `rotation-`); same-claim instances get fixed, anything
separately stale is reported in the PR, not fixed.

**Sequencing dependency:** Phase 4 (guide pruning) requires this PR merged. If
it stalls, skip Phase 4 and close issue 2 half-done with a note.

---

## Phase 3 — modelman convergence (issue 3)

**Machine steps (from `~/github/ohanaverse/modelman`):**

1. `cp ~/.config/litellm/config.yaml /tmp/config.yaml.pre-expose`
2. `uv run modelman expose ollama/qwen3.8:27b-mlx`
   `uv run modelman expose ollama/ornith-1.5:35b`

Effects (source-verified in modelman `src/modelman/litellm.py`): flags flip to
`litellm_exposed = true` in `modelman.toml`; the two rows are rewritten in
canonical modelman shape; the 9 foreign rows are preserved as data. Accepted
side effects: comment banners stripped, file re-dumped by PyYAML. Banners are
**not** re-added (guides get downshifted instead).

**Verification gate before doc edits:**

- `diff` pre/post `config.yaml`: only the two rows plus banner/format loss.
- The two rows keep `litellm_params.model: ollama_chat/…` field semantics.
- `launchctl kickstart`; authed `/v1/models` lists all 11 names.

**Rollback:** restore `/tmp/config.yaml.pre-expose`, revert the two flags in
`modelman.toml`, kickstart. No repo files involved.

**Doc steps:** guides 02, 04, 08 drift paragraphs → one short historical
footnote each (drift real until 2026-08-29; two in-registry ollama entries now
modelman-exposed; the **nine** omlx/openrouter/llama.cpp entries are
**hand-managed by design**, not drift). `00-config-map.md` gets one ownership
line: the 9 foreign entries sit outside modelman's registry (which carries only
the `ollama` provider). Quote-verify each target line before editing;
issues.md line refs may have shifted.

---

## Phase 4 — Guide caveat pruning (issue 2b) — merge-gated

**Precondition:** the agent-worktree README PR merged. Keep the true behavior
claim in each spot; drop the "README is stale" framing and line-number blame.

- `03-model-families.md` stale-README bullet: keep facts (no `d` handler, single
  global `rotation.state`, per-tag files are deleted-after migration inputs).
- `06-wt-agents-and-models.md` —W/`-w` paragraph: keep "`-W` is the real flag".
- The "NO `d` key" bullet: keep "use `-T code`/`-T design`" + tags-are-latent note.
- The `--cwd` auto-resume explanation: keep, minus the README:75 blame clause.
- "Going deeper" pointer: remove the stale-spots parenthetical.

## Phase 5 — Tooling + minors (issue 4 remainder; two minor notes)

- **`bin/check-links`**: extract the plan-doc snippet to a standalone stdlib
  python3 script; targets: `README.md`, `CLAUDE.md`, `docs/guides/*.md`,
  `docs/reference/*.md`, `docs/archive/*.md`, `benchmarks/*.md`; same
  repo-relative-link semantics with `%20` decoding. `make check-links` target
  (separate from `lint`). Plan-doc §"Verification tooling": snippet replaced by
  a pointer + expected `ALL LINKS OK`. CLAUDE.md mention updated if it
  references the old snippet.
- **Health-probe convergence:** guide 05 probe lines (~162–163) → `/health`
  for omlx (8000) and llama.cpp (8080), matching the canonical block (README +
  guide 08). Guide 01's model-*listing* `/v1/models` calls are content, not
  probes — untouched.
- **Guide 05 wording** (~96): "mirror per-model tags" → "mirror per-model ids"
  (examples are model ids, not the `tags` registry field).

## Phase 6 — Bookkeeping + close-out

- `issues.md`: check off remaining items with short dated notes; issue 2 notes
  the merge-precondition outcome.
- OpenRouter user-follow-up checklist carried in the plan.
- Final gates: `make check-links` on the widened targets → `ALL LINKS OK`;
  repo-wide grep gate for the old secret values → zero hits.

---

## Commit strategy

Branch `fix/followup-issues`; one commit per phase (5 commits); one PR at the
end. agent-worktree work lives on its own branch/PR in that repo. Nothing in
this repo is committed before the machine-side rotation + redaction of the same
phase are done together (no intermediate push carrying a live value).

## Error handling & rollback summary

| Failure | Response |
|---|---|
| Salt rotation breaks DB-stored keys | Restore old salt briefly, restore keys from `/tmp/litellm-pre-rotation.sql`, re-rotate only after clearing stored credentials |
| Proxy unhealthy after kickstart | Check `~/.litellm.err.log`; restore plist from `/tmp` backup if needed |
| `modelman expose` rewrites beyond the two rows | Stop; restore `/tmp/config.yaml.pre-expose`; investigate before retrying |
| wt README fix uncovers more drift | Report in the PR; fix only same-claim instances |
| Phase 4 precondition unmet | Skip pruning; close issue 2 half-done |

## Success criteria

1. Old master key / salt / password: zero hits repo-wide, and none of the new
   commits reintroduce them; proxy healthy on the rotated values.
2. `modelman.toml` flags `true` exactly for the two exposed ids; `config.yaml`
   keeps all 11 entries semantically.
3. agent-worktree README matches wt 0.1.0 behavior; guides 03/06 no longer
   describe it as stale (Phase 4 gated on merge).
4. `make check-links` covers `benchmarks/`, `docs/reference/`, `docs/archive/`
   and reports `ALL LINKS OK`.
5. `issues.md` fully checked off with dated notes.

## Out of scope / accepted

- Git history rewrite (accepted decision, documented note instead).
- Registering the omlx/openrouter/llama.cpp providers in modelman (requires a
  provider-registry design incl. live `secret_ref` handling — separate project).
- OpenRouter key rotation itself (user follows the manual checklist).
- Machine-specific expected outputs in the guides (accepted dated-stamp format).