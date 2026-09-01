> **Using the `superpowers:writing-plans` skill to create the implementation plan.**

# Agents Smoke Script Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `scripts/agents-smoke.sh` and `make test-agents` so every wt agent can be verified end-to-end with a configurable one-shot model matrix.

**Architecture:** A single self-contained bash script holds the test matrix as pipe-delimited rows; a generic runner substitutes a unique sentinel prompt, invokes `wt --cwd -A <agent> [-M <model>] -- <one-shot-args>`, and judges PASS/FAIL/SKIP by exit code + stdout. `Makefile` gains a `test-agents` target and extends `lint`/`format`/`format-check` to the new script.

**Tech Stack:** bash 4+, `wt` (installed), `shellcheck`, `shfmt` (optional), agent CLIs (claude, codex, copilot, opencode, pi, agy). macOS-compatible timeouts via `gtimeout` or `perl alarm` fallback.

**Branch:** `agents-smoke-script` (already cut from `main`).

---

## File Structure

- **Create:** `scripts/agents-smoke.sh` — config table, CLI, runner, reporter.
- **Modify:** `Makefile` — add `test-agents` target; include `scripts/agents-smoke.sh` in `lint`, `format`, `format-check`.
- **Modify:** `docs/wt-agents/litellm-troubleshooting.md` — replace hand-run matrix notes with `make test-agents` reference.
- **Modify:** `CLAUDE.md` — add `make test-agents` to the smoke-test section.

---

### Task 1: Skeleton + `--list`

**Files:**
- Create: `scripts/agents-smoke.sh`

Add the shebang, safety options, default constants, and the embedded matrix table. Implement only `--list` and `-h`.

- [ ] **Step 1: Write the script skeleton**

```bash
#!/usr/bin/env bash
# agents-smoke.sh — live one-shot smoke test for every wt agent.

set -o nounset -o pipefail

DEFAULT_TIMEOUT=180
DEFAULT_OLLAMA_MODEL="ollama/glm-5.3-flash:cloud"

# agent|model|one-shot-args-template|comment
# model "-" means launch without -M.
# @PROMPT@ is replaced with the row's prompt (a single shell-quoted argument).
read -r -d '' MATRIX <<'EOF' || true
claude|claude/native|-p @PROMPT@|own subscription, no model args
claude|DEFAULT_OLLAMA_MODEL|-p @PROMPT@|gateway round-trip
codex|DEFAULT_OLLAMA_MODEL|exec @PROMPT@|copilot|copilot/native|-p @PROMPT@|own subscription
copilot|DEFAULT_OLLAMA_MODEL|-p @PROMPT@|
opencode|DEFAULT_OLLAMA_MODEL|run @PROMPT@|
pi|DEFAULT_OLLAMA_MODEL|-p @PROMPT@|
agy|-|-p @PROMPT@|default model, no -M
shell|-|echo @PROMPT@|-- passthrough becomes argv
EOF

usage() {
  cat <<'USAGE'
Usage: scripts/agents-smoke.sh [options]
  --list          Print the test matrix and exit
  --dry-run       Print the wt command for each row and exit
  --only AGENTS   Comma-separated agents to run (default all)
  --timeout SECS  Per-row timeout (default 180)
  -h              Show this help
USAGE
}

list_rows() {
  printf '%-10s %-30s %s\n' "AGENT" "MODEL" "COMMENT"
  while IFS='|' read -r agent model args comment; do
    [[ "$agent" =~ ^# ]] && continue
    [[ -z "$agent" ]] && continue
    model="${model/DEFAULT_OLLAMA_MODEL/$DEFAULT_OLLAMA_MODEL}"
    printf '%-10s %-30s %s\n' "$agent" "$model" "$comment"
  done <<<"$MATRIX"
}

main() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --list) list_rows; exit 0 ;;
      -h|--help) usage; exit 0 ;;
      *) echo "unknown arg: $1" >&2; usage >&2; exit 1 ;;
    esac
  done
  list_rows
}

main "$@"
```

- [ ] **Step 2: Make it executable and run `--list`**

```bash
chmod +x scripts/agents-smoke.sh
./scripts/agents-smoke.sh --list
```

Expected: 9 rows printed (claude×2, copilot×2, codex, opencode, pi, agy, shell).

- [ ] **Step 3: Commit**

```bash
git add scripts/agents-smoke.sh
git commit -m "feat(scripts): add agents-smoke.sh skeleton + --list"
```

