# Follow-up Issues Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close all open items in `issues.md` (rotation/redaction, wt README drift, modelman bookkeeping, link-check tooling, and two minor notes) with security-first sequencing and one commit per phase.

**Architecture:** The work is split into five sequential phases on one branch in `local-ai-setup`, plus a parallel branch/PR in the `agent-worktree` repo for the README fix. Machine state changes (plist, config.yaml) happen before the doc redactions that describe them. Verification gates between phases keep the repo from drifting back into the same bugs.

**Tech Stack:** bash, git, `openssl`, `launchctl`, `uv`, `modelman`, LiteLLM proxy, PostgreSQL (`pg_dump`/`psql`), Python 3 stdlib, `make`.

---

## Task 1: Pre-rotation safety and backups

**Files:**
- Touch: `~/Library/LaunchAgents/local.litellm.proxy.plist`
- Backup: `/tmp/plist.pre-rotation.backup`
- Backup: `/tmp/litellm-pre-rotation.sql`

- [ ] **Step 1: Capture the current plist values for later verification**

```bash
cp ~/Library/LaunchAgents/local.litellm.proxy.plist /tmp/plist.pre-rotation.backup
ls -l /tmp/plist.pre-rotation.backup
```

Expected: file exists, owned by you.

- [ ] **Step 2: Back up the LiteLLM Postgres database**

```bash
pg_dump litellm > /tmp/litellm-pre-rotation.sql
ls -lh /tmp/litellm-pre-rotation.sql
```

Expected: non-empty `.sql` file (size > 0).

- [ ] **Step 3: Confirm the database has no stored keys that salt rotation would break**

```bash
/opt/homebrew/opt/postgresql@16/bin/psql -U "$(whoami)" -h localhost -p 5432 -d litellm -c "SELECT count(*) FROM \"LiteLLM_VirtualKeyTable\";" -tA
/opt/homebrew/opt/postgresql@16/bin/psql -U "$(whoami)" -h localhost -p 5432 -d litellm -c "SELECT \"key\" FROM \"LiteLLM_UserTable\" WHERE \"key\" IS NOT NULL LIMIT 1;" -tA
```

Expected: both queries return `0` or empty. If either is non-empty, **halt**: rotating the salt key would make stored keys undecryptable. Restore from the dump, clear the stored keys first, then retry.

- [ ] **Step 4: Verify the proxy is alive with the current master key**

```bash
curl -s -o /dev/null -w "%{http_code}\n" \
  http://localhost:4000/v1/models \
  -H "Authorization: Bearer $(grep -A1 'LITELLM_MASTER_KEY' /tmp/plist.pre-rotation.backup | grep '<string>' | sed -E 's|.*<string>(.*)</string>.*|\1|')"
```

Expected: `200`.

---

## Task 2: Rotate secrets in the plist

**Files:**
- Modify: `~/Library/LaunchAgents/local.litellm.proxy.plist` (the three secret strings)

- [ ] **Step 1: Generate the new LiteLLM master key and save it outside the repo**

```bash
printf '%s' "sk-litellm-$(openssl rand -base64 32 | tr -d '\n/=' | head -c 40)" > /tmp/new_master_key.txt
chmod 600 /tmp/new_master_key.txt
echo "Master key length: $(wc -c < /tmp/new_master_key.txt)"
```

Expected: file contains one line of 52 chars starting with `sk-litellm-`.

- [ ] **Step 2: Generate the new salt key and save it outside the repo**

```bash
printf '%s' "$(openssl rand -base64 32 | tr -d '\n/=' | head -c 40)" > /tmp/new_salt_key.txt
chmod 600 /tmp/new_salt_key.txt
echo "Salt key length: $(wc -c < /tmp/new_salt_key.txt)"
```

Expected: 40-character file.

- [ ] **Step 3: Generate the new UI password and save it outside the repo**

