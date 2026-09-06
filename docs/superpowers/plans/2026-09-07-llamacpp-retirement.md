# llama.cpp Retirement Implementation Plan (issue #33)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Retire llama.cpp as a provider (issue #33) while preserving every artifact needed to re-enable it, and capture LiteLLM/oMLX artifacts for future reference.

**Architecture:** Artifact capture happens first (verbatim copies of the LaunchAgent plists, the litellm model rows, and the registry provider block land in `docs/reference/artifacts/`), then the host is decommissioned, then the code wiring is disabled while the provider implementation is kept and marked unused, and finally every doc that describes the live stack is updated. Spec: `docs/superpowers/specs/2026-09-07-llamacpp-retirement-design.md`.

**Tech Stack:** bash (benchmark scripts, bin helpers), Python 3.13 + pytest (modelman), TOML/YAML config files, launchd, Homebrew, markdown docs.

**Working branch:** `docs/issue-33-llamacpp-retirement` (already exists, has the spec commit). All commits land here. Never commit a secret value — the litellm plist artifact must be redacted exactly as specified in Task 1.

**Host facts (verified 2026-09-07):**
- `launchctl list | grep llamacpp` → `-	1	local.llamacpp.server` (crash-looping)
- Plists: `~/Library/LaunchAgents/local.llamacpp.server.plist` and `local.llamacpp.server.plist.qwen3.8.bak`
- Logs: `~/.llamacpp.log`, `~/.llamacpp.err.log`
- litellm config `~/.config/litellm/config.yaml` has rows `llama.cpp/local-llama` and `llama.cpp/ornith-1.5-35b`
- `~/.config/local-ai/registry.toml` has a `llamacpp` `[[providers]]` block (lines 27–34) and no llamacpp models
- modelman runs from `modelman/` via `uv run` — **not** installed globally

---

### Task 1: Capture artifacts into the repo

Capture the host's llama.cpp/omlx/litellm artifacts verbatim (except the litellm plist, which is redacted) BEFORE touching the host.

**Files:**
- Create: `docs/reference/artifacts/llamacpp/local.llamacpp.server.plist` (verbatim copy)
- Create: `docs/reference/artifacts/llamacpp/local.llamacpp.server.plist.qwen3.8.bak` (verbatim copy)
- Create: `docs/reference/artifacts/omlx/homebrew.mxcl.omlx.plist` (verbatim copy)
- Create: `docs/reference/artifacts/litellm/local.litellm.proxy.plist` (redacted copy)
- Create: `docs/reference/artifacts/litellm/llamacpp-model-rows.yaml`
- Create: `docs/reference/artifacts/registry/llamacpp-provider.toml`

- [ ] **Step 1: Create the artifacts directory tree and copy verbatim plists**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
mkdir -p docs/reference/artifacts/llamacpp docs/reference/artifacts/litellm docs/reference/artifacts/omlx docs/reference/artifacts/registry
cp ~/Library/LaunchAgents/local.llamacpp.server.plist docs/reference/artifacts/llamacpp/
cp ~/Library/LaunchAgents/local.llamacpp.server.plist.qwen3.8.bak docs/reference/artifacts/llamacpp/
cp ~/Library/LaunchAgents/homebrew.mxcl.omlx.plist docs/reference/artifacts/omlx/
cp ~/Library/LaunchAgents/local.litellm.proxy.plist docs/reference/artifacts/litellm/
```

- [ ] **Step 2: Redact the five secrets from the litellm plist copy**

The live plist's `EnvironmentVariables` carries `OPENROUTER_API_KEY`, `LITELLM_MASTER_KEY`, `LITELLM_SALT_KEY`, `UI_PASSWORD`, `DATABASE_URL`. Replace each value with a named placeholder — values are never printed.

```bash
python3 - <<'EOF'
import plistlib
p = "docs/reference/artifacts/litellm/local.litellm.proxy.plist"
with open(p, "rb") as f:
    data = plistlib.load(f)
env = data["EnvironmentVariables"]
for k in ("OPENROUTER_API_KEY", "LITELLM_MASTER_KEY", "LITELLM_SALT_KEY", "UI_PASSWORD", "DATABASE_URL"):
    if k in env:
        env[k] = f"REDACTED-{k}"
with open(p, "wb") as f:
    plistlib.dump(data, f)
print("redacted:", sorted(k for k in env if k.startswith("REDACTED-")))
EOF
```

Expected output includes all five `REDACTED-<KEY>` names (fewer only if a key is genuinely absent — note any absence in the commit message).

- [ ] **Step 3: Verify no secret value survived the redaction**

```bash
grep -c "REDACTED-" docs/reference/artifacts/litellm/local.litellm.proxy.plist
grep -E "sk-or-v1|sk-" docs/reference/artifacts/litellm/local.litellm.proxy.plist || echo "no sk- values present"
```

Expected: count ≥ 4 (one per redacted key present) and the second grep must not print a value line.

- [ ] **Step 4: Extract the two llama.cpp rows from the live litellm config into an artifact**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/modelman
uv run python - <<'EOF'
import yaml
src = "/Users/keith/.config/litellm/config.yaml"
dst = "../docs/reference/artifacts/litellm/llamacpp-model-rows.yaml"
with open(src) as f:
    cfg = yaml.safe_load(f)
rows = [m for m in cfg["model_list"] if m["model_name"].startswith("llama.cpp/")]
with open(dst, "w") as f:
    yaml.dump({"model_list": rows}, f, sort_keys=False, default_flow_style=False)
print("captured rows:", [m["model_name"] for m in rows])
EOF
```

Expected: `captured rows: ['llama.cpp/local-llama', 'llama.cpp/ornith-1.5-35b']`

- [ ] **Step 5: Extract the llamacpp registry provider block into an artifact**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/modelman
uv run python - <<'EOF'
src = "/Users/keith/.config/local-ai/registry.toml"
dst = "../docs/reference/artifacts/registry/llamacpp-provider.toml"
lines = open(src).read().splitlines(keepends=True)
start = next(i for i, l in enumerate(lines)
             if l.strip() == "[[providers]]"
             and i + 1 < len(lines) and lines[i + 1].strip() == 'id = "llamacpp"')
end = next(i for i in range(start + 1, len(lines)) if lines[i].strip() == "[[providers]]")
open(dst, "w").write("".join(lines[start:end]))
print("captured block:")
print("".join(lines[start:end]))
EOF
```

Expected printed block:

```toml
[[providers]]
id = "llamacpp"
name = "llama.cpp"
location = "local"

[providers.auth]
type = "none"

```

- [ ] **Step 6: Verify artifact inventory and commit**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
find docs/reference/artifacts -type f | sort
git add docs/reference/artifacts
git commit -m "chore(artifacts): capture llama.cpp/oMLX/LiteLLM artifacts pre-retirement (issue #33)"
```

