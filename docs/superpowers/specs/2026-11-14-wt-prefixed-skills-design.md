# wt-prefixed skills, with repo-cleanup moved in

## Intent
`repo-cleanup` (standalone repo `ohanaverse/repo-cleanup-skill`, now a thin wrapper over `wt`) is
copied into this repo so the skill and `wt`'s JSON contracts change in one PR. While doing so, every
skill here gets a `wt-` name prefix so they group together and cannot collide with other skills.

## Goals
- One canonical `SKILL.md` per skill, all named `wt-<name>` (directory name == `name:` field).
- Works on every Agent Skills host: Claude Code, Pi, Codex.
- `wt-repo-cleanup` is installable globally (it runs in any repo); the rest stay repo-local.

## Non-goals / anti-goals
- No plugin (`/wt:name` needs one; Pi and Codex cannot read Claude plugins).
- The other repo is not edited, deleted or synced back. Its README, INSTALL, `plugin.json` and the
  shell-era specs are not copied; their still-true content folds into guide 08 section 9.

## Approaches considered
1. Plugin named `wt` -> `/wt:repo-cleanup`. Rejected: breaks Pi/Codex, project skills stop auto-loading.
2. Prefixed skill names, `.claude/skills/wt-*/` (chosen by user). Same file works on all hosts.
3. Nested `wt/.claude/skills` directory scoping. Rejected: unverified, only loads under `wt/`.

## Design
- Rename: `manage-slot-profiles`, `adding-a-benchmark-backend`, `mlx-lm-quantization`,
  `sentinel-failure` (in `.claude/skills/`) and `adding-a-provider`, `adding-a-wt-agent`, `cloud-sync`
  (in `wt/.claude/skills/`, which stay where they are) to `wt-<name>`;
  update each `name:` and every reference (CLAUDE.md files, guides, `wt/cmd/wt/cloudsync.go`, tests).
- Add `.claude/skills/wt-repo-cleanup/SKILL.md`, copied from the other repo at commit 3ae75af, with
  `name:` changed to `wt-repo-cleanup` and its `/repo-cleanup` heading updated.
- Root Makefile: `install-skill-claude|pi|codex` copy only `wt-repo-cleanup` to
  `~/.claude/skills/`, `~/.agents/skills/`, `~/.codex/skills/`, first removing the old
  `repo-cleanup` copy there; matching `uninstall-skill-*`. A check in `make lint` asserts each
  `SKILL.md` frontmatter name equals its directory name and starts with `wt-`.
- Docs: CLAUDE.md skill list and guide 08 section 9 name the new skill and install targets.

## Left to the builder
Exact Makefile target wiring into `make install`; whether the frontmatter check is shell or Python.
Calls made without asking: the `wt-` prefix applies to `cloud-sync` too ("all skills"); old global
`repo-cleanup` copies are removed on install.
