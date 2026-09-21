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
  picker over every currently eligible model (cloud, local and running, or
  local and idle — an idle pick is started first; blocked rows are
  unselectable), using the same decorated, sorted list (family,
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

An agent is eligible for a model when selecting it would launch or could
launch after a start: the model's provider is in the agent's
`supported_providers` and the row is a launch row (cloud, or a local model
the live probe reports running) or a start row (an idle local model wt can
start). Blocked rows — not on disk, no lifecycle backend, not in LiteLLM —
are excluded; passing one by id names the row's reason (not on disk, no
lifecycle backend) as `wt start` does, while other ineligible ids (for
example an unexposed cloud model) get a generic "cannot be smoke-tested"
message
(`smoke.Candidates` walks the same `catalog` rows a real launch consults).
An idle pick is started first through the shared start driver, honouring
the root `--replace` flag when another model occupies a single-model
provider's slot.

## Exit flow

Once a model has been resolved, `wt smoke`, on a TTY, shows the stop picker
so models it started (or any running model nothing else uses) can be
stopped. (wt smoke never records a session refcount entry — its agents run
one-shot — so there is none to release first.) This runs after PASS or FAIL
once the start step (if any) succeeded, but not after a failed or cancelled
start, an invalid or aborted selection, and is skipped entirely for `--json`
or a non-TTY stdin. A FAIL still exits 1 regardless. A failed or Ctrl+C-ed
start returns its error directly without the stop picker, and Ctrl+C while
agents are running ends wt immediately (a started model stays up; use
`wt stop`).

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

`wt smoke` never isolates providers or flips LiteLLM routing — it tests
whatever is live right now (plus the one idle model you pick, which it
starts). Use `wt stop`/`modelman stop` for explicit shutdown. It does still run a driver's normal pre-launch
step where one exists — e.g. `pi`'s model-catalog sync to
`~/.pi/agent/models.json` — the same as a real launch would; it just never
touches modelman-owned `registry.toml`/`modelman.toml`. See
`docs/superpowers/specs/2026-09-16-wt-smoke-design.md` and
`docs/superpowers/specs/2026-09-21-wt-model-subcommands-design.md` for the
full design rationale. See also [`wt-start-stop.md`](wt-start-stop.md).