Expected `find` output (6 files):

```text
docs/reference/artifacts/llamacpp/local.llamacpp.server.plist
docs/reference/artifacts/llamacpp/local.llamacpp.server.plist.qwen3.8.bak
docs/reference/artifacts/litellm/llamacpp-model-rows.yaml
docs/reference/artifacts/litellm/local.litellm.proxy.plist
docs/reference/artifacts/omlx/homebrew.mxcl.omlx.plist
docs/reference/artifacts/registry/llamacpp-provider.toml
```

---

### Task 2: Write the provider artifacts guide

**Files:**
- Create: `docs/reference/provider-artifacts.md`

- [ ] **Step 1: Write the guide with exactly this content**

```markdown
# Provider artifacts — llama.cpp (retired), LiteLLM, oMLX

> Use this to: restore or rebuild any provider on this machine from the
> exact files it shipped with. Verified against the host on 2026-09-07.
>
> llama.cpp was **retired** on 2026-09-07 (issue #33: its LaunchAgent
> pointed at a GGUF that no longer existed and crash-looped at every
> login). Its artifacts and the full re-enable procedure are preserved
> below. LiteLLM and oMLX remain **active**; their artifacts are captured
> here so the stack can be rebuilt without archaeology.

## Artifact inventory

| Artifact | Source on host (2026-09-07) | Redacted? |
|---|---|---|
| [`artifacts/llamacpp/local.llamacpp.server.plist`](artifacts/llamacpp/local.llamacpp.server.plist) | `~/Library/LaunchAgents/local.llamacpp.server.plist` | no secrets present |
| [`artifacts/llamacpp/local.llamacpp.server.plist.qwen3.8.bak`](artifacts/llamacpp/local.llamacpp.server.plist.qwen3.8.bak) | `~/Library/LaunchAgents/local.llamacpp.server.plist.qwen3.8.bak` | no secrets present |
| [`artifacts/litellm/llamacpp-model-rows.yaml`](artifacts/litellm/llamacpp-model-rows.yaml) | the two llama.cpp rows in `~/.config/litellm/config.yaml` | rows contain only `api_key: dummy-key` |
| [`artifacts/litellm/local.litellm.proxy.plist`](artifacts/litellm/local.litellm.proxy.plist) | `~/Library/LaunchAgents/local.litellm.proxy.plist` | **yes** — 5 values replaced with `REDACTED-<KEY>` |
| [`artifacts/omlx/homebrew.mxcl.omlx.plist`](artifacts/omlx/homebrew.mxcl.omlx.plist) | `~/Library/LaunchAgents/homebrew.mxcl.omlx.plist` | no secrets present |
| [`artifacts/registry/llamacpp-provider.toml`](artifacts/registry/llamacpp-provider.toml) | `[[providers]]` block in `~/.config/local-ai/registry.toml` | no secrets present |

The litellm plist's redacted keys — re-fill each from the live plist or a
secret store at restore time: `OPENROUTER_API_KEY`, `LITELLM_MASTER_KEY`,
`LITELLM_SALT_KEY`, `UI_PASSWORD`, `DATABASE_URL`.

## llama.cpp — RETIRED 2026-09-07

State at retirement: the LaunchAgent was loaded but crash-looping (exit 1)
because its pinned GGUF had been deleted:
`~/.cache/huggingface/hub/models--unsloth--Qwen3.8-27B-GGUF/snapshots/4ca720788d1e01f1bff70c033e0d0028fd02e502/Qwen3.8-27B-UD-Q4_K_M.gguf`
(~90k "failed to load model" lines in `~/.llamacpp.err.log`). Removed on the
host: both plists, both log files, and the Homebrew formula. Disabled in the
repo: the `llamacpp` entries in the benchmark scripts, `SUPPORTED_PROVIDER_IDS`
(`modelman/src/modelman/benchmark/isolation.py`), `DEFAULT_PROVIDER_IDS`
(`modelman/src/modelman/registry.py`), and the two litellm rows. **Kept:** the
provider implementation `modelman/src/modelman/providers/llamacpp.py` (marked
UNUSED in its module docstring) and the `llamacpp` case branch in
`bin/llm-isolate-provider`.

### To re-enable llama.cpp

1. `brew install llama.cpp` (the formula was uninstalled; confirms
   `/opt/homebrew/bin/llama-server` exists).
2. Restore the LaunchAgent:
   `cp docs/reference/artifacts/llamacpp/local.llamacpp.server.plist ~/Library/LaunchAgents/`
   (or start from the template in `01-initial-setup.md`'s history — the
   artifact copy already pins `--port 8080`, `--ctx-size 16384`,
   `--n-gpu-layers 999`).
3. Re-download the pinned GGUF to the exact path the plist names:
   `hf download unsloth/Qwen3.8-27B-GGUF` — verify the snapshot hash matches
   `4ca720788d1e01f1bff70c033e0d0028fd02e502`; if HF rebased the snapshot,
   update the plist's `-m` path instead of the hash.
4. `launchctl load -w ~/Library/LaunchAgents/local.llamacpp.server.plist`,
   then check `curl -s http://localhost:8080/v1/models`.
5. Re-add the litellm rows from
   [`artifacts/litellm/llamacpp-model-rows.yaml`](artifacts/litellm/llamacpp-model-rows.yaml)
   to `~/.config/litellm/config.yaml` and
   `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`.
6. Re-add the registry block from
   [`artifacts/registry/llamacpp-provider.toml`](artifacts/registry/llamacpp-provider.toml)
   to `~/.config/local-ai/registry.toml` (with modelman not running).
7. Re-enable the code wiring (each was removed 2026-09-07 — see git history
   of this repo for the exact diffs):
   - `llamacpp` back in `DEFAULT_PROVIDER_IDS` (`modelman/src/modelman/registry.py`)
   - `llamacpp` back in `SUPPORTED_PROVIDER_IDS` (`modelman/src/modelman/benchmark/isolation.py`)
   - `llama_cpp` entries back in `benchmarks/qwen3.8-benchmark` and
     `benchmarks/ornith-1.5-benchmark` (`DIRECT_URLS`, `DIRECT_MODELS`,
     `LITELLM_MODELS`, `ISOLATE_ID`, `ISOLATE_ENV`, `display_key`,
     `ensure_all_local_started`, backend loops)
   - llamacpp `restart_launchd` line back in `bin/llm-restore-providers`
   - remove the UNUSED marker from `modelman/src/modelman/providers/llamacpp.py`
   - drop the retirement notes from `docs/guides/` and root `CLAUDE.md`

## LiteLLM — active

- Proxy: `litellm --config ~/.config/litellm/config.yaml --port 4000`, kept
  alive by `~/Library/LaunchAgents/local.litellm.proxy.plist` (artifact:
  redacted copy — see inventory for the five keys to re-fill).
