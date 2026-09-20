# Remove the Graphify skill from Pi startup

Date: 2026-09-20
Status: approved, pending implementation plan

## Goal

Remove the user-level Graphify skill installation so Pi no longer reports
duplicate `graphify` skill collisions during startup.

## Context

Pi's global settings in `~/.pi/agent/settings.json` include
`~/.agents/skills` in the `skills` discovery list. That directory currently
contains `graphify/`, with multiple root Markdown skill definitions declaring
the same name:

- `skill-agents.md` is selected as the auto-loaded `graphify` skill.
- `skill-aider.md`, `skill-amp.md`, `skill-claw.md`, and the other
  `skill-*.md` files are skipped as collisions.

The Graphify directory also contains its CLI implementation and always-on
instructions. The repository is clean and contains no Graphify-generated output
that needs to be retained.

## Design

### Removal boundary

Delete exactly this user-level directory:

```text
~/.agents/skills/graphify
```

Do not modify:

- `~/.agents/skills` or any sibling skill directory
- `~/.pi/agent/settings.json`
- repository files
- generated `graphify-out/` data, if present elsewhere

Deleting the complete directory is preferred to deleting only selected skill
files because it removes every Graphify discovery entry and its supporting
files consistently.

### Safety checks

Before deletion:

1. Resolve the target to the expected absolute path under
   `/Users/keith/.agents/skills`.
2. Confirm the target is a real directory and not a symbolic link.
3. If the target is already absent, treat removal as an idempotent success.
4. If the target is a file, symlink, or unexpected path type, stop without
   modifying it.

Use a path-scoped removal command with the exact target. Do not use a broad
pattern or operate on the parent `~/.agents/skills` directory.

### Expected behavior

After removal, Pi's skill scan finds no Graphify definitions under the
configured skill root. The startup header and skill conflict diagnostics should
therefore contain no `graphify` collision entries. Existing Pi settings remain
unchanged, so no configuration migration or restart-time setting update is
required.

### Verification

Verify all of the following after removal:

- `/Users/keith/.agents/skills/graphify` is absent.
- No `graphify` skill definition remains under
  `/Users/keith/.agents/skills`.
- A Pi startup or equivalent skill-discovery run completes without the
  `[Skill conflicts]` Graphify warning.
- The repository remains clean apart from this committed specification.

No repository code tests are required because this change is confined to the
user-level skill installation.

## Out of scope

- Removing or changing Pi's global skill discovery configuration.
- Removing Graphify data or output outside the skill directory.
- Removing any other user-level skills.
- Changing repository code or documentation.