---

### Task 2: Option parsing + `--only`

**Files:**
- Modify: `scripts/agents-smoke.sh`

Add parsing for `--only` and `--timeout`, plus a filter helper used by `--list`.

- [ ] **Step 1: Add option vars and parse loop**

Insert above `main()`:

```bash
ONLY_AGENTS=""
TIMEOUT="$DEFAULT_TIMEOUT"

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --list) list_rows; exit 0 ;;
      --dry-run) dry_run; exit 0 ;; # defined in Task 3
      --only) ONLY_AGENTS="$2"; shift 2 ;;
      --timeout) TIMEOUT="$2"; shift 2 ;;
      -h|--help) usage; exit 0 ;;
      *) echo "unknown arg: $1" >&2; usage >&2; exit 1 ;;
    esac
  done
}

should_run_agent() {
  local agent="$1"
  [[ -z "$ONLY_AGENTS" ]] && return 0
  # comma-delimited match
  [[ ",${ONLY_AGENTS}," == *",${agent},"* ]]
}

list_rows() {
  printf '%-10s %-30s %s\n' "AGENT" "MODEL" "COMMENT"
  while IFS='|' read -r agent model args comment; do
    [[ "$agent" =~ ^# ]] && continue
    [[ -z "$agent" ]] && continue
    should_run_agent "$agent" || continue
    model="${model/DEFAULT_OLLAMA_MODEL/$DEFAULT_OLLAMA_MODEL}"
    printf '%-10s %-30s %s\n' "$agent" "$model" "$comment"
  done <<<"$MATRIX"
}
```

Change `main()` to call `parse_args "$@"` before doing anything else.

- [ ] **Step 2: Test filtering**

```bash
./scripts/agents-smoke.sh --only claude --list
./scripts/agents-smoke.sh --only claude,codex --list
```

Expected: first prints 2 rows, second prints 3 rows.

- [ ] **Step 3: Commit**

```bash
git add scripts/agents-smoke.sh
git commit -m "feat(scripts): --only filtering for agents-smoke.sh"
```

---

### Task 3: `--dry-run` command preview

**Files:**
- Modify: `scripts/agents-smoke.sh`

Build the exact `wt` command array per row and print it without running. This validates the matrix-to-command translation before any agent launches.

- [ ] **Step 1: Add dry-run and command builder**

Insert:

```bash
build_prompt() {
  local agent="$1" runid="$2"
  echo "Reply with exactly this text and nothing else: WT-SMOKE-${agent}-${runid}"
}

build_cmd() {
  local agent="$1" model="$2" args_template="$3" prompt="$4"
  local cmd=(wt --cwd -A "$agent")
  if [[ "$model" != "-" ]]; then
    cmd+=(-M "$model")
  fi
  cmd+=(--)
  # Replace @PROMPT@ with the prompt as a single shell-quoted word, then eval-expand the template.
  local prompt_quoted
  prompt_quoted=$(printf '%q' "$prompt")
  local args_str="${args_template//@PROMPT@/$prompt_quoted}"
  eval "local extra=( $args_str )"
  cmd+=("${extra[@]}")
  printf '%s\n' "${cmd[*]}"
}

dry_run() {
  local runid
  runid="$(date +%s)-$RANDOM"
  while IFS='|' read -r agent model args_template comment; do
    [[ "$agent" =~ ^# ]] && continue
    [[ -z "$agent" ]] && continue
    should_run_agent "$agent" || continue
    model="${model/DEFAULT_OLLAMA_MODEL/$DEFAULT_OLLAMA_MODEL}"
    local prompt
    prompt=$(build_prompt "$agent" "$runid")
    echo "--- $agent × ${model/-/default} ---"
    build_cmd "$agent" "$model" "$args_template" "$prompt"
  done <<<"$MATRIX"
}
```

- [ ] **Step 2: Test dry-run**

```bash
./scripts/agents-smoke.sh --dry-run
./scripts/agents-smoke.sh --only codex --dry-run
```

Expected: printed commands contain `wt --cwd -A codex -M ollama/glm-5.3-flash:cloud -- exec 'Reply with exactly...'` and similar for other rows. No actual launches.

- [ ] **Step 3: Commit**

```bash
git add scripts/agents-smoke.sh
git commit -m "feat(scripts): --dry-run prints exact wt commands"
```

---

### Task 4: Timeout helper + shell row live run

**Files:**
- Modify: `scripts/agents-smoke.sh`