```bash
printf '%s' "$(openssl rand -base64 18 | tr -d '\n/=' | head -c 24)" > /tmp/new_ui_password.txt
chmod 600 /tmp/new_ui_password.txt
echo "UI password length: $(wc -c < /tmp/new_ui_password.txt)"
```

Expected: 24-character file.

- [ ] **Step 4: Replace the three secret values in the live plist with `plutil`**

```bash
plutil -replace EnvironmentVariables.LITELLM_MASTER_KEY -string "$(cat /tmp/new_master_key.txt)" \
  ~/Library/LaunchAgents/local.litellm.proxy.plist
plutil -replace EnvironmentVariables.LITELLM_SALT_KEY -string "$(cat /tmp/new_salt_key.txt)" \
  ~/Library/LaunchAgents/local.litellm.proxy.plist
plutil -replace EnvironmentVariables.UI_PASSWORD -string "$(cat /tmp/new_ui_password.txt)" \
  ~/Library/LaunchAgents/local.litellm.proxy.plist
```

Leave `UI_USERNAME` as `admin`. Do **not** paste these values into any file tracked by git, into this plan, or into the chat log. Read the UI password from `/tmp/new_ui_password.txt` (or store it in your password manager).

- [ ] **Step 5: Restart the LiteLLM proxy and verify the new master key works**

```bash
launchctl kickstart -k gui/$(id -u)/local.litellm.proxy
sleep 10
curl -s -o /dev/null -w "%{http_code}\n" \
  http://localhost:4000/v1/models \
  -H "Authorization: Bearer $(cat /tmp/new_master_key.txt)"
tail -5 ~/.litellm.err.log
```

Expected: `200`, and the error log contains no fatal DB/auth errors.

- [ ] **Step 6: Test the dashboard login with the new UI password**

Open `http://localhost:4000/ui` and sign in with `admin` / the contents of `/tmp/new_ui_password.txt`. Expected: successful login.

---

## Task 3: Redact the rotated values in the repo

**Files:**
- Modify: `docs/reference/litellm-admin-ui-setup.md`
- Modify: `issues.md`

- [ ] **Step 1: Record how many copies of each leaked string exist before editing**

```bash
OLD_MASTER_FILE=/tmp/old_master_key.txt
OLD_SALT_FILE=/tmp/old_salt_key.txt
# These store only the old values; they live in /tmp and are never committed.
grep -A1 'LITELLM_MASTER_KEY' /tmp/plist.pre-rotation.backup | grep '<string>' | sed -E 's|.*<string>(.*)</string>|\1|' | tr -d '\n' > "$OLD_MASTER_FILE"
grep -A1 'LITELLM_SALT_KEY' /tmp/plist.pre-rotation.backup | grep '<string>' | sed -E 's|.*<string>(.*)</string>|\1|' | tr -d '\n' > "$OLD_SALT_FILE"
OLD_MASTER_COUNT=$(grep -oF "$(cat "$OLD_MASTER_FILE")" docs/reference/litellm-admin-ui-setup.md | wc -l)
OLD_SALT_COUNT=$(grep -oF "$(cat "$OLD_SALT_FILE")" docs/reference/litellm-admin-ui-setup.md | wc -l)
echo "master key occurrences in doc: $OLD_MASTER_COUNT"
echo "salt key occurrences in doc: $OLD_SALT_COUNT"
```

Expected: positive counts you will drive to zero.

- [ ] **Step 2: Replace every leaked master key string in the reference doc**

Use `sed` with a fixed-string replacement so the exact value never appears in a command that is saved anywhere:

```bash
sed -i.bak -e "s/$(sed 's/[]\/&$*.^|[]/\\&/g' "$OLD_MASTER_FILE")/sk-litellm-<rotate-me>/g" \
  docs/reference/litellm-admin-ui-setup.md
```

- [ ] **Step 3: Replace every leaked salt key string in the reference doc**

```bash
sed -i.bak -e "s/$(sed 's/[]\/&$*.^|[]/\\&/g' "$OLD_SALT_FILE")/<generated-salt>/g" \
  docs/reference/litellm-admin-ui-setup.md
```

