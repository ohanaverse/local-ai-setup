# User Guide Set for local-ai-setup — Design

- **Date:** 2026-08-29
- **Status:** Approved design, pending implementation plan
- **Branch:** `docs/user-guides` (off `main`)

## Problem

`local-ai-setup` is the entry point for the three-repo local AI stack
(`local-ai-setup`, `modelman`, `agent-worktree`), but current documentation is
scattered: one dated monolithic setup doc, four saved reference articles, and
authoritative material trapped in sibling repos' READMEs, specs, and `CLAUDE.md`.
Common operations have no task-oriented playbook, and none of the docs are
structured for reliable consumption by an LLM agent or skill.

## Audience

1. **Primary:** Keith — terse personal playbook; assumes the existing setup,
   real paths, no hand-holding.
2. **Secondary:** LLM models and skills using the guides as reference — guides
   must be unambiguous and machine-consumable (exact commands, explicit paths,
   expected outputs, no implied context).
3. **Tertiary:** other developers — must be able to follow along without
   tribal knowledge.

## Scope

**In:**

- Nine new playbook files under `docs/guides/` (see inventory below).
- A config-file map appendix covering all config surfaces in the stack.
- README restructured as a thin index pointing at the guides.
- Doc hygiene: dated/absorbed material relocated (archive / reference).
- `CLAUDE.md` updated to point agents at `docs/guides/` as the canonical playbook.

**Out (explicitly):**

- Any changes to the `modelman` or `agent-worktree` repos.
- Generated CLI reference pages (approach C — parked; can be bolted on later).
- Rewriting legacy benchmark write-ups (`benchmarks/*.md` stays as-is).
- New features in any tool; docs only. If a guide reveals a tool gap, it is
  recorded as a note, not fixed in this effort.

## Guide inventory

Nine files under `docs/guides/`, numbered by lifecycle order:

| File | Covers | Source material |
|---|---|---|
| `00-config-map.md` | Every config file: `~/.config/local-ai/registry.toml`, `modelman.toml`, `settings.yaml`, `~/.config/litellm/config.yaml`, `~/.config/agent-wt/*` (`config.toml`, `themes.toml`, `usage.jsonl`, `rotation.state`), LaunchAgent plists. Owner of each file, readers/writers, env overrides. | Inspect real `~/.config/local-ai/`, `~/.config/agent-wt/`, `~/Library/LaunchAgents/`, `~/.config/litellm/` trees; modelman/agent-worktree READMEs |
| `01-initial-setup.md` | Fresh-machine setup: LiteLLM proxy (`uv tool install 'litellm[proxy]'`), Ollama, llama.cpp, oMLX, OpenRouter + HF auth, PostgreSQL + Redis, LaunchAgents, end-to-end smoke test. **Absorbs `docs/Local AI Setup 2026-08-25.md`.** | Archived setup doc; `docs/reference/` articles |
| `02-providers-and-models.md` | Adding/editing providers & models in modelman TUI and non-interactively (local + cloud), auth `secret_ref`s, HF-backed downloads (`[models.fetch]`), `modelman sync` reconcile commands | `modelman/README.md`, sync design specs |
| `03-model-families.md` | Family concept and semantics, display names (`modelman.toml` `[families]`), code/design tag groups, how families/tags drive the `wt` model picker and rotation | modelman specs; agent-worktree registry-consumer spec |
| `04-litellm-config.md` | `~/.config/litellm/config.yaml` anatomy, modelman exposure flags → `model_list` entries, hand-editing vs modelman, admin UI (web dashboard) — **absorbs `docs/litellm-admin-ui-setup.md`** — restarting via LaunchAgent | Admin UI doc; live config; existing LiteLLM reference articles |
| `05-benchmarks.md` | `modelman benchmark run --family/--model`, provider isolation (`bin/llm-isolate-provider`, `bin/llm-restore-providers`), multi-pass runs, results archiving into `benchmarks/results/` | modelman benchmark spec; this repo's `CLAUDE.md`; `bin/` scripts |
| `06-wt-agents-and-models.md` | `wt config` (agents, default rotation tag), provider/model selection, rotation state, `*-wt` shims, flags, session resume support matrix | `agent-worktree/README.md`, `docs/configuration.md`, native-unification spec |
| `07-usage-and-spend.md` | `modelman usage report --days N`, reconciliation (matched / WT-only / LiteLLM-only), reading the Markdown report, `rotation.state` last-launched | modelman usage-design spec |
| `08-maintenance-and-troubleshooting.md` | Post-reboot status checks, restarting each backend (per-backend stop/start mechanics), log locations, "model missing from LiteLLM" debug flow, upgrading modelman / wt / LiteLLM | This repo's `CLAUDE.md` gotchas; LaunchAgent plists; Makefiles in sibling repos |