Add a macOS-safe timeout wrapper and run the easiest live row: `shell` (no gateway, no model). This proves the runner/sentinel logic end-to-end.

- [ ] **Step 1: Add timeout helper**

Insert:

```bash
with_timeout() {
  local secs="$1"; shift
  if command -v gtimeout >/dev/null 2>&1; then
    gtimeout "$secs" "$@"
  elif command -v perl >/dev/null 2>&1; then
    perl -e 'alarm shift; exec @ARGV' "$secs" "$@"
  else
    echo "warning: no gtimeout/perl; running without timeout" >&2
    "$@"
  fi
}
```

- [ ] **Step 2: Add row runner + shell-only execution**

Replace the default `main()` action from `list_rows` to a new `run_tests()` that currently only handles the `shell` agent (special-cased) and stubs others.

```bash
run_row() {
  local agent="$1" model="$2" args_template="$3" comment="$4" runid="$5"
  local prompt result status elapsed
  prompt=$(build_prompt "$agent" "$runid")
  local cmdline
  cmdline=$(build_cmd "$agent" "$model" "$args_template" "$prompt")

  local start end
  start=$(date +%s)
  result=$(with_timeout "$TIMEOUT" bash -c "$cmdline" 2>&1) && status=0 || status=$?
  end=$(date +%s)
  elapsed=$((end - start))

  local verdict="FAIL"
  if [[ $status -ne 0 ]] && [[ "$result" =~ (not installed|command not found|No such file) ]]; then
    verdict="SKIP"
  elif [[ $status -eq 0 ]] && [[ "$result" == *"WT-SMOKE-${agent}-${runid}"* ]]; then
    verdict="PASS"
  fi

  printf '[%-4s] %-10s × %-28s (%ss)\n' "$verdict" "$agent" "${model/-/default}" "$elapsed"
  if [[ "$verdict" == "FAIL" ]]; then
    echo "  command: $cmdline"
    echo "  output:"
    sed 's/^/    /' <<<"$result"
  fi
  [[ "$verdict" == "PASS" ]] && return 0 || return 1
}

run_tests() {
  local runid fail_count=0
  runid="$(date +%s)-$RANDOM"
  echo "=== Agents Smoke Run (timeout=${TIMEOUT}s, runid=${runid}) ==="
  while IFS='|' read -r agent model args_template comment; do
    [[ "$agent" =~ ^# ]] && continue
    [[ -z "$agent" ]] && continue
    should_run_agent "$agent" || continue
    model="${model/DEFAULT_OLLAMA_MODEL/$DEFAULT_OLLAMA_MODEL}"
    if [[ "$agent" == "shell" ]]; then
      run_row "$agent" "$model" "$args_template" "$comment" "$runid" && true || ((fail_count++)) || true
    else
      printf '[SKIP] %-10s × %-28s (not yet implemented)\n' "$agent" "${model/-/default}"
    fi
  done <<<"$MATRIX"
  echo "=== FAILS: $fail_count ==="
  [[ $fail_count -eq 0 ]]
}

main() {
  parse_args "$@"
  run_tests
}
```

- [ ] **Step 3: Run shell live test**

```bash
./scripts/agents-smoke.sh --only shell
```

Expected: `[PASS] shell × default (0s)` and exit 0. If FAIL, inspect output.

- [ ] **Step 4: Commit**

```bash
git add scripts/agents-smoke.sh
git commit -m "feat(scripts): shell live row + timeout helper"
```

---

### Task 5: Generic runner + full live matrix

**Files:**
- Modify: `scripts/agents-smoke.sh`

Remove the `shell`-only special case; run every row through `run_row`. Run the full matrix.

- [ ] **Step 1: Remove the agent special-case in run_tests**

Change the loop body to:

```bash
    run_row "$agent" "$model" "$args_template" "$comment" "$runid" && true || ((fail_count++)) || true
```

Remove the `if [[ "$agent" == "shell" ]]` branch entirely.

- [ ] **Step 2: Run the full matrix**

```bash
./scripts/agents-smoke.sh
```

Expected: a summary with rows for claude, codex, copilot, opencode, pi, agy, shell; PASS/SKIP/FAIL reflects live agent/gateway state. Rows that fail are printed with command + output for debugging.

- [ ] **Step 3: Iterate until all expected rows pass**

If any agent that should be green (per the 2026-08-31 matrix) fails, fix the corresponding row or driver issue before proceeding. Do not paper over real failures — the script is the oracle.