- `model_list`: modelman owns exposed ids; the hand-managed rows are the 3
  omlx variants, the `openrouter/qwen/qwen3.8-*` set, and `ollama/q8` /
  `ollama/o35`. (The 2 llama.cpp rows were retired — kept in
  [`artifacts/litellm/llamacpp-model-rows.yaml`](artifacts/litellm/llamacpp-model-rows.yaml).)
- Setup/deep-dive: [01-initial-setup.md](../guides/01-initial-setup.md),
  [04-litellm-config.md](../guides/04-litellm-config.md).
- Gotcha: never commit or paste the live config/plist — real `sk-or-v1-…`
  keys live in both.

## oMLX — active

- Server on `:8000` (`omlx start` / `omlx stop`), kept alive by
  `~/Library/LaunchAgents/homebrew.mxcl.omlx.plist` (installed by Homebrew;
  artifact: verbatim copy).
- Serves both the 4-bit and 6-bit MLX variants from one service — warmup
  must name the exact variant.
- Setup: [01-initial-setup.md](../guides/01-initial-setup.md),
  backend reference: [oMLX Download and Run.md](oMLX%20Download%20and%20Run.md).
```

- [ ] **Step 2: Verify the guide's relative links resolve**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
bin/check-links
```

Expected: no new failures mentioning `provider-artifacts.md` or `artifacts/`.

- [ ] **Step 3: Commit**

```bash
git add docs/reference/provider-artifacts.md
git commit -m "docs: provider artifacts guide with llama.cpp re-enable procedure (issue #33)"
```

---

### Task 3: Disable the wiring in modelman (TDD)

Keep the provider implementation; drop llamacpp from the canonical tuple and the isolation allowlist.

**Files:**
- Modify: `modelman/src/modelman/registry.py:239`
- Modify: `modelman/src/modelman/benchmark/isolation.py:17`
- Modify: `modelman/src/modelman/providers/llamacpp.py:1`
- Modify: `modelman/tests/benchmark/test_isolation.py:43-50`
- Test: `modelman/tests/benchmark/test_isolation.py`

- [ ] **Step 1: Update the failing test**

In `modelman/tests/benchmark/test_isolation.py`, replace the test and its docstring (currently lines 43–50) with:

```python
def test_supported_provider_ids_matches_llm_isolate_providers_documented_list():
    """bin/llm-isolate-provider's own header comment ("Supported: ollama,
    omlx, omlx-6bit") is the real source of truth for what this helper may
    isolate on behalf of modelman benchmark agent. The llamacpp case branch
    is retained in the shell script but retired (issue #33, 2026-09-07 —
    see docs/reference/provider-artifacts.md), so it is not in this set; a
    backend added to the shell script's case statement without updating
    this constant is caught here rather than silently running unisolated."""
    assert frozenset({"ollama", "omlx", "omlx-6bit"}) == isolation.SUPPORTED_PROVIDER_IDS
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/modelman
uv run pytest tests/benchmark/test_isolation.py::test_supported_provider_ids_matches_llm_isolate_providers_documented_list -v
```

Expected: FAIL — `assert frozenset({'ollama', 'omlx', 'omlx-6bit'}) == frozenset({'ollama', 'llamacpp', 'omlx', 'omlx-6bit'})`

- [ ] **Step 3: Update `SUPPORTED_PROVIDER_IDS`**

In `modelman/src/modelman/benchmark/isolation.py`, replace line 17:

```python
SUPPORTED_PROVIDER_IDS: frozenset[str] = frozenset({"ollama", "llamacpp", "omlx", "omlx-6bit"})
```

with:

```python
# llamacpp retired 2026-09-07 (issue #33): kept as a case branch in
# bin/llm-isolate-provider but not isolatable. Re-enable steps:
# docs/reference/provider-artifacts.md
SUPPORTED_PROVIDER_IDS: frozenset[str] = frozenset({"ollama", "omlx", "omlx-6bit"})
```

- [ ] **Step 4: Update `DEFAULT_PROVIDER_IDS`**

In `modelman/src/modelman/registry.py`, replace (around line 239):

```python
# Provider ids that have a canonical default entry (the reconcilable local
# providers). migrate.py uses this to decide whether to use the default or
# fall back to its generic title()-cased import.
DEFAULT_PROVIDER_IDS: tuple[str, ...] = ("ollama", "llamacpp", "omlx")
```

with:

```python
# Provider ids that have a canonical default entry (the reconcilable local
# providers). migrate.py uses this to decide whether to use the default or
# fall back to its generic title()-cased import.
# llamacpp retired 2026-09-07 (issue #33): provider code kept in
# providers/llamacpp.py; re-enable steps in docs/reference/provider-artifacts.md.
DEFAULT_PROVIDER_IDS: tuple[str, ...] = ("ollama", "omlx")
```

- [ ] **Step 5: Mark the provider module UNUSED**

In `modelman/src/modelman/providers/llamacpp.py`, replace line 1:

```python
"""llama.cpp provider — uses the HF cache; no separate model dir by default."""
```

with:

```python
"""llama.cpp provider — uses the HF cache; no separate model dir by default.

UNUSED since 2026-09-07 (issue #33): the llamacpp provider is retired — no
registry entry, no litellm rows, no benchmark wiring. This module and its
tests are kept so re-enabling is a config change, not a rewrite. Re-enable
steps: docs/reference/provider-artifacts.md (repo root)."""
```

- [ ] **Step 6: Run the touched test plus the neighbor suites**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/modelman
uv run pytest tests/benchmark/test_isolation.py tests/test_registry.py tests/test_sync.py -q
```

Expected: all PASS (the llamacpp ids in test_registry/test_sync are fixture data, not tuple assertions; sync's `("llamacpp", "omlx")` literals in `_modeldir_providers`/`list_modeldir` are kept code and don't consult the tuple).

- [ ] **Step 7: Run the full modelman test suite**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/modelman
uv run pytest -q
```

Expected: all PASS. If `test_forms.py`/`test_models.py` fail, they must be passing explicit provider lists — fix nothing there; investigate only if a test asserts `DEFAULT_PROVIDER_IDS` content directly.

- [ ] **Step 8: Commit**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
git add modelman/src/modelman/registry.py modelman/src/modelman/benchmark/isolation.py modelman/src/modelman/providers/llamacpp.py modelman/tests/benchmark/test_isolation.py
git commit -m "feat(modelman): retire llamacpp provider wiring, keep implementation (issue #33)"
```

---

### Task 4: Remove llamacpp from the benchmark scripts and agent suite

**Files:**
- Modify: `benchmarks/qwen3.8-benchmark` (lines 54, 57, 60, 82, 99, 105, 125–129, 263, 282)
- Modify: `benchmarks/ornith-1.5-benchmark` (lines 47, 51, 55, 61, 80, 87, 107–111, 123, 220, 239)
- Modify: `benchmarks/suites/q4-agent-sweep.toml` (lines 28–30)

- [ ] **Step 1: Edit `benchmarks/qwen3.8-benchmark`**

Delete these lines (each shown with its exact current content):

```bash
DIRECT_URLS[llama_cpp]="http://localhost:8080/v1/chat/completions"
DIRECT_MODELS[llama_cpp]="local-llama"
LITELLM_MODELS[llama_cpp]="llama.cpp/local-llama"
```

```bash
        llama_cpp) echo "llama.cpp" ;;