- [ ] **Step 4: Replace the UI password in the reference doc, leaving the username alone**

Edit `docs/reference/litellm-admin-ui-setup.md` so the `UI_PASSWORD` value (currently `admin`) becomes `<strong-password>`, while `UI_USERNAME` stays `admin`.

Example old/new pair in the "Step 1" xml block and the "Final plist" block:

```xml
<key>UI_PASSWORD</key>
<string>admin</string>
```

→

```xml
<key>UI_PASSWORD</key>
<string><strong-password></string>
```

- [ ] **Step 5: Update the `curl` example and the 401 troubleshooting note**

In `docs/reference/litellm-admin-ui-setup.md`, change:
- Step 10's curl `Authorization: Bearer sk-litellm-...` → `Authorization: Bearer sk-litellm-<rotate-me>`.
- The 401 troubleshooting note's bearer string → the same placeholder.

- [ ] **Step 6: Add the history-accepted note**

Append a short paragraph to the `docs/reference/litellm-admin-ui-setup.md` "Final plist" section or near the bottom:

> **Rotated 2026-08-29.** The original master key, salt key, and UI password were
> committed in this document and in `issues.md`. They were rotated on the
> machine via the LaunchAgent plist, and the repo now uses placeholders. Git
> history was accepted rather than rewritten: the leaked values are dead after
> rotation, and rewriting would have orphaned the checkout-SHA stamps in the
> maintenance guide.

- [ ] **Step 7: Redact the master key from `issues.md`**

In `issues.md`, replace the quoted master key in the issue 1 heading with the same placeholder string.

- [ ] **Step 8: Verify no leaked values remain in the repo**

```bash
# Any sk-litellm- token left must only be the placeholder.
grep -R 'sk-litellm-' docs/reference/litellm-admin-ui-setup.md issues.md | grep -v 'sk-litellm-<rotate-me>' && echo "FAIL" || echo "OK"

# Salt occurrences should equal the placeholder count.
NEW_SALT_COUNT=$(grep -oF '<generated-salt>' docs/reference/litellm-admin-ui-setup.md | wc -l)
echo "placeholder salt count: $NEW_SALT_COUNT (expected $OLD_SALT_COUNT)"
test "$NEW_SALT_COUNT" -eq "$OLD_SALT_COUNT" && echo "OK" || echo "FAIL"

# UI password should be the placeholder.
grep -n '<string><strong-password></string>' docs/reference/litellm-admin-ui-setup.md
```

Expected: all three checks report OK.

- [ ] **Step 9: Commit Phase 1**

```bash
git add docs/reference/litellm-admin-ui-setup.md issues.md
rm docs/reference/litellm-admin-ui-setup.md.bak
git commit -m "security: rotate litellm secrets and redact from docs (issue 1)"
```

---

## Task 4: Fix agent-worktree README drift (issue 2a)

**Files:**
- Modify: `~/github/ohanaverse/agent-worktree/README.md`

- [ ] **Step 1: Create the worktree branch**

```bash
cd ~/github/ohanaverse/agent-worktree
git checkout -b fix/readme-drift-wt010
```

- [ ] **Step 2: Fix the Quick start example**

In `README.md`, find:

```bash
claude-wt -w my-feature
```

Change to:

```bash
claude-wt -W my-feature
```

- [ ] **Step 3: Fix the Flags table worktree row**

Find:

```markdown
| `-w <name>`, `--worktree <name>` | Use or create a worktree for the given branch; skip TUI |
```

Change to:

```markdown
| `-W <name>`, `--worktree <name>` | Use or create a worktree for the given branch; skip TUI |
```

- [ ] **Step 4: Fix the `--cwd` description**

Find:

```markdown
| `--cwd` | Launch in the current repo root; skip TUI and session resume |
```

Change to:

