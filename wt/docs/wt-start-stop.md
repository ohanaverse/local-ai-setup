# `wt start` and `wt stop`

Start or stop local models directly, without launching an agent.

```bash
wt start                         # pick from every local model (needs a TTY)
wt start ollama/qwen3.8:27b-mlx  # start one model, no TTY needed
wt start <model> --replace       # replace or unload a running model without asking
wt stop                          # pick from running models (needs a TTY)
wt stop ollama/qwen3.8:27b-mlx   # stop one model
wt stop ollama                   # stop every running model of a provider
wt stop omlx                     # halt the omlx service and every model in it
wt stop <target> --yes           # skip the in-use confirmation
wt stop --all                    # stop every running local model, then the omlx service (and an mtplx wt cannot read)
```

## `wt start [model]`

- `model` — `<provider>/<name>`, as with `-M`. Omitted: the screen-1 model
  picker over every local model the live inventory finds — on disk or
  running, with or without a registry entry (needs a TTY).
- Idle model: started through the same driver as `wt -M` (progress on
  stderr, Ctrl+C cancels). If mtplx (one model per process) is
  occupied wt asks before replacing the running model; `--replace` skips the
  question. omlx is different, see below.
  Once the model is up and the LiteLLM proxy has picked up its route, wt
  asks the provider's server once whether the model is still there, and only
  then prints `wt: <id> is running`. A model that something stopped in the
  meantime (a `wt stop` in another terminal while this one showed
  `updating LiteLLM routes`) is an error and exit 1:
  `wt: <id> is not running: it started, and was stopped while wt updated the LiteLLM routes (mtplx no longer answers at http://127.0.0.1:8003)`.
  The route this start wrote is removed again. What counts as stopped: an
  mtplx whose port refuses the connection or that serves another model; an
  omlx that refuses or no longer has the model loaded; an ollama daemon that
  refuses. An ollama model that was only unloaded (`ollama stop`) is still
  reported as running, since ollama loads a pulled model on the next
  request. `wt smoke`, a `-M` launch that starts its model and the picker's
  start make the same check before they use the model; the full-screen
  picker reports a model that is gone on its status instead of exiting (see
  the picker's entry below).
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
- No lifecycle backend (mlx_lm_server): exits 1 with
  ``local model "<id>" is not running, and wt cannot start an mlx_lm_server pairing — start it with `llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>` ``,
  the target and draft being the row's own. A stopped mlx_lm_server pairing
  is not listed in the picker, so that message is reached only by naming
  the model.
- A provider wt cannot probe (retired llamacpp): a registry model of it is
  still listed, as a row that cannot be selected, since wt can neither
  see whether it is on disk nor start it. `wt start <id>` exits 1 with
  `local model "<id>" is not running, and wt cannot start provider "<provider>" — start it with that provider's own tool`.
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
  an error. A model of a provider whose probe gave no usable answer is
  neither: wt says it cannot tell what is running there, as the bare
  provider does, rather than that the model is not running. mtplx serves one
  process, so stopping it stops the whole
  provider. On omlx it unloads that one model and leaves the service and the
  other loaded models up; use `wt stop omlx` to halt the service.
- A bare provider (`ollama`, `omlx`, `omlx-6bit`, `mtplx`) stops all its
  running models; nothing running exits 0 with a note
  (`wt: nothing running on <provider>`). For omlx it halts the
  service itself. Other names are an
  error.
  - `wt stop mtplx` stops mtplx unless its port refuses the connection, also
    when wt cannot tell what it has loaded: a server that answered with an
    error, or gave no answer within the probe's 2 seconds. It prints
    `Stopping mtplx... done`, or `failed` and exits 1 when the port is still
    open afterwards. After a stop that worked wt removes the pidfile and
    its own start record beside it, when the pidfile named the server that
    was just stopped: checked before the stop by the rules given for a
    loading mtplx below, and removed once that process is gone. A pidfile
    that names anything else (another live process, a pid wt could not
    verify, the pid of a server started since) is left as it is.
    wt stops mtplx with mtplx's own `mtplx stop`, which
    has to recognise the server on the port: when it does not (a server that
    accepts the connection and answers nothing), the stop is `failed` with
    mtplx's own message, and the process has to be ended by hand.
    A port that refuses the connection is "nothing running", unless mtplx
    is still loading (next item). A provider row wt does not probe (no
    `location` and no local model) is "nothing running" as well.
  - **An mtplx that is still loading** has not opened its port — it reads
    its weights first — so `mtplx stop` cannot reach it. `wt stop mtplx` and
    `--all` stop it by the pid in the pidfile wt wrote when it started the
    server, and print `Stopping mtplx (still loading, pid N)... done`. wt
    never acts on the pid alone, since a pid can be reused. It signals the
    process only when all of this holds: the pidfile is a regular file the
    current user owns (it is in `/tmp`; a symlink or another user's file
    reads as no pidfile); the process is alive; the current
    user owns it; its arguments are those of the mtplx server on this
    provider's port (`mtplx serve ... --port <port>`, or the
    `python -m mtplx.server.openai ... --port <port>` it turns into, compared
    argument by argument); and, when wt recorded the server's start time —
    `wt start` writes it beside the pidfile — the process started at that
    time. A pidfile from an older wt or from llmbench holds only the pid, and
    is judged by the others. wt sends SIGTERM, waits up to 10 seconds,
    checks again that the pid is still the same process, and only then sends
    SIGKILL. A process that is already exiting when the 10 seconds end — a
    server this size takes a moment to give its memory back — gets no
    SIGKILL, only up to 6 more seconds. The stop is `done` once the process
    is gone; if wt cannot confirm that, it prints `failed` and exits 1.
    Ctrl+C during the wait ends the command with `cancelled` and exit 1. The
    server already has the signal and normally exits a moment later, so the
    error says
    `cancelled — mtplx (pid N) was sent SIGTERM and wt did not wait for it to exit; run "wt stop mtplx" again to confirm`.
    wt did not see it go: the pidfile is left, and a second `wt stop mtplx`
    either finds nothing running or stops what is still there.
    When the pidfile names a live process that fails a check, wt signals
    nothing: it prints that nothing is running, then what it found
    (`wt: the mtplx pidfile names pid N, which is not an mtplx server on port 8003 — left alone`),
    and exits 0. When it cannot read the process table at all it exits 1
    with `cannot tell whether mtplx is still loading` — as the last line of
    `--all`, after everything else was stopped, ending
    `nothing was stopped there`. A pidfile that is
    missing, unreadable or names a dead pid is "nothing running"; wt does
    not delete it.
    `wt stop <provider>/<name>` for an mtplx model during a load stops
    nothing — wt does not know which model the loading process holds — and
    fails with
    `model "<id>" is not running — mtplx is still loading (pid N); "wt stop mtplx" stops it`
    (or with `cannot tell whether mtplx is still loading` when wt could not
    read the process table, or with the `left alone` line when the pidfile
    names a live process that is not an mtplx server on the provider's port).
    Limits:
    - Only a server wt or llmbench started has a pidfile. An mtplx started by
      hand is invisible while it loads: `wt stop mtplx` and `--all` print
      that nothing is running and exit 0. End that process by hand.
    - Starting mtplx again while one is still loading — a second `wt start`,
      `wt start --replace`, the picker — sees an empty port and starts a
      second server, whose pid replaces the first in the pidfile. wt then
      finds only the newer one, and the first has to be ended by hand. Run
      `wt stop mtplx` before starting another.
    - The check and the signal are two steps. A pid that exits and is
      reused between them — a few microseconds — would get the signal. With
      the start time wt records, the process checked is known to be the one
      wt started; a pidfile holding only a pid (an older wt's, llmbench's)
      has the command line alone to go on.
    - The command-line rule is the one mtplx 2.12.0 has. If a later mtplx
      renames its server module, wt prints the `left alone` note above and
      stops nothing: end the process by hand, and report it.
  - ollama's models are stopped one by one and its daemon is left up, so
    when ollama's probe gives no usable answer (and its port does not
    refuse) there is nothing wt can stop: `wt stop ollama` fails with
    `cannot tell what is running on ollama` and exits 1.