```

```bash
ISOLATE_ID[llama_cpp]="llamacpp"
```

```bash
ISOLATE_ENV[llama_cpp]="LLM_ISOLATE_LLAMACPP_MODEL"
```

Delete the llamacpp block inside `ensure_all_local_started()`:

```bash
    if ! curl -s -m 2 http://localhost:8080/v1/models > /dev/null 2>&1; then
        if [ -f ~/Library/LaunchAgents/local.llamacpp.server.plist ]; then
            launchctl load -w ~/Library/LaunchAgents/local.llamacpp.server.plist 2>/dev/null || true
        fi
    fi
```

Change both backend loops (lines 263 and 282):

```bash
for backend in ollama omlx llama_cpp; do
```

→

```bash
for backend in ollama omlx; do
```

- [ ] **Step 2: Edit `benchmarks/ornith-1.5-benchmark`**

Delete these lines (exact current content):

```bash
DIRECT_URLS[llama_cpp]="http://localhost:8080/v1/chat/completions"
DIRECT_MODELS[llama_cpp]="local-llama"
LITELLM_MODELS[llama_cpp]="llama.cpp/ornith-1.5-35b"
```

```bash
        llama_cpp) echo "llama.cpp" ;;
```

```bash
ISOLATE_ID[llama_cpp]="llamacpp"
```

```bash
ISOLATE_ENV[llama_cpp]="LLM_ISOLATE_LLAMACPP_MODEL"
```

Delete the llamacpp block inside `ensure_all_local_started()`:

```bash
    if ! curl -s -m 2 http://localhost:8080/v1/models > /dev/null 2>&1; then
        if [ -f ~/Library/LaunchAgents/local.llamacpp.server.plist ]; then
            launchctl load -w ~/Library/LaunchAgents/local.llamacpp.server.plist 2>/dev/null || true
        fi
    fi
```

Delete the llamacpp health-check line inside the wait loop:

```bash
        curl -s -m 1 http://localhost:8080/v1/models > /dev/null 2>&1 || ok=false
```

Change both backend loops (lines 220 and 239):

```bash
for backend in ollama omlx_4bit omlx_6bit llama_cpp; do
```

→

```bash
for backend in ollama omlx_4bit omlx_6bit; do
```

- [ ] **Step 3: Edit `benchmarks/suites/q4-agent-sweep.toml`**

Delete:

```toml
[routes.direct.llamacpp]
base_url = "http://localhost:8080/v1"
api = "openai-completions"
```

- [ ] **Step 4: Verify syntax and no leftover references**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
/opt/homebrew/bin/bash -n benchmarks/qwen3.8-benchmark && /opt/homebrew/bin/bash -n benchmarks/ornith-1.5-benchmark && echo "syntax OK"
grep -n "llama_cpp\|LLAMACPP\|llamacpp" benchmarks/qwen3.8-benchmark benchmarks/ornith-1.5-benchmark benchmarks/suites/q4-agent-sweep.toml || echo "no references remain"
```

Expected: `syntax OK` then `no references remain`.

- [ ] **Step 5: Commit**

```bash
git add benchmarks/qwen3.8-benchmark benchmarks/ornith-1.5-benchmark benchmarks/suites/q4-agent-sweep.toml
git commit -m "feat(benchmarks): drop llamacpp backend (retired, issue #33)"
```

---

### Task 5: Update the bin helpers

**Files:**
- Modify: `bin/llm-restore-providers:54` (and the `wait` block)
- Modify: `bin/llm-isolate-provider:4` (header comment only — the llamacpp case branch, `start_llamacpp`, `LLAMACPP_MODEL`, and the `stop_all_local` branch stay as kept-but-disabled code)

- [ ] **Step 1: Drop the llamacpp restart from `bin/llm-restore-providers`**

Replace (lines 51–59):

```bash
FAILED=0
restart_ollama & p1=$!
restart_omlx & p2=$!
restart_launchd ~/Library/LaunchAgents/local.llamacpp.server.plist http://localhost:8080/v1/models llamacpp & p3=$!
restart_launchd ~/Library/LaunchAgents/local.litellm.proxy.plist http://localhost:4000/v1/models litellm & p4=$!
wait $p1 || FAILED=1
wait $p2 || FAILED=1
wait $p3 || FAILED=1
wait $p4 || FAILED=1
```

with:

```bash
FAILED=0
restart_ollama & p1=$!
restart_omlx & p2=$!
# llamacpp retired 2026-09-07 (issue #33) — not restarted; re-enable steps
# in docs/reference/provider-artifacts.md.
restart_launchd ~/Library/LaunchAgents/local.litellm.proxy.plist http://localhost:4000/v1/models litellm & p3=$!
wait $p1 || FAILED=1
wait $p2 || FAILED=1
wait $p3 || FAILED=1
```

- [ ] **Step 2: Update `bin/llm-isolate-provider`'s header**

Replace (lines 1–5):

```bash
#!/bin/bash
# llm-isolate-provider — stop other local providers, start+warmup one.
# Usage: llm-isolate-provider <provider-id>
# Supported: ollama, llamacpp, omlx, omlx-6bit
# On success, print JSON with provider/model/direct_url.
```

with:

```bash
#!/bin/bash
# llm-isolate-provider — stop other local providers, start+warmup one.
# Usage: llm-isolate-provider <provider-id>
# Supported: ollama, omlx, omlx-6bit
# (The llamacpp case branch below is retained but disabled — retired
# 2026-09-07, issue #33. Re-enable steps: docs/reference/provider-artifacts.md.)
# On success, print JSON with provider/model/direct_url.
```

- [ ] **Step 3: Verify and commit**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
bash -n bin/llm-restore-providers && bash -n bin/llm-isolate-provider && echo "syntax OK"
git add bin/llm-restore-providers bin/llm-isolate-provider
git commit -m "fix(bin): restore helper no longer restarts llamacpp (issue #33)"
```

Expected: `syntax OK`.

---

### Task 6: Retire on the host

Run only after Tasks 1–5 are committed (artifacts must exist in the repo first). Modelman must not be running (no pending TUI queue).

**Files (outside repo, all deleted or moved):**
- `~/Library/LaunchAgents/local.llamacpp.server.plist` → moved into repo (Task 1 copied it; delete here)
- `~/Library/LaunchAgents/local.llamacpp.server.plist.qwen3.8.bak` → same
- `~/.llamacpp.log`, `~/.llamacpp.err.log` → deleted
- `~/.config/litellm/config.yaml` → 2 rows removed
- `~/.config/local-ai/registry.toml` → llamacpp `[[providers]]` block removed

- [ ] **Step 1: Confirm the artifacts are committed**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
git log --oneline -1 -- docs/reference/artifacts/llamacpp/local.llamacpp.server.plist
```

