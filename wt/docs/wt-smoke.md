# `wt smoke`

Finds every agent currently eligible for one model and runs a one-shot
prompt through each, using wt's real launch machinery
(`agents.BuildLaunchCmd`) — the same construction a real `wt -A <agent> -M
<id>` launch uses. Reports PASS/FAIL/SKIP per agent with enough detail to
diagnose a failure without re-running anything by hand.

This is a live debugging tool over whichever model you point it at right
now, distinct from `make test-agents` (`agents-smoke.sh`'s hand-curated
regression matrix across a fixed model list and both routing modes).

```bash
wt smoke                        # list eligible models, prompt for a pick (needs a TTY)
wt smoke ollama/qwen3.8:27b-mlx # test one model directly, no TTY needed
wt smoke <model-id> --only claude,codex
wt smoke <model-id> --timeout 60s
wt smoke <model-id> --prompt "explain what 2+2 is"
wt smoke <model-id> --json
```

- `model-id` — a registry model id (`provider/name`). Omitted → interactive
  picker over every currently eligible model (cloud, or local and running
  per the live probe), using the same decorated, sorted list (family,
  usage, cost, tags, rotation marker) the `wt` agent flow's model picker
  renders — unlike that flow, the list here is never narrowed to one
  agent's supported providers, since `wt smoke` picks the model first and
  derives its eligible agents from that choice afterward.
- `--prompt` — override the default sentinel-echo prompt. A custom prompt
  cannot be verified for a sentinel it was never asked to produce, so
  verification degrades to "the agent exited 0 within the timeout" — this
  confirms wiring, not response correctness.
- `--timeout` — per-agent timeout (default `180s`).
- `--only` — comma-separated agents to restrict the run to; must be a
  subset of the model's currently eligible agents.
- `--json` — emit a machine-readable report instead of the human table.

## Eligibility

An agent is eligible for a model when selecting it would launch: the
model's provider is in the agent's `supported_providers` and the row is a
launch row — cloud models plus local models the live probe reports as
running (`smoke.Eligibility` walks the same `catalog` rows a real launch
consults). `wt smoke` never starts or stops anything — starting a model to
check it is the launch path's job, not the doctor's.

## Progress (stderr)

While a run is in progress, `wt smoke` writes a timestamped line to stderr
before and after each agent's row — always on, since it never touches
stdout (the human table or `--json` report are unaffected). This makes a
long or hung agent visible in real time instead of silence until the final
report, and a FAIL is fully debuggable the moment it happens: the result
line carries the same command/exit-code/error/output detail the final
report's FAIL block shows.

```
[15:04:05] wt smoke: claude x ollama/qwen3.8:27b-mlx - starting
[15:04:09] wt smoke: claude x ollama/qwen3.8:27b-mlx - PASS (4.2s)
[15:04:09] wt smoke: codex x ollama/qwen3.8:27b-mlx - starting
[15:07:09] wt smoke: codex x ollama/qwen3.8:27b-mlx - FAIL (180.0s): timed out after 3m0s
  command: codex exec ...
  exit code: 0
  error: timed out after 3m0s
[15:07:09] wt smoke: copilot x ollama/qwen3.8:27b-mlx - starting
[15:07:09] wt smoke: copilot x ollama/qwen3.8:27b-mlx - SKIP (0.0s): agent copilot not installed
```

## Output

Human (default): one line per agent, `[STATUS] agent model (duration)`.
Every FAIL row gets an indented detail block (command, exit code, captured
output) immediately after it; PASS/SKIP stay single-line. A trailing
`=== PASS: n FAIL: n SKIP: n (model=..., runid=...) ===` line summarizes the
run.

```
$ wt smoke ollama/qwen3.8:27b-mlx
[PASS ] claude     ollama/qwen3.8:27b-mlx (4.2s)
[FAIL ] codex      ollama/qwen3.8:27b-mlx (180.0s)
  command: codex exec ...
  exit code: 0
  error: timed out after 3m0s
[SKIP ] copilot    ollama/qwen3.8:27b-mlx (0.0s)
=== PASS: 1 FAIL: 1 SKIP: 1 (model=ollama/qwen3.8:27b-mlx, runid=run-a1b2c3d4) ===
```

`--json` emits `{"run_id", "model", "rows": [{"agent", "model", "status",
"exit_code", "duration_ms", "command", "output", "error"}]}`.

Exit code: `0` if every non-SKIP row PASSed, `1` if any row FAILed. SKIP
(agent binary not installed) never affects the exit code.

## Out of scope

`wt smoke` never starts, stops, or isolates local model providers (use
`modelman start`/`stop`) and never flips LiteLLM routing — it tests
whatever is live right now. It does still run a driver's normal pre-launch
step where one exists — e.g. `pi`'s model-catalog sync to
`~/.pi/agent/models.json` — the same as a real launch would; it just never
touches modelman-owned `registry.toml`/`modelman.toml` or starts/stops any
provider process. See
`docs/superpowers/specs/2026-09-16-wt-smoke-design.md` for the full design
rationale.