- `--all` stops every running local model wt can stop, on every provider,
  and then halts the omlx service as `wt stop omlx` does — also when omlx has
  nothing loaded, since that is what frees its memory. omlx's models are not
  unloaded one by one first. mtplx is stopped as `wt stop mtplx` stops it:
  unless its port refuses the connection, also when wt cannot tell what it
  has loaded (then as `Stopping mtplx...`, before omlx), and while it is
  still loading. It asks once when any of the
  models, or any provider it is about to stop as a whole, is in use by
  a live wt session (`--yes` skips the question). Every stop is attempted: if
  one fails the others still run, and the exit code is 1. When ollama's probe
  gives no usable answer, nothing is stopped on ollama, the rest is still
  stopped, and the command exits 1 with
  `cannot tell what is running on ollama`. `wt: no running local models` is
  printed only when every provider either answered with nothing running or
  refused the connection with no mtplx loading behind it. Ctrl+C is not a
  failure to step over: it cancels the stop in flight and ends the command
  there, so a `--all` interrupted while it stops the models leaves the omlx
  service up, and the error names it. It takes no argument. A running
  mlx_lm_server pairing is not stopped, because wt has no engine for one;
  `llmbench provider stop mlx_lm_server` stops it.
- No argument: the stop picker (needs a TTY). Unlike the exit-flow pickers
  it also lists models in use by other wt sessions, marked with their
  session count. Type the numbers to stop, separated by spaces (`1 3`), or
  `a` / `all` for every listed model, and press Enter; Enter on an empty
  line stops nothing. There is no separate confirming step. A line with
  anything else on it stops nothing and the prompt asks again. With nothing
  to list it prints `wt: no running local models` and exits 0 — unless a
  provider's probe gave no usable answer, when it fails with
  `cannot tell what is running on <provider>` (exit 1). For mtplx and omlx
  the message names the commands that stop a server wt cannot read
  (`"wt stop mtplx" or "wt stop --all" stops mtplx`); for ollama there is
  none, and it ends `nothing was stopped`. When the picker does have models
  to list, it prints one line after it for a provider it could not read
  (`wt: could not tell what is running on mtplx — ...`): the list is then
  not everything that may be running. The picker lists running models, so
  it has no row for an mtplx that is still loading and stops none; wt names
  it instead, in place of `no running local models` or after the picker:
  `wt: mtplx is still loading (pid N) — "wt stop mtplx" or "wt stop --all" stops it`.
  The other two things a pidfile can name are reported as `wt stop mtplx`
  reports them: the `left alone` note, and exit 1 with
  `cannot tell whether mtplx is still loading`.