Expected: a commit hash (from Task 1). Do not proceed on empty output.

- [ ] **Step 2: Unload the crash-looping agent and remove both plists**

```bash
launchctl bootout gui/$(id -u)/local.llamacpp.server 2>/dev/null || true
rm ~/Library/LaunchAgents/local.llamacpp.server.plist
rm ~/Library/LaunchAgents/local.llamacpp.server.plist.qwen3.8.bak
launchctl list | grep llamacpp || echo "llamacpp no longer loaded"
```

Expected: `llamacpp no longer loaded`.

- [ ] **Step 3: Delete the logs and uninstall the formula**

```bash
rm ~/.llamacpp.log ~/.llamacpp.err.log
brew uninstall llama.cpp
brew list --versions llama.cpp || echo "formula gone"
```

Expected: `formula gone`.

- [ ] **Step 4: Remove the two llama.cpp rows from the litellm config**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/modelman
uv run python - <<'EOF'
import yaml
p = "/Users/keith/.config/litellm/config.yaml"
with open(p) as f:
    cfg = yaml.safe_load(f)
before = {m["model_name"] for m in cfg["model_list"]}
cfg["model_list"] = [m for m in cfg["model_list"] if not m["model_name"].startswith("llama.cpp/")]
with open(p, "w") as f:
    yaml.dump(cfg, f, sort_keys=False, default_flow_style=False)
print("removed:", sorted(before - {m["model_name"] for m in cfg["model_list"]}))
EOF
launchctl kickstart -k "gui/$(id -u)/local.litellm.proxy"
sleep 3
curl -s -m 5 http://localhost:4000/v1/models | grep -c "llama.cpp" || echo "0 llama.cpp rows served"
```

Expected: `removed: ['llama.cpp/local-llama', 'llama.cpp/ornith-1.5-35b']` then `0 llama.cpp rows served` (grep exits non-zero when the count is 0, which triggers the echo).

- [ ] **Step 5: Remove the llamacpp block from the registry**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/modelman
uv run python - <<'EOF'
p = "/Users/keith/.config/local-ai/registry.toml"
lines = open(p).read().splitlines(keepends=True)
start = next(i for i, l in enumerate(lines)
             if l.strip() == "[[providers]]"
             and i + 1 < len(lines) and lines[i + 1].strip() == 'id = "llamacpp"')
end = next(i for i in range(start + 1, len(lines)) if lines[i].strip() == "[[providers]]")
del lines[start:end]
open(p, "w").write("".join(lines))
print("removed llamacpp provider block")
EOF
grep -c "llamacpp" ~/.config/local-ai/registry.toml || echo "no llamacpp rows in registry"
```

Expected: `removed llamacpp provider block` then `no llamacpp rows in registry`.