```markdown
| `--cwd` | Launch in the current repo root; skips the TUI picker. A prior resume-capable session still auto-resumes. |
```

- [ ] **Step 5: Remove the `d` toggle sentence in the Flags intro**

Find the sentence (near the Model rotation paragraph in the Flags section):

```markdown
There are no `--code`/`--design`/`--native` flags. Use `d` to switch tag groups, and `up`/`down` to pick a model — launching it advances the rotation automatically for the next entry.
```

Change to:

```markdown
There are no `--code`/`--design`/`--native` flags. Use `-T <tag>` to filter to a tag group, then `up`/`down` to pick a model — launching it advances the rotation automatically for the next entry.
```

- [ ] **Step 6: Remove the `d` key bullet in the Model rotation section**

Find:

```markdown
- `d` toggles between code and design tag groups; each tag has its own rotation state
```

Remove it.

- [ ] **Step 7: Correct the rotation state description**

Find:

```markdown
- State is persisted to `~/.config/agent-wt/rotation-<tag>.state` (one line: the last-launched model ID)
- Legacy 2-line state files (index + ID) are still read correctly via the last non-empty line
```

Change to:

```markdown
- State is persisted to `~/.config/agent-wt/rotation.state` (single global slot: the last-launched model ID)
- Legacy per-tag `rotation-<tag>.state` files are one-shot migration inputs; they are merged into the global slot on first run and then deleted
```

Also remove the bullet `each tag has its own rotation state` if it still appears.

- [ ] **Step 8: Audit for other stale instances**

```bash
cd ~/github/ohanaverse/agent-worktree
grep -n '\b-w ' README.md | grep -v '\-W'
grep -n ' rotation-' README.md
grep -n 'toggle.*tag' README.md
```

Expected: only the new, correct references remain; anything matching the old claims is fixed as part of this task, not deferred.

- [ ] **Step 9: Commit, push, and open PR**

```bash
git add README.md
git commit -m "docs: fix README drift vs wt 0.1.0 (--cwd resume, -W flag, d toggle, rotation state)"
git push -u origin fix/readme-drift-wt010
```

Open a PR in the `agent-worktree` repo. **Do not merge it yourself** — the next task in `local-ai-setup` depends on it.

---

## Task 5: Modelman convergence (issue 3)

**Files:**
- Backup: `/tmp/config.yaml.pre-expose`
- Touch machine-side: `~/.config/litellm/config.yaml`, `~/.config/local-ai/modelman.toml`

- [ ] **Step 1: Back up the LiteLLM config**

```bash
cp ~/.config/litellm/config.yaml /tmp/config.yaml.pre-expose
ls -lh /tmp/config.yaml.pre-expose
```

- [ ] **Step 2: Expose the two in-registry models**

```bash
cd ~/github/ohanaverse/modelman
uv run modelman expose ollama/qwen3.8:27b-mlx
uv run modelman expose ollama/ornith-1.5:35b
```

Expected: both commands exit 0.

- [ ] **Step 3: Diff and sanity-check the config rewrite**

```bash
diff /tmp/config.yaml.pre-expose ~/.config/litellm/config.yaml
```

Expected: only the two ollama rows are reshaped and comment banners/formatting are lost. If any foreign row (omlx/openrouter/llama.cpp) moved semantically or disappeared, stop and restore the backup.

- [ ] **Step 4: Verify the two modelman flags flipped**

```bash
grep -A2 'ollama/qwen3.8:27b-mlx' ~/.config/local-ai/modelman.toml | grep 'litellm_exposed'
grep -A2 'ollama/ornith-1.5:35b' ~/.config/local-ai/modelman.toml | grep 'litellm_exposed'
```

Expected: both lines read `litellm_exposed = true`.

- [ ] **Step 5: Restart and verify LiteLLM still serves all 11 entries**