### In-use confirmation

If live wt sessions use the target, wt asks
`... in use by N live wt session(s) (models); stop anyway? [y/N]` on the
controlling terminal, default No. N is the total across the models being
stopped (an mtplx provider counts once) — also for an mtplx stopped through a
model wt could list, where a session recorded on another mtplx model is not
counted. `--yes` skips the question; with no terminal and no
`--yes`, the command fails and says to rerun with `--yes`.

When a provider is stopped as a whole — `wt stop omlx`, `wt stop mtplx` with
nothing wt could list, and the same two inside `wt stop --all` — N is every
live wt session on any model of that provider, whether or not wt could see
the model running, a model with no registry entry included. A provider whose
port refuses the connection serves nobody, so it is stopped with no question
— except an mtplx that is still loading, whose sessions are waiting for that
load: `mtplx is still loading (pid N) and is in use by 1 live wt session(s) (mtplx/m); stop anyway? [y/N]`
(in `--all`'s question: `..., and mtplx is still loading (pid N); stop them all?`).
If wt could not tell what the provider has loaded, the question says so:
`omlx is in use by 1 live wt session(s) (omlx/c), and wt could not tell what it has loaded; stop anyway? [y/N]`.

## omlx: a pool of loaded models

omlx holds several models at once under a memory ceiling and unloads the least
recently used when a load does not fit.

- `wt start <omlx model>` loads it **beside** the ones already loaded. Both
  are routed. When the model fits, nothing is asked.
- When it does not fit, wt names the models omlx is expected to unload, each
  with its live wt session count, and asks (default No); `--replace` skips
  the question. Declining changes nothing. With no TTY wt cannot ask: the
  start exits 1 with the question followed by
  `— rerun with --replace to confirm`, and changes nothing (the same holds
  for a start that would replace the mtplx model). If wt cannot size the pool
  (omlx's status endpoint refused, or its memory limit is off) it names every
  other loaded model.
- The prediction is an estimate: omlx starts unloading below its ceiling
  (at 85% of it by default) and does not report where, so wt keeps 15% of
  the ceiling free when it predicts. If omlx unloads a model wt did not name, wt
  removes that model's route and prints
  `wt: omlx unloaded <id> to make room (not predicted)`. A load omlx refuses
  as too large fails with omlx's own explanation of what holds the memory.
- The full-screen `wt` picker cannot show stderr, so it takes every line a
  start prints and shows them itself, in the same words: what omlx unloaded
  (with `(not predicted)`), `LiteLLM route not updated: …` when `config.yaml`
  could not be written, and the warnings of the proxy restart. After a start
  that succeeds they are printed above the agent's output; if the agent then
  fails to launch, the picker's status shows them above `launch failed`.
  After a start that fails or is cancelled the status shows them above the
  failure, wrapped to the terminal's width and never cut, and the same lines
  are printed, with the failure under them, when wt exits or above the next
  agent launched from that picker. A status too tall to share the terminal
  with the table has the screen to itself until the next key. The agent is
  still launched after a start that could not write the route: the picker
  tries the route once more first (the launch-time check every launch gets),
  and when that fails too the lines above the agent's output are where to
  find why it answers `Invalid model name`. Once a start has reached
  `updating LiteLLM routes` the model is loaded and there is nothing left to
  cancel: esc does nothing there, and ctrl+c quits wt without launching the
  agent. On a terminal too narrow for the start screen's one line, the stage
  is on a line of its own under the model id. After a start that
  fails or is cancelled having already displaced a model, the start screen
  stays up until the proxy has restarted (`updating LiteLLM routes`), so
  those warnings are in the status. A model that was stopped while the
  screen showed `updating LiteLLM routes` (a `wt stop` in another terminal)
  is a failed start here too: wt asks the provider's server once after the
  proxy has restarted, and when the model is gone the picker comes back with
  `<id> is not running: it started, and was stopped while wt updated the
  LiteLLM routes (...)` on its status, under whatever the start printed, and
  launches no agent. The route the start wrote is removed again and not
  written back. The screen stays on `updating LiteLLM routes` and keeps
  answering ctrl+c while wt asks. Quitting wt during a start (ctrl+c
  twice) prints what the start had printed once the screen is restored.
  Nothing a start prints is written to stderr while the picker is up, and
  `wt 2>log` records each line once.
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
    `wt served` (which exits 1), or `wt stop`.
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

It is a diagnostic: it answers with the key the registry names, so it works
where a keyless probe of a partly loaded omlx pool is refused.

## `wt warm <provider> <model>`

Loads a model into an **omlx** server that is already running, with one
one-token request — the warmup step of `wt start` by itself. Nothing is
started, stopped or routed. `<model>` is the name omlx serves the model under
(its directory name), or a repo id ending in it.

When the server has an API key, wt sends the one the registry's omlx provider
row names (`auth.secret_ref`). A server that refuses the request (401/403)
fails the command at once, saying whether a key is missing or was refused.

llmbench's omlx backend asks this when omlx refuses its keyless warmup, since
only wt resolves a `secret_ref`. To start a model yourself, use `wt start`.

## Exit codes

`0` on success, when there was nothing to start or stop (already running —
`wt start` may still write that model's missing route — or nothing to stop
on a provider) or a cancelled `wt stop` picker; `1` on any error, including a declined
confirmation, a cancelled `wt start` picker (`model selection canceled`), a
provider wt stopped but that still answers, and a `wt stop` that could not
tell what a provider is running and had no way to stop it regardless.

An error of `wt start` is printed once, on stderr, as `wt: <message>`. A
failed mtplx start ends with the server's last log lines
(`...; log tail: <lines>`): at most 512 bytes of the log, from the start of
a line (a last line longer than that is shown cut).

See also [`wt-smoke.md`](wt-smoke.md).
