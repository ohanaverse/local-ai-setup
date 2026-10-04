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
wt smoke <model-id> --cwd        # run the agents in the current directory
```

- `model-id` — a model id (`provider/name`): a registry id, or a discovered
  model's id. Omitted → interactive
  picker over every currently eligible model (cloud, local and running, or
  local and idle — an idle pick is started first; blocked rows are
  unselectable), using the same decorated, sorted list (family,
  usage, cost, tags, rotation marker) the `wt` agent flow's model picker
  renders — unlike that flow, the list here is never narrowed to one
  agent's supported providers, since `wt smoke` picks the model first and
  derives its eligible agents from that choice afterward.
- `--prompt` — override the default sentinel prompt. A custom prompt
  cannot be verified for a sentinel it was never asked to produce, so
  verification degrades to "the agent exited 0 within the timeout" — this
  confirms wiring, not response correctness. **Runs with tool-use
  permission bypassed** (see "Tool-use permission" below) — an override
  prompt therefore runs whatever it asks for unsupervised, in a temporary
  directory by default and in your checkout with `--cwd`.
- `--cwd` — run each agent in the current directory. Without it every row
  runs in its own fresh temporary git repository, removed when the row ends
  (see "Tool-use permission").
- `--timeout` — per-agent timeout. Default: `180s` for cloud models, `900s` for local models (cold prefill of large agent prompts can take minutes). An explicit value applies to both.
- `--only` — comma-separated agents to restrict the run to; must be a
  subset of the model's currently eligible agents.
- `--json` — emit a machine-readable report instead of the human table.

## Eligibility

An agent is eligible for a model when selecting it would launch or could
launch after a start: the model's provider is in the agent's
`supported_providers` and the row is a launch row (cloud, or a local model
the live probe reports running) or a start row (an idle local model wt can
start). Rows that cannot run — no lifecycle backend, or an unmapped cloud
model whose route goes through LiteLLM — are excluded, and a
local model that is not on disk has no row; passing either by id names the
reason (not on disk, no lifecycle backend) as `wt start` does, while other ineligible ids (for
example a model no agent supports) get a generic "cannot be smoke-tested"
message
(`smoke.Candidates` walks the same `catalog` rows a real launch consults).
A discovered model (on disk, no registry entry) is eligible like any other:
wt routes it under its discovered id when it starts it, so agents that go
through LiteLLM can run it. A target that is already running — whoever
started it — has its route written first if it is missing
(`wt: LiteLLM route for <id> updated`).
An idle pick is started first through the shared start driver, honouring
the root `--replace` flag when another model occupies a single-model
provider's slot.

## Tool-use permission

Every one-shot launch except codex runs with the agent's
yolo/`--allow-all-tools`-equivalent flag set, regardless of the root
`--yolo` flag's own state. A one-shot prompt has no TTY to answer an
interactive tool-permission prompt; without this, a backing model that
attempts any tool call during the trivial smoke prompt gets a
permission-denied response it may not recover from within the timeout
(observed with copilot CLI, whose own docs call `--allow-all-tools`
"required for non-interactive mode"). codex is excluded: its `exec`
subcommand already never prompts for approval, so it never had this
failure mode, and its yolo flag
(`--dangerously-bypass-approvals-and-sandbox`) would additionally strip
its own sandbox for no benefit. opencode's flag is `--auto`, passed with
its value attached (`opencode --auto=true run <prompt>` — a bare `--auto` in
front of `run` would swallow the subcommand); it approves permissions that
are "not explicitly denied", so a permission the opencode config sets to
`deny` stays denied.

**Where it runs:** each row runs in its own fresh temporary directory, which
`wt smoke` creates before the row and removes after it, with whatever the
agent wrote there. The directory is a git repository (`git init`), because
codex refuses to run outside one. So an agent running with permission checks
bypassed cannot write into your checkout: the default prompt only asks for a
line of text, but a `--prompt` override, or a backing model that decides to
call a tool unprompted, runs genuinely unsupervised.

`--cwd` runs the rows in the current directory instead, for a test that needs
your project's files or agent configuration. Everything above then applies to
your checkout.

An agent can leave a background process behind that writes into its working
directory after it exits (a session-save hook, for example). `wt smoke`
removes each row's directory when the row ends and sweeps them all once more
at the end of the run (after the stop picker); anything written later than
that is left in the system temp directory, under a `wt-smoke-*` name. A
directory the sweep cannot remove is named on stderr (`wt: could not remove
the smoke directory <path>`). A row whose directory cannot be created is FAIL
with that reason; the other rows still run and the report is still printed.

The temporary directory limits where a *relative* write lands. It is not a
sandbox: an agent with permissions off can still write to an absolute path.

## The default prompt

The default prompt gives the model a short text in lower case and asks for it
back in upper case; the row passes when the upper-case text is in the output.
The text the row looks for is never in the prompt itself. That matters because
some agents print the prompt back (codex's output opens with it): a check for
text the prompt contains would pass on that echo whatever the model replied,
or if no model replied at all.

## Exit flow

Once a model has been resolved, `wt smoke`, on a TTY, shows the stop picker
so models it started (or any running model nothing else uses) can be
stopped. (wt smoke never records a session refcount entry — its agents run
one-shot — so there is none to release first.) This runs after PASS or FAIL
once the start step (if any) succeeded, but not after a failed or cancelled
start, an invalid or aborted selection, and is skipped entirely for `--json`
or a non-TTY stdin. A FAIL still exits 1 regardless. A failed or Ctrl+C-ed
start returns its error directly without the stop picker. Ctrl+C (or SIGTERM)
while an agent is running kills that agent and everything it spawned, fails
its row as `interrupted`, removes the row's directory, runs no further agent,
prints the report for the rows that ran and exits 1 without the stop picker (a
started model stays up; use `wt stop`). A second Ctrl+C ends wt at once.

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

A row whose agent could not select the model under test and would run on
its own default model instead is FAIL, with the error `<agent> fell back to
its default model instead of <id>: <the driver's reason>`; the agent is not
run (empty `command`). The default model would produce the sentinel just as
well, so exit 0 plus the sentinel proves nothing there. Today only pi can
fall back this way (see [pi-wt.md](wt-agents/pi-wt.md)); a real launch only
warns.

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