The split of topics 2 and 3 (families vs provider/model configs) is deliberate:
`02` is registry CRUD, `03` is organizing models into groups and understanding
how those groups are consumed downstream.

## Layout and migration

```
docs/
├── guides/                     # NEW: canonical playbooks (nine files above)
├── reference/                  # kept: 4 deep-dive articles + admin-ui doc (moved here)
├── archive/                    # NEW: dated material kept for history
│   └── Local AI Setup 2026-08-25.md
├── superpowers/                # unchanged
└── (benchmarks/ tree unchanged)
```

- `Local AI Setup 2026-08-25.md` → condensed into `01`, original moved to
  `docs/archive/`.
- `docs/litellm-admin-ui-setup.md` → absorbed as a section of `04`, original
  file moved to `docs/reference/`.
- `README.md` → thin index: guide table (file / use-when / one-liner) + install
  quick start. Content the guides own is removed from README, not duplicated.
- `CLAUDE.md` → gains a pointer making `docs/guides/` the canonical playbook.

## Guide skeleton

Every guide uses the same structure:

1. **Title + "Use this to…"** — one-line statement of the operation.
2. **Prerequisites** — what must be installed/running first (with links to
   earlier guides where relevant).
3. **TL;DR** — the complete operation as one copy-paste fenced block.
4. **Steps** — each step: command in a fenced block + *expected output*
   ("you should see…").
5. **Verification** — how to confirm success after the fact.
6. **Gotchas** — drawn from `CLAUDE.md` and lived experience (MLX isolation,
   per-backend stop mechanics, oMLX 4-bit/6-bit variant naming, shebang
   conventions, etc.).
7. **Going deeper** — links to `docs/reference/` and the owning repo's docs.

## LLM-consumability conventions

- Absolute paths always (`/Users/keith/...` or `~/.config/...`) — never
  "the config file mentioned above."
- Every command in a fenced code block, with the source tool named
  (e.g., `# run from: ~/github/ohanaverse/modelman`).
- Explicit expected outputs for every non-trivial command.
- Header stamp: `Verified against: modelman vX, wt vY on YYYY-MM-DD`.
- No prose-only instructions: anything an agent must *do* appears as a command,
  file path, or keypress table.

## Accuracy and verification policy

Every command is verified against the installed tools while drafting: run
`--help` variants, read real config files, `ls` real data directories, run
read-only subcommands (`modelman sync` in dry forms where safe, `wt --check-guard`,
status checks). Commands that cannot be verified safely (destructive: deleting
models, stopping services, `launchctl unload`) are marked `<!-- UNVERIFIED -->`
inline. Where source documents contradict reality, reality wins and the
discrepancy is recorded in the guide's Gotchas section.

## Testing (for a docs project)

1. **Verification pass** as described above — logged per guide.
2. **Link check:** every relative link in `docs/guides/`, `README.md`, and
   `CLAUDE.md` resolves to an existing file/anchor.
3. **Cold read:** each TL;DR block read start-to-finish as if executing on a
   broken state — prerequisites must not be referenced implicitly.

## Workflow

- Design doc committed to `docs/user-guides` first, then implementation on the
  same branch (docs only, low merge risk).
- Suggested commit granularity: one commit per guide set change is fine; final
  commit contains README + CLAUDE.md restructure.
- PR title: `docs: user guide playbook set for local-ai-setup`.

## Success criteria

- All nine guides exist, follow the skeleton, and pass the three checks above.
- README is a thin index; the dated setup doc and admin-UI doc are archived /
  relocated without broken links.
- `CLAUDE.md` points agents at `docs/guides/`.
- Guides reference sibling repos only via exact paths (e.g.,
  `~/github/ohanaverse/modelman`), not relative guesses.