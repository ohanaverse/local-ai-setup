# `wt start` and `wt stop`

Start or stop local models directly, without launching an agent.

```bash
wt start                         # pick from every local model (needs a TTY)
wt start ollama/qwen3.8:27b-mlx  # start one model, no TTY needed
wt start <model> --replace       # replace or unload a running model without asking
wt start <model> --plan --json   # what a start would unload; changes nothing
wt stop                          # pick from running models (needs a TTY)
wt stop ollama/qwen3.8:27b-mlx   # stop one model
wt stop ollama                   # stop every running model of a provider
wt stop omlx                     # halt the omlx service and every model in it
wt stop <target> --yes           # skip the in-use confirmation
```

## `wt start [model]`

- `model` — `<provider>/<name>`, as with `-M`. Omitted: the screen-1 model
  picker over every local model the live inventory finds — on disk or
  running, with or without a registry entry (needs a TTY).
- Idle model: started through the same driver as `wt -M` (progress on
  stderr, Ctrl+C cancels). If mtplx (one model per process) is
  occupied wt asks before replacing the running model; `--replace` skips the
  question. omlx is different, see below.
- omlx: "running" means loaded. The omlx service lists every model in its
  directory whether or not it is loaded, so an omlx model that is on disk but
  not loaded is idle and is started (loaded) like any other. If omlx has some
  models loaded and others not, and the server has an API key, wt needs that
  key to see which: set `auth.secret_ref` on the registry's omlx provider.
  See [omlx: a pool of loaded models](#omlx-a-pool-of-loaded-models).
- Already running: the model is left running. Its LiteLLM route is written
  if it is missing (`wt: LiteLLM route for <id> updated` on stderr), then wt
  prints `wt: <id> is already running` and exits 0.
- Still loading (omlx): the model cannot answer yet, so `wt start <id>` waits
  for the load that is already in progress, with the usual progress on
  stderr, then prints `wt: <id> is running`. It asks nothing, because it
  loads nothing new. In the pickers the row's RUNNING column reads `load`,
  and selecting it waits the same way. `wt` without `-M` never picks a model
  that is still loading. `wt stop <id>` on it fails, saying
  `omlx is still loading <id> and will not unload it mid-load` and that
  `wt stop omlx` stops the service and the load with it: omlx does not
  unload a model in the middle of a load.
  wt sees a load only through omlx's status endpoint: on a server with an
  API key the registry does not name, a loading model reads as not running.
- Not on disk (a registry entry whose artifact is missing): not listed;
  `wt start <id>` exits 1 with `<id> is not on disk — pull or download it
  first`.
- No lifecycle backend (mlx_lm_server): exits 1 with the `modelman start`
  hint. A stopped mlx_lm_server pairing is not listed in the picker, so
  that message is reached only by naming the model.
- A provider wt cannot probe (retired llamacpp): a registry model of it is
  still listed, as a row that cannot be selected, since wt can neither
  see whether it is on disk nor start it. `wt start <id>` exits 1 with
  `local model "<id>" is not running — start it with \`modelman start <id>\``.
- A started model is routed under its registry id, or its discovered id
  (`<family>/<artifact>`) when it has no registry entry; starting or stopping an mtplx model clears that provider family's routes —
  the family's registry-model rows and its wt-marked discovered rows —
  keeping only the started model's own route on start. An omlx start adds
  its own route and removes only the routes of models omlx unloaded; an omlx
  stop removes only that model's route. A hand-written row under
  another name is never removed. A model that is already running — one
  something else started — gets only its own missing route: that write
  removes no route and never replaces a hand-written row whose name is not
  a registry model id (one named like a registry id is adopted as wt's own
  row, as `wt litellm sync` does — guide 04,
  `docs/guides/04-litellm-config.md`, Gotchas). Stale routes are left to the
  next start, stop or `wt litellm sync`.
- Cloud or unknown id: exits 1.

## `wt stop [model|provider]`

- `<provider>/<name>` stops one running model; not running or unknown is
  an error. mtplx serves one process, so stopping it stops the whole
  provider. On omlx it unloads that one model and leaves the service and the
  other loaded models up; use `wt stop omlx` to halt the service.
- A bare provider (`ollama`, `omlx`, `omlx-6bit`, `mtplx`) stops all its
  running models; nothing running exits 0 with a note. For omlx it halts the
  service itself. Other names are an
  error.
- No argument: the stop picker (needs a TTY). Unlike the exit-flow pickers
  it also lists models in use by other wt sessions, marked with their
  session count. Type the numbers to stop, separated by spaces (`1 3`), or
  `a` / `all` for every listed model, and press Enter; Enter on an empty
  line stops nothing. There is no separate confirming step. A line with
  anything else on it stops nothing and the prompt asks again.

### In-use confirmation

If live wt sessions use the target, wt asks
`... in use by N live wt session(s) (models); stop anyway? [y/N]` on the
controlling terminal, default No. N is the total across the models being
stopped (an mtplx provider counts once). `--yes` skips the question; with no terminal and no
`--yes`, the command fails and says to rerun with `--yes`.

## omlx: a pool of loaded models

omlx holds several models at once under a memory ceiling and unloads the least
recently used when a load does not fit.

- `wt start <omlx model>` loads it **beside** the ones already loaded. Both
  are routed. When the model fits, nothing is asked.
- When it does not fit, wt names the models omlx is expected to unload, each
  with its live wt session count, and asks (default No); `--replace` skips
  the question. Declining changes nothing. If wt cannot size the pool (omlx's
  status endpoint refused, or its memory limit is off) it names every other
  loaded model.
- The prediction is an estimate: omlx starts unloading below its ceiling
  (at 85% of it by default) and does not report where, so wt keeps 15% of
  the ceiling free when it predicts. If omlx unloads a model wt did not name, wt
  removes that model's route and prints
  `wt: omlx unloaded <id> to make room (not predicted)`. A load omlx refuses
  as too large fails with omlx's own explanation of what holds the memory.
- In the `wt` picker the unloaded models are reported without the stderr
  line, which the full-screen picker hides, and without the
  `(not predicted)` marker: after a start that fails or is cancelled the
  status line begins `omlx unloaded <id> to make room`, and after a start
  that succeeds `wt: omlx unloaded <id> to make room` is printed above the
  agent's output. If the agent then fails to launch, the picker's status
  line begins with the same note, ahead of `launch failed`.
- `wt stop <omlx model>` unloads that one model. The service and the other
  models stay up, even when it was the last one.
- `wt stop omlx` (or `omlx-6bit`) halts the service and every model in it.
- An omlx with an API key that allows unauthenticated inference serves
  keyless chat requests but refuses keyless status, load and unload requests.
  With no `auth.secret_ref` on the registry's omlx provider, `wt start` still
  loads the model, through a keyless chat request, but wt is limited there:
  - It cannot see **which** models are loaded while the pool is partly loaded
    (some loaded, some not), which is the usual state once the pool holds
    more than one model. Those models do not read as running in the picker,
    `wt served` (which exits 1), `wt stop`, or `wt start --plan` (which
    answers `unknown`).
  - A second start therefore normally asks "cannot tell whether omlx is
    already serving a model"; `--replace` skips the question. The prompt
    names models only when every model in the pool is loaded. wt cannot
    predict or report what omlx unloads.
  - `wt litellm sync` leaves omlx routes as they are, with a warning, while
    the pool is partly loaded. The route of a model omlx unloaded stays
    until the pool is empty or fully loaded.
  - `wt stop <omlx model>` usually ends at `model "<id>" is not running`
    (`unknown model "<id>"` when the model has no registry entry), even for
    a model wt just started. Only when every pool model is loaded
    (a one-model pool included) does it reach omlx, which refuses the
    unload; wt then says how to proceed and never stops the service itself.
  - A model omlx cannot fit or does not know fails only when the warmup
    times out, not at once.

  Two ways out: set `auth.secret_ref`, which gives the full pool behavior
  above, or run `wt stop omlx` to stop the service and clear its routes.
- A load wt does not perform is not covered: an agent that dials omlx
  directly and names a model that is not loaded makes omlx load it, and
  possibly evict another, with no prompt. The routes are corrected by
  `wt litellm sync`; launching a model through wt also writes that model's
  own route if it is missing.

### `--plan` and `--json`

`wt start <id> --json` is the form scripts use. It never prompts.
`--plan` (needs `--json`) reports what a start would unload and changes
nothing. `status` is `running`, `fits`, `would_unload` or `unknown` (wt
could not tell what would be unloaded); `would_unload` is empty for every
status but `would_unload`.

A model omlx is still loading reports `fits` with an empty `would_unload`.
Starting it waits for the load and prints `started`.

```json
{"id": "omlx/B", "status": "would_unload",
 "would_unload": [{"id": "omlx/A", "sessions": 1}]}
```

Without `--plan`, a start that would unload a model, or whose plan is
`unknown`, and has no `--replace` exits 1 with that plan on stdout. With `--replace` it starts, then prints
`status` `started` or `already_running` and what omlx unloaded:

```json
{"id": "omlx/B", "status": "started", "unloaded": ["omlx/A"]}
```

A start that fails exits 1 with the message on stderr and nothing on stdout.
Both shapes are pinned by `docs/contracts/wt-start-cli.sample.json`.

## Selection screens

1. **Model screen** (`wt`, `wt start`, `wt smoke`): the shared model table.
   Rows that cannot start cannot be selected.
2. **Stop screen** (`wt stop`, and the exit flows after `wt` and
   `wt smoke`): running local models; the exit flows hide in-use models,
   `wt stop` shows them.

## `wt served <provider>`

Prints the model ids the provider's server is serving now, one per line, as
the server names them; `--json` prints `{"provider": …, "served": […]}`. It
answers for `omlx`, `mtplx` and `mlx_lm_server` — the same probe `wt start`,
`wt stop` and the pickers act on. Ollama is not answered for: a pulled model
loads on request, so it has no "serving now".

- omlx lists every model in its pool, loaded or not, so wt reports the ones
  **loaded or loading**. When the pool is partly loaded only omlx's status
  endpoint says which, and a server with an API key answers it only to that
  key: wt sends the one the registry's omlx provider row names
  (`auth.secret_ref`).
- Nothing printed with exit `0` means the server answered and serves nothing.
  Exit `1` means it gave no usable answer — nothing is listening, or it would
  not say which model is loaded — and is never to be read as "nothing".

`modelman` asks this when its own keyless probe of a partly loaded omlx pool
is refused, since only wt resolves a `secret_ref`.

## `wt warm <provider> <model>`

Loads a model into an **omlx** server that is already running, with one
one-token request — the warmup step of `wt start` by itself. Nothing is
started, stopped or routed. `<model>` is the name omlx serves the model under
(its directory name), or a repo id ending in it.

When the server has an API key, wt sends the one the registry's omlx provider
row names (`auth.secret_ref`). A server that refuses the request (401/403)
fails the command at once, saying whether a key is missing or was refused.

`modelman start` asks this when omlx refuses its own keyless warmup, since
only wt resolves a `secret_ref`. To start a model yourself, use `wt start`.

## Exit codes

`0` on success, when there was nothing to start or stop (already running —
`wt start` may still write that model's missing route — or nothing to stop
on a provider) or a cancelled `wt stop` picker; `1` on any error, including a declined
confirmation and a cancelled `wt start` picker (`model selection canceled`).

See also [`wt-smoke.md`](wt-smoke.md).