- [ ] **Step 4: Commit**

```bash
git add scripts/agents-smoke.sh
git commit -m "feat(scripts): run all agents in agents-smoke.sh"
```

---

### Task 6: Makefile wiring

**Files:**
- Modify: `Makefile`

Add the `test-agents` target and extend the shell-tool targets to include the new script.

- [ ] **Step 1: Add target and update lint/format globs**

Changes to `Makefile`:

```makefile
.PHONY: help build install uninstall check lint format format-check test test-agents clean
```

Add to `help`:

```makefile
	@echo "  test-agents - Run live one-shot agent smoke tests (requires gateways/agent CLIs)"
```

Add new target near the end:

```makefile
test-agents:            # Run live agent smoke tests
	@command -v wt >/dev/null 2>&1 || { echo "wt not on PATH; run: make install"; exit 1; }
	@scripts/agents-smoke.sh $(ARGS)

# Keep the existing lint/format targets but extend the file list.
lint:                   # Run shellcheck
	@command -v shellcheck >/dev/null 2>&1 || { echo "shellcheck not found. Install with: brew install shellcheck"; exit 1; }
	@echo "Running shellcheck..."
	shellcheck $(SRCDIR)/*-wt scripts/agents-smoke.sh
	@echo "Lint passed."

format:                 # Run shfmt (write changes)
	@command -v shfmt >/dev/null 2>&1 || { echo "shfmt not installed, skipping format"; exit 0; }
	shfmt -w -i 2 -ci $(SRCDIR)/*-wt scripts/agents-smoke.sh

format-check:           # Check formatting without modifying
	@command -v shfmt >/dev/null 2>&1 || { echo "shfmt not installed, skipping format check"; exit 0; }
	shfmt -d -i 2 -ci $(SRCDIR)/*-wt scripts/agents-smoke.sh
```

- [ ] **Step 2: Verify help, list, dry-run via make**

```bash
make help
make test-agents ARGS="--list"
make test-agents ARGS="--dry-run --only shell"
```

Expected: `help` shows `test-agents`; list and dry-run work through Make.

- [ ] **Step 3: Run lint/format**

```bash
make lint
make format-check
```

Expected: shellcheck passes; shfmt reports no diff.

- [ ] **Step 4: Commit**

```bash
git add Makefile
git commit -m "build(make): add test-agents target + lint/format the smoke script"
```

---

### Task 7: Docs updates

**Files:**
- Modify: `docs/wt-agents/litellm-troubleshooting.md`
- Modify: `CLAUDE.md`

Point readers at the new script instead of the hand-run matrix.

- [ ] **Step 1: Update `litellm-troubleshooting.md`**

Find the section that says "Matrix result (2026-08-31)". Insert after the table:

```markdown
> Re-run this matrix whenever drivers or gateway config change:
> `make test-agents`. The script uses the same one-shot sentinel check
> (exit 0 + expected string on stdout) and runs against the live config's
> current gateway mode.
```

- [ ] **Step 2: Update `CLAUDE.md`**

Find the smoke-test section near the bottom (`## Smoke test`). Append to the list:

```markdown
make test-agents        # live one-shot smoke: every agent × configured models
```

- [ ] **Step 3: Commit**

```bash
git add docs/wt-agents/litellm-troubleshooting.md CLAUDE.md
git commit -m "docs: reference make test-agents in smoke-test sections"
```

---

## Final Verification

- [ ] Run `make check` — Go tests + shellcheck/shfmt must pass.
- [ ] Run `make test-agents -- --list` — all 9 rows appear.
- [ ] Run `make test-agents -- --dry-run` — commands look correct.
- [ ] Run `make test-agents` — full live matrix produces a summary; exit 0 iff all expected rows PASS.
- [ ] `git log --oneline` shows one commit per task.

---

## Self-Review Checklist

- [x] **Spec coverage:** every spec section (matrix, pass rule, sentinel, timeouts, Makefile, docs) maps to a task.
- [x] **No placeholders:** each step has real bash/Make code or exact commands.
- [x] **Type consistency:** `runid`, `TIMEOUT`, `ONLY_AGENTS`, matrix fields used consistently across tasks.
- [x] **Scope:** single script + make wiring + docs; no unrelated work.

**Execution handoff:** Plan saved. Use `superpowers:subagent-driven-development` (fresh subagent per task with review) or `superpowers:executing-plans` (inline batch execution with checkpoints).
