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
  picker over every currently eligible model (cloud-exposed, or local and
  live-verified-running).
- `--prompt` — override the default sentinel-echo prompt. A custom prompt
  cannot be verified for a sentinel it was never asked to produce, so
  verification degrades to "the agent exited 0 within the timeout" — this
  confirms wiring, not response correctness.
- `--timeout` — per-agent timeout (default `180s`).
- `--only` — comma-separated agents to restrict the run to; must be a
  subset of the model's currently eligible agents.
- `--json` — emit a machine-readable report instead of the human table.

## Eligibility

An agent is eligible for a model when the model's provider is in the
agent's `supported_providers` **and** the model passes the same
exposure/local-running-gate checks a real launch would (`Config.EligibleModels`
+ `internal/localgate.Apply`) — the exact rules `wt`'s own launch path
applies, not a separate matrix.

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
whatever is live right now. See
`docs/superpowers/specs/2026-09-16-wt-smoke-design.md` for the full design
rationale.