- [ ] **Step 6: Verify the restore helper is green (the issue #33 acceptance test)**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
PATH=$PWD/bin:$PATH llm-restore-providers
```

Expected: `[llm-restore-providers] providers restored`, exit 0.

---

### Task 7: Update root and package-level docs

**Files:**
- Modify: `CLAUDE.md` (root)
- Modify: `README.md`
- Modify: `modelman/CLAUDE.md`
- Modify: `modelman/README.md`

- [ ] **Step 1: Root `CLAUDE.md`**

Make these exact replacements:

1. `- \`./benchmarks/qwen3.8-benchmark [max_tokens]\` — single-pass benchmark (4 qwen3.8 backends)` →
   `- \`./benchmarks/qwen3.8-benchmark [max_tokens]\` — single-pass benchmark (2 qwen3.8 local backends + OpenRouter)`
2. `- \`./benchmarks/ornith-1.5-benchmark [max_tokens]\` — single-pass (4 Ornith-1.5-35B variants)` →
   `- \`./benchmarks/ornith-1.5-benchmark [max_tokens]\` — single-pass (3 Ornith-1.5-35B local variants + OpenRouter)`
3. `- \`bin/llm-isolate-provider <ollama|llamacpp|omlx|omlx-6bit>\` — stop others, start+warmup one (for \`modelman benchmark\`)` →
   `- \`bin/llm-isolate-provider <ollama|omlx|omlx-6bit>\` — stop others, start+warmup one (for \`modelman benchmark\`; llamacpp branch retained but disabled — see \`docs/reference/provider-artifacts.md\`)`
4. In the Architecture section, `- LaunchAgent plists: \`~/Library/LaunchAgents/local.llamacpp.server.plist\` (llama.cpp), \`local.litellm.proxy.plist\` (LiteLLM) — referenced by the isolation helpers` →
   `- LaunchAgent plists: \`~/Library/LaunchAgents/local.litellm.proxy.plist\` (LiteLLM) — referenced by the isolation helpers. (The llama.cpp plist was retired 2026-09-07 — artifact + restore steps in \`docs/reference/provider-artifacts.md\`.)`
5. In Key Gotchas, `- **Stop mechanisms per backend**: Ollama \`ollama stop <model>\` (daemon stays up), oMLX \`omlx stop\` (halts service), llama.cpp \`launchctl unload\` (halts LaunchAgent).` →
   `- **Stop mechanisms per backend**: Ollama \`ollama stop <model>\` (daemon stays up), oMLX \`omlx stop\` (halts service). (llama.cpp — formerly \`launchctl unload\` — was retired 2026-09-07; see \`docs/reference/provider-artifacts.md\`.)`

- [ ] **Step 2: Root `README.md`**

1. `| Root (\`bin/\`, \`benchmarks/\`, \`docs/\`) | backends (LiteLLM proxy, Ollama, llama.cpp, oMLX) + LaunchAgents + benchmarks + user guides |` →
   `| Root (\`bin/\`, \`benchmarks/\`, \`docs/\`) | backends (LiteLLM proxy, Ollama, oMLX) + LaunchAgents + benchmarks + user guides |`
2. Delete the line `curl -s -m 2 http://localhost:8080/health -o /dev/null -w "8080(llama.cpp):%{http_code}\n"` from the health-check block.
3. `launchctl list | grep -E 'litellm|llamacpp|omlx|postgresql|redis|ollama'` →
   `launchctl list | grep -E 'litellm|omlx|postgresql|redis|ollama'`
4. `Expected: \`401\` on :4000 = proxy up (correctly demanding a key); \`200\` on the other three.` →
   `Expected: \`401\` on :4000 = proxy up (correctly demanding a key); \`200\` on the other two.`
5. Add to the docs/guides reference list (wherever the other guide links live): `- [provider-artifacts.md](docs/reference/provider-artifacts.md) — captured provider artifacts + llama.cpp re-enable procedure`.

- [ ] **Step 3: `modelman/CLAUDE.md`**

Line 7, replace `(Ollama, llama.cpp, oMLX)` with `(Ollama, oMLX — llama.cpp is retired but its provider code is kept; see `docs/reference/provider-artifacts.md`)`. Leave the provider-module descriptions (lines 76–78, 133, 161–162) untouched — they describe kept code.

- [ ] **Step 4: `modelman/README.md`**

1. Lines 4–5: `across providers (Ollama,\nllama.cpp, oMLX, OpenRouter, and native agent providers like \`claude\`,\n\`codex\`)` → `across providers (Ollama,\noMLX, OpenRouter, and native agent providers like \`claude\`, \`codex\`; the\nllama.cpp provider is retired — see `../docs/reference/provider-artifacts.md`)`
2. `(Ollama, llama.cpp, oMLX) it means "the model is present on this machine".` →
   `(Ollama, oMLX; llama.cpp was retired 2026-09-07) it means "the model is present on this machine".`
3. Leave lines 252 and 309 (provider-module descriptions) untouched — kept code.

- [ ] **Step 5: Check links and commit**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
bin/check-links
git add CLAUDE.md README.md modelman/CLAUDE.md modelman/README.md
git commit -m "docs: root/modelman docs reflect llama.cpp retirement (issue #33)"
```

Expected: check-links clean, commit lands.

---

### Task 8: Update the user guides

**Files:**
- Modify: `docs/guides/00-config-map.md`
- Modify: `docs/guides/01-initial-setup.md`
- Modify: `docs/guides/02-providers-and-models.md`
- Modify: `docs/guides/04-litellm-config.md`
- Modify: `docs/guides/05-benchmarks.md`
- Modify: `docs/guides/08-maintenance-and-troubleshooting.md`

- [ ] **Step 1: `docs/guides/00-config-map.md`**

1. Delete the TL;DR table row:
   `| \`~/Library/LaunchAgents/local.llamacpp.server.plist\` | you (setup = \`01-initial-setup.md\`) | launchd | \`llama-server\` on :8080 |`
2. In the `~/.config/litellm/config.yaml` section, replace `the remaining 11 — 3 omlx variants, 2 llama.cpp rows, the hand-managed \`openrouter/qwen/qwen3.8-*\` set, and \`ollama/q8\`/\`ollama/o35\` — are deliberately hand-managed.` with `the remaining 9 — 3 omlx variants, the hand-managed \`openrouter/qwen/qwen3.8-*\` set, and \`ollama/q8\`/\`ollama/o35\` — are deliberately hand-managed. (The 2 llama.cpp rows were retired 2026-09-07 — see [provider-artifacts.md](../reference/provider-artifacts.md).)`
3. In the plists Purpose line, remove `, llama-server (:8080, pinned GGUF)` from the service list.
4. In the Verification `launchctl list` output block, delete the line `94631	0	local.llamacpp.server`. (The parenthetical below the block mentions only the PIDs column — no other change needed.)
5. In "Going deeper", add: `- Provider artifacts + llama.cpp re-enable procedure: \`/Users/keith/github/ohanaverse/local-ai-setup/docs/reference/provider-artifacts.md\``

- [ ] **Step 2: `docs/guides/01-initial-setup.md`**

1. Title (line 1): `# Initial setup — LiteLLM proxy + Ollama, llama.cpp, oMLX, Postgres/Redis` →
   `# Initial setup — LiteLLM proxy + Ollama, oMLX, Postgres/Redis (llama.cpp retired)`
2. Prerequisites: change `brew list --versions 2>/dev/null | grep -E 'llama.cpp|redis|postgresql|omlx'` → `brew list --versions 2>/dev/null | grep -E 'redis|postgresql|omlx'`; change `which omlx llama-server litellm hf` → `which omlx litellm hf`; in the two sample-output blocks delete the `llama.cpp 0.3.0` line, the `llama.cpp 0.3.0`-adjacent llama-server lines, and the `/opt/homebrew/bin/llama-server` line.
3. Delete the bullet `- macOS Apple Silicon (Homebrew's llama.cpp build enables Metal automatically).` (or replace with `- macOS Apple Silicon.` if the list would otherwise be empty).
4. `brew install llama.cpp omlx postgresql@16 redis` → `brew install omlx postgresql@16 redis`
5. In the "proxy model list" verification block, delete the two lines `llama.cpp/local-llama` and `llama.cpp/ornith-1.5-35b`. The trailing comment (`# (your registry's ids differ — ≥1 model present = success)`) needs no change.
6. Replace the entire `### 3. llama.cpp` section body (from the `### 3. llama.cpp` heading through the `(PID in column 1, exit status 0 = running.)` paragraph, keeping the heading itself) with:

```markdown
### 3. llama.cpp — RETIRED

> **Retired 2026-09-07** (issue #33): the LaunchAgent's pinned GGUF no longer
> existed, so the agent crash-looped at every login and
> `bin/llm-restore-providers` could never succeed. Removed from this host:
> both plists, the logs, and the Homebrew formula. The verbatim plist, the
> litellm rows, the registry block, and the full re-enable procedure live in
> [provider-artifacts.md](../reference/provider-artifacts.md).
```

(Keep the section number so later sections keep their numbers.)

- [ ] **Step 3: `docs/guides/02-providers-and-models.md`**

1. `### 4. HF-backed model (llama.cpp / oMLX)` → `### 4. HF-backed model (oMLX; llama.cpp retired — see [provider-artifacts.md](../reference/provider-artifacts.md))`
2. Lines 188–190 are dated run observations — leave them untouched (including their `DEFAULT_PROVIDER_IDS` parenthetical).
4. `- Cloud providers (OpenRouter) are never reconciled. Documented reconcilable set is \`("ollama", "llamacpp", "omlx")\` (\`src/modelman/sync.py:31\`).` →
   `- Cloud providers (OpenRouter) are never reconciled. Documented reconcilable set is \`("ollama", "omlx")\` — llamacpp was retired 2026-09-07 (\`src/modelman/sync.py:31\`, \`registry.py\` \`DEFAULT_PROVIDER_IDS\`).`
5. In the `grep -n "model_name"` excerpt block, delete the two lines `45:  - model_name: llama.cpp/local-llama` and `74:  - model_name: llama.cpp/ornith-1.5-35b`, and change the parenthetical below it to `(9 entries after the 2026-09-07 llama.cpp retirement; was 11 live 2026-08-29. Never \`cat\` this file into chat/docs — its \`api_key:\` values include real \`sk-or-v1-…\` keys.)`
6. Line 265: `the omlx/llamacpp entries remain hand-managed by design` → `the omlx entries remain hand-managed by design (the llama.cpp rows were retired 2026-09-07)`
7. Line 286: `reconcile only (\`ollama\`/\`llamacpp\`/\`omlx\`)` → `reconcile only (\`ollama\`/\`omlx\`; llamacpp retired 2026-09-07)`

- [ ] **Step 4: `docs/guides/04-litellm-config.md`**

1. Line 88: remove the `openai/local-model\` (llamacpp, fixed string for every model)` cell → the row reads `\`litellm_params.model\` | provider prefix + model name — \`ollama_chat/\`, \`openai/\` (omlx), \`openrouter/\` |`
2. Line 89: remove the `\`:8080/v1\` llama.cpp` cell → `\`api_base\` | provider \`auth.base_url\` — \`:11434\` ollama, \`:8000/v1\` omlx, \`https://openrouter.ai/api/v1\` |`
3. Line 90: remove the `llamacpp: literal \`"dummy-key"\`` cell → `\`api_key\` | ollama: omitted · omlx: literal \`"not-needed"\` · openrouter: \`provider.auth.secret_ref\` **verbatim** |`
4. Line 283: `the omlx/llamacpp rows, the hand-managed \`openrouter/qwen/qwen3.8-*\` set` → `the omlx rows, the hand-managed \`openrouter/qwen/qwen3.8-*\` set (the 2 llama.cpp rows were retired 2026-09-07 — see [provider-artifacts.md](../reference/provider-artifacts.md))`
5. Line 284: `\`api_base\` targets are oMLX \`:8000\`, llama.cpp \`:8080\`, ollama \`:11434\`` → `\`api_base\` targets are oMLX \`:8000\`, ollama \`:11434\``

- [ ] **Step 5: `docs/guides/05-benchmarks.md`**

1. Line 11: `- Backends healthy: the four-port block in Verification answers (llama.cpp \`:8080\`, oMLX \`:8000\`, ollama \`:11434\`, LiteLLM \`:4000\`).` →
   `- Backends healthy: the three-port block in Verification answers (oMLX \`:8000\`, ollama \`:11434\`, LiteLLM \`:4000\`). llama.cpp was retired 2026-09-07 — see [provider-artifacts.md](../reference/provider-artifacts.md).`
2. Line 13: `bin/llm-isolate-provider <ollama|llamacpp|omlx|omlx-6bit>` → `bin/llm-isolate-provider <ollama|omlx|omlx-6bit>`
3. Isolation table: delete the whole `| \`llamacpp\` | … |` row; in the \`ollama\` row's stop column remove `, llama.cpp (\`launchctl unload\`)`; in the \`omlx\` and \`omlx-6bit\` rows' stop columns remove `, llama.cpp`.
4. Line 72: remove `\`LLM_ISOLATE_LLAMACPP_MODEL\`, ` from the env-var list.
5. Line 74: `\`bin/llm-restore-providers\` restarts all four services in parallel (ollama, oMLX, llama.cpp, LiteLLM)` → `\`bin/llm-restore-providers\` restarts all three services in parallel (ollama, oMLX, LiteLLM)`
6. Line 99: `Targets are local providers only (\`ollama\`, \`llamacpp\`, \`omlx\`)` → `Targets are local providers only (\`ollama\`, \`omlx\`; llamacpp retired 2026-09-07)`
7. Verification block: delete the `curl -s -m 2 http://localhost:8080/health ...` line and the `8080(llama.cpp):200` output line; adjust any "four ports" wording to "three ports".
8. Line 192: remove the `; llama.cpp: \`launchctl unload ~/Library/LaunchAgents/local.llamacpp.server.plist\`` clause.

- [ ] **Step 6: `docs/guides/08-maintenance-and-troubleshooting.md`**

1. Line 9: `the five LaunchAgents exist and load (\`~/Library/LaunchAgents/\`: \`local.litellm.proxy.plist\`, \`local.llamacpp.server.plist\`, \`homebrew.mxcl.omlx.plist\`, ...)` → `the four LaunchAgents exist and load (\`~/Library/LaunchAgents/\`: \`local.litellm.proxy.plist\`, \`homebrew.mxcl.omlx.plist\`, \`homebrew.mxcl.postgresql@16.plist\`, \`homebrew.mxcl.redis.plist\`) — llama.cpp's plist was retired 2026-09-07 (see [provider-artifacts.md](../reference/provider-artifacts.md))`
2. Verification: delete the `curl -s -m 2 http://localhost:8080/health ...` line; change the grep to `launchctl list | grep -E 'litellm|omlx|postgresql|redis|ollama'`; delete the `8080(llama.cpp):200` and `94631	0	local.llamacpp.server` output lines.
3. Line 47: remove the `\`local.llamacpp.server\` has \`RunAtLoad\` and \`KeepAlive\` both \`true\` in its plist (verified) — **:8080 is expected to be up at every login**, and launchd respawns it if it dies.` sentence; change `Same two keys are \`true\` in all five plists.` → `Same two keys are \`true\` in all four remaining plists.`
4. In the plist grep sample and its output block, remove both `local.llamacpp.server.plist` grep arguments and all four `local.llamacpp.server.plist:` output lines.
5. Line 79: `So after login: LiteLLM (:4000), llama.cpp (:8080), oMLX (:8000), Postgres, Redis all come up on their own — llama.cpp included, because of its \`RunAtLoad\`/\`KeepAlive\`.` → `So after login: LiteLLM (:4000), oMLX (:8000), Postgres, Redis all come up on their own.`
6. Line 83: `(this is the trio you manage via \`brew services\`; LiteLLM and llama.cpp are hand-rolled plists outside brew)` → `(this is the trio you manage via \`brew services\`; LiteLLM is a hand-rolled plist outside brew)`
7. Line 103: delete the `| llama.cpp :8080 | ... |` service-table row.

- [ ] **Step 7: Check exposure-snapshot drift, links, and commit**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
git grep -n "litellm_exposed = " docs/guides/ | wc -l   # note the count
bin/check-links
git add docs/guides/
git commit -m "docs(guides): reflect llama.cpp retirement across user guides (issue #33)"
```

Expected: check-links clean. (The exposure-snapshot count itself should not change — no `litellm_exposed = ` lines are touched — the grep is a drift tripwire per CLAUDE.md; investigate before committing if the count changes and you did not touch an exposure block.)

---

### Task 9: Update benchmark docs and the roadmap

**Files:**
- Modify: `benchmarks/README.md`
- Modify: `benchmarks/qwen3.8-benchmark.md`
- Modify: `benchmarks/ornith-1.5-benchmark.md`
- Modify: `docs/superpowers/plans/2026-09-06-issue-roadmap.md`

- [ ] **Step 1: `benchmarks/README.md`**

1. Line 16: `covering the four qwen3.8 variants (Ollama, oMLX, llama.cpp, OpenRouter)` → `covering the qwen3.8 variants (Ollama, oMLX, OpenRouter; llama.cpp retired 2026-09-07)`
2. Line 19: `(Ollama Q4_K_M, oMLX 4-bit, oMLX 6-bit, llama.cpp Q6_K)` → `(Ollama Q4_K_M, oMLX 4-bit, oMLX 6-bit; llama.cpp Q6_K retired 2026-09-07)`
3. Leave line 69 (archive-doc description) untouched — it describes a historical document.

- [ ] **Step 2: `benchmarks/qwen3.8-benchmark.md`**

1. Delete the backend-table row (line 15): `| **llama.cpp** | \`llama.cpp/local-llama\` (Qwen3.8-27B-UD-Q4_K_M.gguf) | GGUF (Q4_K_M, 16 GB) | \`~/.cache/huggingface/hub/models--unsloth--Qwen3.8-27B-GGUF/...\` |`
2. Directly under the backend table, add:

```markdown
> **llama.cpp retired 2026-09-07** (issue #33) — no longer a benchmark
> backend; artifacts + re-enable steps in
> [provider-artifacts.md](../reference/provider-artifacts.md). Dated result
> tables below keep their historical llama.cpp rows.
```

3. Line 32: `all three local models are unloaded (Ollama) or fully stopped (oMLX, llama.cpp)` → `all local models are unloaded (Ollama) or fully stopped (oMLX)`
4. Delete the stop-mechanics row (line 38): `| **llama.cpp** | \`launchctl unload ~/Library/LaunchAgents/local.llamacpp.server.plist\` | Nothing — LaunchAgent halts, freeing the 16 GB Metal allocation |`
5. Line 57: `For each local backend (ollama, omlx, llama.cpp):` → `For each local backend (ollama, omlx):`
6. Leave the dated result tables (lines 88–128) and bug-history §5 (lines 239+) untouched.

- [ ] **Step 3: `benchmarks/ornith-1.5-benchmark.md`**

1. Delete the backend-table row (line 16): `| **llama.cpp** | \`Ornith-1.5-35B-Q6_K.gguf\` | Q6_K GGUF | 29.2 GB |`
2. Under the table, add the same retirement callout as in Step 2 (adjusting nothing else).
3. Line 24: `all local models are unloaded (Ollama) or fully stopped (oMLX, llama.cpp)` → `all local models are unloaded (Ollama) or fully stopped (oMLX)`
4. Delete the stop-mechanics row (line 30): `| **llama.cpp** | \`launchctl unload ~/Library/LaunchAgents/local.llamacpp.server.plist\` | Nothing — LaunchAgent halts, frees the ~29 GB Metal allocation |`
5. Line 49: `For each of the four variants (ollama, omlx 4-bit, omlx 6-bit, llama.cpp):` → `For each of the three variants (ollama, omlx 4-bit, omlx 6-bit):`
6. Leave the dated result tables (lines 80–123) and the 2026-09-01 plist-repoint note (line 130) untouched.

- [ ] **Step 4: `docs/superpowers/plans/2026-09-06-issue-roadmap.md`**

Directly under the `## Milestone A — Repair \`local.llamacpp.server\` (#33)` heading, insert:

```markdown
> **RESOLVED 2026-09-07 — decision: Option 2 (retire).** llama.cpp was
> disabled with artifacts preserved instead of repaired. See
> `docs/superpowers/specs/2026-09-07-llamacpp-retirement-design.md` and
> `docs/reference/provider-artifacts.md`. The text below is kept as the
> decision record.
```

Leave the rest of the milestone text untouched.

- [ ] **Step 5: Check links and commit**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
bin/check-links
git add benchmarks/README.md benchmarks/qwen3.8-benchmark.md benchmarks/ornith-1.5-benchmark.md docs/superpowers/plans/2026-09-06-issue-roadmap.md
git commit -m "docs(benchmarks+roadmap): mark llama.cpp retired, Milestone A resolved (issue #33)"
```

---

### Task 10: Full verification

- [ ] **Step 1: Lint and links**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
make lint-shell
bin/check-links
```

Expected: both clean.

- [ ] **Step 2: Full test suite (mirrors CI)**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
make test-all
```

Expected: modelman `make check`/`make test` and wt `go build`/`vet`/`test` all green.

- [ ] **Step 3: Runtime verification of the host state**

```bash
launchctl list | grep llamacpp || echo "no llamacpp agent"
ls ~/Library/LaunchAgents | grep llamacpp || echo "no llamacpp plist"
brew list --versions llama.cpp || echo "no llama.cpp formula"
ls ~/.llamacpp.log 2>/dev/null || echo "no llama.cpp logs"
curl -s -m 5 http://localhost:4000/v1/models | grep -c llama.cpp || echo "0 llama.cpp rows via proxy"
cd /Users/keith/github/ohanaverse/local-ai-setup && PATH=$PWD/bin:$PATH llm-restore-providers
```

Expected: all five "gone" confirmations plus `[llm-restore-providers] providers restored` with exit 0 — this is the issue #33 acceptance criterion.

- [ ] **Step 4: Benchmark smoke (optional, ~2 min; needs ollama + omlx up)**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
./benchmarks/qwen3.8-benchmark 30
```

Expected: runs the ollama and omlx backends plus OpenRouter rows (OpenRouter rows may be N/A without a key); no llama.cpp rows; ends with a results file in `/tmp` and no restore error.

---

### Task 11: Push and open the PR

- [ ] **Step 1: Push the branch**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
git push -u origin docs/issue-33-llamacpp-retirement
```

- [ ] **Step 2: Open the PR**

```bash
gh pr create \
  --title "Retire llama.cpp provider, preserve artifacts (issue #33)" \
  --body "Resolves #33 via the retire path (roadmap Milestone A Option 2).

- Artifacts (plists, litellm rows, registry block) captured under docs/reference/artifacts/ + provider-artifacts.md guide with the full re-enable procedure
- Host: LaunchAgent booted out, plists/logs removed, brew formula uninstalled, litellm rows + registry block removed
- Code: llamacpp dropped from DEFAULT_PROVIDER_IDS / SUPPORTED_PROVIDER_IDS / benchmark arrays; providers/llamacpp.py kept and marked UNUSED; llm-restore-providers no longer restarts llamacpp
- Docs: CLAUDE.md files, guides 00/01/02/04/05/08, benchmark docs, roadmap Milestone A marked resolved
- Litellm + oMLX artifacts documented for future reference

Verification: llm-restore-providers exits 0; make lint-shell / check-links / test-all green; no llama.cpp rows via the proxy." \
  --base main
```

Expected: PR URL printed. The PR references (and auto-closes on merge, if the tracker is configured for it) issue #33 — note #33 is already closed, so adjust the body to `Refs #33` instead of `Resolves #33` if GitHub rejects the keyword on a closed issue.