```bash
launchctl kickstart -k gui/$(id -u)/local.litellm.proxy
sleep 10
curl -s -H "Authorization: Bearer $(grep -A1 'LITELLM_MASTER_KEY' ~/Library/LaunchAgents/local.litellm.proxy.plist | grep '<string>' | sed -E 's|.*<string>(.*)</string>|\1|')" \
  http://localhost:4000/v1/models | python3 -m json.tool | grep '"id"' | wc -l
```

Expected: `11`.

- [ ] **Step 6: Clean up the backup**

```bash
rm /tmp/config.yaml.pre-expose
```

---

## Task 6: Downshift modelman drift notes in the guides

**Files:**
- Modify: `docs/guides/00-config-map.md`
- Modify: `docs/guides/02-providers-and-models.md`
- Modify: `docs/guides/04-litellm-config.md`
- Modify: `docs/guides/08-maintenance-and-troubleshooting.md`

- [ ] **Step 1: Update ownership note in `00-config-map.md`**

Find the section that describes `~/.config/litellm/config.yaml` and add:

```markdown
- **Hand-managed entries:** modelman owns only the two in-registry `ollama/*` models; the 9 omlx/openrouter/llama.cpp entries are deliberately hand-managed.
```

- [ ] **Step 2: Downshift the drift paragraph in `02-providers-and-models.md`**

Locate the paragraph referenced by `02:264` (around the `config.yaml` shape and modelman bookkeeping). Replace the drift complaint with a historical footnote:

```markdown
> **Historical note (2026-08-29):** `modelman.toml` flags were out of sync because the non-ollama entries were seeded outside modelman. The two in-registry ollama models are now modelman-exposed; the nine omlx/openrouter/llama.cpp entries remain hand-managed by design.
```

- [ ] **Step 3: Downshift the drift paragraph in `04-litellm-config.md`**

Same treatment near `04:286`; also update the redacted `config.yaml` shape block if the expose command changed its formatting or banner state. The note should now read as historical, not current drift.

- [ ] **Step 4: Downshift the drift paragraph in `08-maintenance-and-troubleshooting.md`**

Same historical footnote near `08:176`.

- [ ] **Step 5: Commit Phase 3**

```bash
git add docs/guides/00-config-map.md docs/guides/02-providers-and-models.md docs/guides/04-litellm-config.md docs/guides/08-maintenance-and-troubleshooting.md
git commit -m "docs: downshift modelman bookkeeping drift notes (issue 3)"
```

---

## Task 7: Prune README-drift caveats (issue 2b) — merge-gated

**Files:**
- Modify: `docs/guides/03-model-families.md`
- Modify: `docs/guides/06-wt-agents-and-models.md`

- [ ] **Step 1: Confirm the agent-worktree README PR merged**

```bash
cd ~/github/ohanaverse/agent-worktree
git fetch origin
git log --oneline origin/main | head -5 | grep -c 'fix README drift'
```

Expected: `1` (the merge commit is visible). If `0`, **skip this entire task**, note the dependency in `issues.md`, and move on.

- [ ] **Step 2: Prune the stale-README bullet in `03-model-families.md`**

Find the bullet that says the README is stale in two places. Keep the facts:

```markdown
- **TUI behavior:** there is no `d` tag-toggle key, `rotation.state` is a single global slot, and per-tag `rotation-<tag>.state` files are legacy migration inputs that are deleted after migration.
```

Remove any line-number callouts or "fix README" instructions.

- [ ] **Step 3: Prune the `-w`/`-W` paragraph in `06-wt-agents-and-models.md`**

Keep the true claim that `-W` is the real flag; remove the "README example showing `-w` is wrong" wording.

- [ ] **Step 4: Prune the `d` key bullet**

Change the "NO `d` key" bullet to a plain statement:

```markdown
- **There is no `d` tag-toggle key.** Use `-T code` / `-T design` instead; picker footer is `[↑/↓] navigate [enter] launch [q] quit`.
```

- [ ] **Step 5: Prune the `--cwd` bullet blame clause**

Keep the auto-resume explanation; remove the "README:75 wrongly claims" clause.

- [ ] **Step 6: Prune the Going deeper parenthetical**

Remove the parenthetical in `06-wt-agents-and-models.md` that warns about stale README spots.

- [ ] **Step 7: Verify the framing is gone**

```bash
grep -n 'README is stale\|README.*wrongly\|README example.*-w\|mind the stale' docs/guides/03-model-families.md docs/guides/06-wt-agents-and-models.md
```

Expected: no matches.

- [ ] **Step 8: Commit Phase 4**

```bash
git add docs/guides/03-model-families.md docs/guides/06-wt-agents-and-models.md
git commit -m "docs: prune wt README-drift caveats now that README is fixed (issue 2b)"
```

---

## Task 8: Extract link-check script + Makefile target + minors

**Files:**
- Create: `bin/check-links`
- Modify: `Makefile`
- Modify: `docs/superpowers/plans/2026-08-29-user-guides.md`
- Modify: `docs/guides/05-benchmarks.md`
- Modify (maybe): `CLAUDE.md`

- [ ] **Step 1: Demonstrate the old snippet misses directories**

```bash
python3 - <<'EOF'
import pathlib
print("benchmarks md:", len(list(pathlib.Path("benchmarks").glob("*.md"))))
print("docs/reference md:", len(list(pathlib.Path("docs/reference").glob("*.md"))))
print("docs/archive md:", len(list(pathlib.Path("docs/archive").glob("*.md"))))
EOF
```

Expected: positive counts, proving the old `targets` list skipped those dirs.

- [ ] **Step 2: Create `bin/check-links`**

Create `bin/check-links`:

```python
#!/usr/bin/env python3
"""Check repo-relative markdown links across all doc directories."""
import re
import pathlib
import sys
import urllib.parse

TARGETS = [
    pathlib.Path("README.md"),
    pathlib.Path("CLAUDE.md"),
    *pathlib.Path("docs/guides").glob("*.md"),
    *pathlib.Path("docs/reference").glob("*.md"),
    *pathlib.Path("docs/archive").glob("*.md"),
    *pathlib.Path("benchmarks").glob("*.md"),
]

broken = []
for p in TARGETS:
    text = p.read_text()
    for m in re.finditer(r"]\(([^)#\s]+)(?:#[^)]*)?\)", text):
        target = urllib.parse.unquote(m.group(1))
        if target.startswith(("http", "mailto", "https")):
            continue
        if not (p.parent / target).exists():
            broken.append(f"{p}: {target}")

if broken:
    print("\n".join(broken))
    sys.exit(1)
print("ALL LINKS OK")
```

Then:

```bash
chmod +x bin/check-links
```

- [ ] **Step 3: Add `check-links` to the Makefile**

Modify `Makefile` to add `.PHONY: lint lint-shell check-links` and:

```makefile
check-links:
	@bin/check-links
```

- [ ] **Step 4: Replace the inline snippet in the plan doc**

In `docs/superpowers/plans/2026-08-29-user-guides.md`, replace the inline python snippet under "Verification tooling → Link check" with:

```markdown
Run `make check-links` from repo root. Expected: `ALL LINKS OK`.
```

- [ ] **Step 5: Update `CLAUDE.md` if it references the old link-check snippet**

Check whether `CLAUDE.md` contains the old inline python snippet. If yes, replace it with a pointer to `make check-links`.

- [ ] **Step 6: Fix health-probe endpoints in `05-benchmarks.md`**

Find the probe block around lines 162–163:

```bash
curl -s -m 2 http://localhost:8000/v1/models -o /dev/null -w "8000(omlx):%{http_code}\n"
curl -s -m 2 http://localhost:8080/v1/models -o /dev/null -w "8080(llama.cpp):%{http_code}\n"
```

Change to:

```bash
curl -s -m 2 http://localhost:8000/health -o /dev/null -w "8000(omlx):%{http_code}\n"         # /health — plain / gives 404
curl -s -m 2 http://localhost:8080/health -o /dev/null -w "8080(llama.cpp):%{http_code}\n"
```

- [ ] **Step 7: Fix the wording at `05-benchmarks.md` line 96**

Find:

```markdown
Family names come from `family =` in `~/.config/local-ai/registry.toml` — they currently mirror per-model tags (`qwen3.8:27b-mlx`, `ornith-1.5:35b`, …), so a bare `--family qwen3.8` matches nothing.
```

Change `per-model tags` to `per-model ids`.

- [ ] **Step 8: Run the new link checker**

```bash
make check-links
```

Expected: `ALL LINKS OK`.

- [ ] **Step 9: Commit Phase 5**

```bash
git add bin/check-links Makefile docs/superpowers/plans/2026-08-29-user-guides.md docs/guides/05-benchmarks.md
git add CLAUDE.md  # if changed
git commit -m "tooling: widen link-check to benchmarks/reference/archive + fix health probes and wording (issues 4, minors)"
```

---

## Task 9: Bookkeeping and close-out

**Files:**
- Modify: `issues.md`

- [ ] **Step 1: Check off all items and add dated notes**

Update `issues.md` so every open checkbox becomes `[x]` with a short note:
- Issue 1: "Rotated 2026-08-29; redacted in docs; history accepted."
- Issue 2: "README fixed in agent-worktree PR <link>; caveats pruned (or skipped if PR unmerged)."
- Issue 3: "Two in-registry ollama models exposed; 9 foreign entries declared hand-managed; drift notes downshifted."
- Issue 4: "Link-check script widened to benchmarks/docs/reference/docs/archive."
- Minors: "Health probes converged on `/health`; 05:96 wording fixed."

- [ ] **Step 2: Run the final link check**

```bash
make check-links
```

Expected: `ALL LINKS OK`.

- [ ] **Step 3: Run the final secret-occurrence gate**

```bash
# Ensure the only sk-litellm token in tracked files is the placeholder.
grep -R 'sk-litellm-' docs issues.md | grep -v 'sk-litellm-<rotate-me>' && echo "LEAK" || echo "OK"

# Ensure the UI password is a placeholder.
grep -n '<string><strong-password></string>' docs/reference/litellm-admin-ui-setup.md
```

Expected: `OK`, and two placeholder UI password lines exist.

- [ ] **Step 4: Commit Phase 6 and push the branch**

```bash
git add issues.md
git commit -m "chore: check off follow-up issues and close the tracker"
git push -u origin fix/followup-issues
```

- [ ] **Step 5: Open the PR**

Open a PR for `fix/followup-issues → main` in the `local-ai-setup` repo. Include a summary referencing `docs/superpowers/specs/2026-08-29-followup-issues-design.md`.

---

- [ ] **Step 7: Clean up the temporary secret files**

```bash
rm -f /tmp/old_master_key.txt /tmp/old_salt_key.txt /tmp/new_master_key.txt /tmp/new_salt_key.txt /tmp/new_ui_password.txt
rm -f /tmp/plist.pre-rotation.backup /tmp/litellm-pre-rotation.sql
```

Expected: no leftover pre-rotation or post-rotation secret material in `/tmp`.

## OpenRouter follow-up (user executes manually after the PR lands)

Because the OpenRouter API key was never committed to the repo, rotation is
outside the git plan but worth doing for a clean slate. Steps:

1. Revoke the current key and generate a new one at `https://openrouter.ai/keys`.
2. Update `OPENROUTER_API_KEY` in `~/Library/LaunchAgents/local.litellm.proxy.plist`.
3. Update the four `api_key:` values in `~/.config/litellm/config.yaml`.
4. Update the `secret_ref` for the OpenRouter provider in `~/.config/local-ai/registry.toml`.
5. `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`.
6. Verify one OpenRouter-routed call returns 200.

This checklist is not automated because the new key must transit only through
the OpenRouter UI and local files, never through this repo.
