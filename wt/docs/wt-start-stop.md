# `wt start`, `wt stop` and `wt-model stop`

Start or stop local models directly, without launching an agent.

```bash
wt start                         # pick from every local model (needs a TTY)
wt start ollama/qwen3.8:27b-mlx  # start one model, no TTY needed
wt start <model> --replace       # replace or unload a running model without asking
wt start <model> --others=stop   # stop other providers' loaded models first, without asking
wt start <model> --others=keep   # keep them, without asking or warning
wt stop                          # pick from running models (needs a TTY)
wt stop ollama/qwen3.8:27b-mlx   # stop one model
wt stop <model> --yes            # skip the in-use confirmation
```

On the machine that runs the model server:

```bash
wt-model stop ollama             # stop every running model of a provider
wt-model stop omlx               # halt the omlx service and every model in it
wt-model stop <provider> --yes   # skip the in-use confirmation
wt-model stop --all              # stop every running local model, then the omlx service (and an mtplx wt cannot read)
```

```bash
wt job <id>                      # show a model server job (a start or stop); --cancel cancels it
```

`wt start`, `wt stop <model>`, bare `wt stop` and `wt job` go through the
model server (see "Start and stop on the model server" below; with none
configured they refuse, exit 1). Provider and `--all` stops are
`wt-model stop <provider>` and `wt-model stop --all`, run on the machine that
runs the model server: they act on that machine's registry and read each
model's client sessions from the running server through its runtime record
(see [In-use confirmation](#in-use-confirmation)). `wt stop` given a provider
name or `--all` prints a pointer to them and exits 1. Where a section below
describes the registry-side behavior of `wt start` or of a model-id stop, that
is now what the model server's start and stop jobs do on its host.

## `wt start [model]`

- `model` — `<provider>/<name>`, as with `-M`. Omitted: the screen-1 model
  picker over every local model the live inventory finds — on disk or
  running, with or without a registry entry (needs a TTY).
- Idle model: started through the same driver as `wt -M` (progress on
  stderr, Ctrl+C cancels). If mtplx (one model per process) is
  occupied wt asks before replacing the running model; `--replace` skips the
  question. omlx is different, see below. Models of other providers that
  are loaded are listed and asked about separately, see
  [Other providers' models](#other-providers-models-412).
  mtplx is served on the port of the mtplx provider's `base_url` in the
  registry. An `http` url for `localhost`, `127.0.0.1` or the unspecified address (`0.0.0.0`, `[::]`) that
  names no port (`http://127.0.0.1/v1`) means 8003: wt then looks for the
  server on 8003 too, and routes to it there. Any other mtplx `base_url`
  with no port (an `https` url, another host name, `[::1]`, `127.0.0.2`) is
  refused before anything is spawned:
  `the registry's mtplx base_url (<origin>) names no port, and wt serves mtplx on 127.0.0.1 — name the port mtplx listens on in the base_url, or use http://127.0.0.1`.
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
  `wt-model stop omlx` stops the service and the load with it: omlx does not
  unload a model in the middle of a load.
  wt sees a load only through omlx's status endpoint: on a server with an
  API key the registry does not name, a loading model reads as not running.
- sushi: before the server is spawned, wt checks that the pack fits
  `iogpu.wired_limit_mb` (about 1.1 times the pack's `*.safetensors`; a
  setting of 0 counts as 75% of the machine's memory). A pack that does not
  fit fails the start with `sudo sysctl iogpu.wired_limit_mb=59000` (resets on
  reboot; wt never sets it). When wt cannot measure, it prints
  `wt: skipping the memory check (<why>)` and goes on. A start that finds the
  port busy with a sushi wt itself started (up, no pack loaded yet) reuses it
  and loads the pack there instead of failing with `port in use`.
- rec: one instance serves the whole machine, on any port. A rec already
  serving the model at the registry's address is adopted (nothing is spawned);
  one serving another model or address is asked about before it is replaced
  (`--replace` answers yes). `wt-model stop rec` runs `rec stop`, which reaches it
  wherever it listens.
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
  row, as `wt-model litellm sync` does — guide 04,
  `docs/guides/04-litellm-config.md`, Gotchas). Stale routes are left to the
  next start, stop or `wt-model litellm sync`.
- Cloud or unknown id: exits 1.

## `wt stop [model]`

- `<provider>/<name>` stops one running model; not running or unknown is
  an error. A model of a provider whose probe gave no usable answer is
  neither: wt says it cannot tell what is running there, as a
  provider stop does, rather than that the model is not running. mtplx serves one
  process, so stopping it stops the whole
  provider. On omlx it unloads that one model and leaves the service and the
  other loaded models up; use `wt-model stop omlx` to halt the service.
- No argument: a numbered picker over the model server's running local
  models (needs a TTY), each marked with the client sessions using it (see
  "Start and stop on the model server" below). Type the numbers to stop,
  separated by spaces (`1 3`), or `a` / `all` for every listed model, and
  press Enter; Enter on an empty line stops nothing.

## `wt-model stop <provider> | --all`

Run on the machine that runs the model server; it reads that machine's
registry. `wt stop` given a provider name or `--all` prints a pointer here and
exits 1 (`stop ollama moved to wt-model — run 'wt-model stop ollama'; nothing was changed`).
A mistake in the argument (a model id, an unknown provider, a provider
together with `--all`) prints one error line naming the valid form and exits 1.

- `<provider>` (`ollama`, `omlx`, `omlx-6bit`, `mtplx`, `rec`, `sushi`: any provider
  in the registry that wt can start and stop) stops all its
  running models; nothing running exits 0 with a note
  (`wt-model: nothing running on <provider>`). For omlx it halts the
  service itself. Other names are an
  error.
  - `wt-model stop mtplx` stops mtplx unless its port refuses the connection, also
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
    its weights first — so `mtplx stop` cannot reach it. `wt-model stop mtplx` and
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
    `cancelled — mtplx (pid N) was sent SIGTERM and wt did not wait for it to exit; run "wt-model stop mtplx" again to confirm`.
    wt did not see it go: the pidfile is left, and a second `wt-model stop mtplx`
    either finds nothing running or stops what is still there.
    When the pidfile names a live process that fails a check, wt signals
    nothing: it prints that nothing is running, then what it found
    (`wt-model: the mtplx pidfile names pid N, which is not an mtplx server on port 8003 — left alone`),
    and exits 0. When it cannot read the process table at all it exits 1
    with `cannot tell whether mtplx is still loading` — as the last line of
    `--all`, after everything else was stopped, ending
    `nothing was stopped there`. A pidfile that is
    missing, unreadable or names a dead pid is "nothing running"; wt does
    not delete it.
    `wt stop <model>` for an mtplx model during a load stops
    nothing — wt does not know which model the loading process holds — and
    fails with
    `model "<id>" is not running — mtplx is still loading (pid N); "wt-model stop mtplx" stops it`
    (or with `cannot tell whether mtplx is still loading` when wt could not
    read the process table, or with the `left alone` line when the pidfile
    names a live process that is not an mtplx server on the provider's port).
    Limits:
    - Only a server wt or llmbench started has a pidfile. An mtplx started by
      hand is invisible while it loads: `wt-model stop mtplx` and `--all` print
      that nothing is running and exit 0. End that process by hand.
    - Starting mtplx again while one is still loading — a second `wt start`,
      `wt start --replace`, the picker — sees an empty port and starts a
      second server, whose pid replaces the first in the pidfile. wt then
      finds only the newer one, and the first has to be ended by hand. Run
      `wt-model stop mtplx` before starting another.
    - The check and the signal are two steps. A pid that exits and is
      reused between them — a few microseconds — would get the signal. With
      the start time wt records, the process checked is known to be the one
      wt started; a pidfile holding only a pid (an older wt's, llmbench's)
      has the command line alone to go on.
    - The command-line rule is the one mtplx 2.12.0 has. If a later mtplx
      renames its server module, wt prints the `left alone` note above and
      stops nothing: end the process by hand, and report it.
  - **`wt-model stop sushi`** has no stop command of Sushi's to call (running the
    `sushi` binary with no arguments is not a stop), so wt signals the
    process the pidfile `/tmp/local-ai-setup-sushi.pid` names, once it has
    checked that process: the current user owns it, its arguments are
    `sushi serve ... --port 12345` (the port is the provider's), and, when wt
    recorded the server's start time (the `.wt` record beside the pidfile),
    it started at that time. A pid of 1 or wt's own is never signalled. wt
    sends SIGTERM to the process group (to the pid alone when it has no start
    record), waits up to 10 seconds, sends SIGKILL only if it is still the
    same process, removes the pidfile and the record once the process is
    gone, then waits up to 6 seconds for the port to close. Stopping Sushi unloads its pack: it
    serves one model per process (`--max-resident-models 1`).
    - A Sushi started by hand has no pidfile. If it answers on the port, wt
      says `sushi at port 12345 was not started by wt; stop it yourself`
      and signals nothing (exit 1); if the port is closed, nothing is
      running (`wt-model: nothing running on sushi`, exit 0).
    - A pidfile that llmbench wrote holds only the pid. It is accepted when
      the process passes the same user and arguments checks, but with no start
      time to compare and no process group to signal, so a pid reused by
      another `sushi serve --port 12345` would be taken for it.
    - Before Sushi has opened its port (the moments after a start) a pidfile
      that verifies is stopped through the loading-server path described for
      mtplx above, which signals the same way.
  - ollama's models are stopped one by one and its daemon is left up, so
    when ollama's probe gives no usable answer (and its port does not
    refuse) there is nothing wt can stop: `wt-model stop ollama` fails with
    `cannot tell what is running on ollama` and exits 1.
  - Stopping an ollama model is `ollama stop <name>` against the registry's
    ollama address, followed by a check that ollama no longer lists the
    model as loaded. A model that is still answering a request is not
    unloaded at once: ollama accepts the stop, shows the model as
    `Stopping...` in `ollama ps`, and unloads it when the request ends. wt
    waits for that, checking every second for up to 3 minutes. After 2
    seconds of waiting the open line says so —
    `Stopping <id>... waiting for ollama to finish a request in flight (up to 3m0s)... `
    — and then ends in `done` once the model is gone.
  - A model ollama still lists after the 3 minutes is not a failed stop:
    ollama accepted it and unloads the model when its request ends. The line
    ends in `still finishing a request; ollama unloads it when that ends`,
    the other stops go on, and that model alone does not make `wt-model stop`
    exit 1. `ollama ps` shows when it has unloaded.
  - When several ollama models are stopped together (`wt-model stop ollama`,
    `--all`, the picker's `a`), wt sends every `ollama stop` first and then
    waits for all of them under one 3-minute bound, not 3 minutes each. The
    lines still come one model at a time, in order, and the omlx halt of
    `--all` still comes after them.
  - wt does not wait when ollama refuses the stop (a non-zero exit with the
    model still loaded): ollama's own message is the failure, at once, and
    the command exits 1. A model that is already unloaded is a successful
    stop whatever `ollama stop` said. Ctrl+C during the wait ends wt's wait
    (the line ends in `cancelled`) and skips the models after it, and wt
    names the ones whose stop ollama had already accepted — ollama still
    unloads those when their requests end:
    ``wt: ollama had already accepted the stop of <ids>; it unloads each one once any request in flight ends (`ollama ps` shows when)``.
    If ollama stops answering during the wait, wt goes by `ollama stop`'s
    own exit, so the line can read `done` a moment before the model has
    left `ollama ps`.
- `--all` stops every running local model wt can stop, on every provider,
  and then halts the omlx service as `wt-model stop omlx` does — also when omlx has
  nothing loaded, since that is what frees its memory. omlx's models are not
  unloaded one by one first. mtplx is stopped as `wt-model stop mtplx` stops it:
  unless its port refuses the connection, also when wt cannot tell what it
  has loaded (then as `Stopping mtplx...`, before omlx), and while it is
  still loading. It asks once about the models client sessions use (see
  "In-use confirmation" below). Every stop is attempted: if
  one fails the others still run, and the exit code is 1. When ollama's probe
  gives no usable answer, nothing is stopped on ollama, the rest is still
  stopped, and the command exits 1 with
  `cannot tell what is running on ollama`. `wt-model: no running local models` is
  printed only when every provider either answered with nothing running or
  refused the connection with no mtplx loading behind it. Ctrl+C is not a
  failure to step over: it cancels the stop in flight and ends the command
  there, so a `--all` interrupted while it stops the models leaves the omlx
  service up, and the error names it. It takes no argument. A running
  mlx_lm_server pairing is not stopped, because wt has no engine for one;
  `llmbench provider stop mlx_lm_server` stops it.
### In-use confirmation

`wt-model stop <provider>` and `wt-model stop --all` ask (default No) before
stopping a model that client sessions of the running model server use. They
find the server through `wt-model serve`'s runtime record (`serve.json` in the
`~/.config/wt-model/` home; see `wt-model-server.md`, "Startup and shutdown")
and ask it for its client sessions, once per command. `--yes` answers the
question; with no terminal and no `--yes` the command exits 3 and stops
nothing. The question for one provider reads
`<what> in use by N live wt session(s) (<sessions>); stop anyway?` and for
`--all`
`running local models are in use by N live wt session(s) (<sessions>); stop them all?`.

- No record (no model server running on this machine): there is nothing to
  ask, and the stop goes on.
- A record it cannot read, or a server that does not answer or refuses: one
  line
  `wt-model: note: could not read client sessions from the model server (<why>) — models they use are stopped without asking`,
  and the stop goes on without asking.
- Ctrl+C while the sessions or what is running are being read, or before
  the question is answered, stops nothing: no note, the command ends with
  `cancelled — nothing was stopped` and exit 1.

A model-id stop (`wt stop <model>`) and the bare picker go to the
model server, which asks before it stops a model other client sessions use:
the question is asked on this terminal (`--yes` answers it; with no terminal
and no `--yes` the command exits 3 and stops nothing). See "Start and stop on
the model server" below.

## Other providers' models (#412)

Models of different providers share the machine's memory: an mtplx server, an
ollama model ollama keeps in memory (what `ollama ps` lists) and the models
in omlx's pool all hold RAM at once. A start of a model of one provider lists
the loaded models of every **other** local provider (omlx and omlx-6bit are one
provider here) and asks, every time, before it loads:

```
starting omlx/qwen3.6-27b-4bit loads it beside models of other providers:
  mtplx/qwen3.8-35b — memory use unknown
  ollama/gemma4:12b — 9.6 GB loaded
  cannot tell whether mlx_lm_server is serving a model
stop them first, keep them and start, or cancel? [s/k/N]
```

(An illustrative example: the lines list whatever is loaded when you start.)

- Each line is the model's id and the memory its server says it holds:
  `9.6 GB loaded` (ollama), `18.2 GB resident (omlx estimate)`, or
  `memory use unknown` (mtplx, and any server that does not say; never 0 and
  never a guess). An
  mtplx still loading reads `mtplx/<id> (loading)`. An mlx_lm_server pairing's
  figure ends `; wt cannot stop it`: wt has no way to stop one. So does an
  omlx model still loading (`omlx/<id> (loading)`): omlx refuses to unload a
  model mid-load, so it is listed and never stopped. A provider whose
  server answered but whose answer wt cannot read is a `cannot tell whether …`
  line; one that refuses the connection holds nothing and is not listed.
- `s` (or `stop`) stops every listed model wt can stop, then starts; `k` (or
  `keep`) starts beside them; anything else, Enter and EOF included, cancels
  with `approval missing: declined; <ids> still loaded` (exit 3) and changes nothing. When nothing
  listed can be stopped the question is `keep them and start, or cancel? [k/N]`.
- A start that would also replace or unload a model of its own provider asks
  that y/N first, then this question, then starts once with both answers.
  When wt cannot tell what its own provider is serving, it asks
  `cannot tell whether … is already serving a model; replace it with <id>?`
  first and only then learns about the other providers, so that start asks
  its two questions one after the other and checks up to three times.
  `--replace` answers only the y/N: it never stops another provider's model.
- `--others=stop` stops every listed model wt can stop and `--others=keep`
  keeps them, both without asking. Any other value is a usage error.
- The question and `--others` apply to `wt start`, `wt smoke`, and a
  `wt -M` launch that skips wt's picker screens: with `-W <name>`, with
  `--cwd`, or outside a git repository (a slot profile's local model
  included). A start from the picker screens asks in a dialog instead (see
  below).
- **When stdin is not a terminal (scripts, CI) and `--others` is not given**
  wt never asks and never stops:
  it keeps the models and prints one line to stderr,
  `wt: warning: starting <id> beside <id> (<memory>), …; pass --others=stop to stop them, --others=keep to silence this`.
  When nothing it names can be stopped (an mlx_lm_server pairing, an omlx
  model still loading, a provider wt cannot read), the advice is
  `pass --others=keep to silence this` alone: `--others=stop` would stop
  nothing.
- wt decides whether it may ask from stdin alone, though it reads the answer
  from the terminal: a redirected stdin is a script even with a terminal
  attached, so `wt start <id> < answers.txt` asks neither this question nor
  the replace question (it keeps the other models with the warning above, and
  a start that would replace a model exits 3 naming `--replace`). A piped
  answer can never stop a model and a script never waits on a question
  nobody answers; a script proceeds with `--replace` and `--others=keep` or
  `--others=stop` (#424).
- A stop goes through the same stop as `wt stop` (omlx unloads the model,
  ollama runs `ollama stop` and waits, mtplx stops the server) and removes
  the model's route (an ollama model keeps its route). A stop that fails ends
  the start: `stopping <id> before starting <model>: …`, and nothing is
  started. A model that was loaded after the question is kept, with
  `wt: <id> (<memory>) is also loaded; not stopped`, and so is a model you
  chose to stop that wt can no longer stop by then (an omlx model that is
  loading again), with the same line.
- Nothing is asked when the model is already running or omlx is already
  loading it, and a provider whose server was down when the start looked is
  not listed even if it comes up during the start.

### In the picker

A start from wt's picker screens (Enter on a start row, a slot profile whose
local model has to start, or `wt -M <id>` inside a repository without `-W` or
`--cwd`) asks the same question in a dialog:

```
Starting omlx/qwen3.6-27b-4bit will stop:
  omlx/other-4bit (in use by 1 wt session(s))
Also loaded, by other providers:
  mtplx/qwen3.8-35b — memory use unknown (in use by 1 wt session(s))
  ollama/gemma4:12b — 9.6 GB loaded
  cannot tell whether mlx_lm_server at http://127.0.0.1:8001 is serving a model
```

(An illustrative example. The `will stop` lines appear only when the start
would also replace or unload a model of its own provider.)

- The choices are **Cancel** (the default: back to the model screen,
  nothing stopped), **Start and keep them** (load the model beside the ones
  listed) and **Stop them, then start** (`Stop <n> model(s) of other
  providers first`: every listed model wt can stop). The stop choice is not
  offered when nothing listed can be stopped. With `will stop` lines as well
  the two actions read **Replace, keep the others** and **Replace and stop
  the others**: either one also confirms the replace.
- The stop choice is offered only while the dialog shows every model it
  would stop. On a terminal too short for the whole list the dialog keeps
  its first lines and ends them with
  `… enlarge the terminal to stop them`, and offers only Cancel and keep;
  enlarge the terminal and the stop choice comes back.
- esc backs out like Cancel and ctrl+c quits wt; neither stops anything. A
  slot profile's cloud routes written before the dialog are published on
  every way out, as for the replace dialog.
- `--others=keep|stop` answers the question for the `-M` pinned model only,
  as `--replace` does: never for a row you choose in the picker, which still
  gets the dialog. `--others=stop` does not answer the replace question: a
  pin that would also replace a model of its own provider without
  `--replace` still asks that, then stops the others it was told to.

## omlx: a pool of loaded models

omlx holds several models at once under a memory ceiling and unloads the least
recently used when a load does not fit.

- `wt start <omlx model>` loads it **beside** the ones already loaded. Both
  are routed. When the model fits, nothing is asked.
- When it does not fit, wt names the models omlx is expected to unload, each
  with the client sessions the model server counts on it, and asks (default No); `--replace` skips
  the question. Declining changes nothing. With no TTY wt cannot ask: the
  start exits 3 with
  `approval missing: pass --replace (there is no terminal to ask); nothing was started`,
  and changes nothing (the same holds for a start that would replace the mtplx
  model). A "no" exits 3 as well (`approval missing: declined; <id> still running`). If wt cannot size the pool
  (omlx's status endpoint refused, or its memory limit is off) it names every
  other loaded model.
- The full-screen `wt` picker's replace dialog, shown when Enter on a model
  that is not running would stop or unload others (a slot profile's local
  model included), names them the same way, one per line under
  `Starting <id> will stop:`: each one some wt session uses as
  `<id> (in use by N wt session(s))`, one nobody uses by its id alone.
  A line wider than the terminal wraps rather than being cut. The count is
  taken when the dialog opens and does not include the wt that is asking.
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
  agent, once the route update is finished (`wt: waiting for the LiteLLM
  proxy restart…` when that takes a moment). On a terminal too narrow for the start screen's one line, the stage
  is on a line of its own under the model id. After a start that
  fails or is cancelled having already displaced a model, the start screen
  stays up until the proxy has restarted (`updating LiteLLM routes`), so
  those warnings are in the status. A model that was stopped while the
  screen showed `updating LiteLLM routes` (a `wt stop` in another terminal)
  is a failed start here too: wt asks the provider's server once after the
  proxy has restarted, and when the model is gone the picker comes back with
  `<id> is not running: it started, and was stopped while wt updated the
  LiteLLM routes (...)` on its status, under whatever the start printed, and
  launches no agent. When the start printed nothing, the status is the only
  place that line appears. The route the start wrote is removed again and not
  written back. The screen stays on `updating LiteLLM routes` and keeps
  answering ctrl+c while wt asks. Quitting wt during a start (ctrl+c
  twice) prints what the start had printed once the screen is restored.
  Nothing a start prints is written to stderr while the picker is up, and
  `wt 2>log` records each line once.
- `wt stop <omlx model>` unloads that one model. The service and the other
  models stay up, even when it was the last one.
- `wt-model stop omlx` (or `omlx-6bit`) halts the service and every model in it.
- An omlx with an API key that allows unauthenticated inference serves
  keyless chat requests but refuses keyless status, load and unload requests.
  With no `auth.secret_ref` on the registry's omlx provider, `wt start` still
  loads the model, through a keyless chat request, but wt is limited there:
  - It cannot see **which** models are loaded while the pool is partly loaded
    (some loaded, some not), which is the usual state once the pool holds
    more than one model. Those models do not read as running in the picker,
    `wt-model served` (which exits 1), or `wt stop`.
  - A second start therefore normally asks "cannot tell whether omlx is
    already serving a model"; `--replace` skips the question. The prompt
    names models only when every model in the pool is loaded. wt cannot
    predict or report what omlx unloads.
  - `wt-model litellm sync` leaves omlx routes as they are, with a warning, while
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
  above, or run `wt-model stop omlx` to stop the service and clear its routes.
- A load wt does not perform is not covered: an agent that dials omlx
  directly and names a model that is not loaded makes omlx load it, and
  possibly evict another, with no prompt. The routes are corrected by
  `wt-model litellm sync`; a launch writes no route itself (a running model the
  model server knows to be unrouted is started there first, which writes it).

## A launch on the model server (`[model-server]`)

A model-driven launch that asks for a model needs a model server
(`[model-server]` in `config.toml`, or
`WT_MODEL_URL`/`WT_MODEL_TOKEN_FILE`/`WT_MODEL_TOKEN`; with none configured it
refuses before anything starts, exit 1) and reads no registry. The one
exception is a plain launch (no `-M`, `-T` or `-F`) in native mode (`mode =
"native"` under `[model-server]`, or `WT_MODEL_MODE=native`): it runs the agent
natively with its own configuration ([wt-session.md](wt-session.md), "Native
launch"); with no mode chosen, or a half-configured server, it refuses. A launch on a server reads no registry ([wt-model-server.md](wt-model-server.md)). The picker lists the
server's catalog. A launch that names its directory and its agent (`-W`,
`--cwd`, `--root` with `-A`) skips the picker: a `-M` pin is resolved by the
server; without one the server's one launchable model launches, and when
several match the next one after the last launch rotates in. A model is started only for a pin of an idle model, or for a running
local model the server says the gateway does not list; a running model whose
route the server could not read launches as it is. Before the start, the launch
registers a client session with the model so the server's idle grace counts it,
and ends it when the agent exits (a register the server refuses is one
`wt: note: the model server did not register this client session (…) — the launch goes on, …`
line). There is no stop picker after the agent exits: the server stops idle
models itself.

A start reports on stderr, once per step and every 10 seconds:

```text
wt: starting <id> — <step> (12s)
wt: starting <id> — queued behind another start or stop on the model server (12s)
```

The questions a start can ask, on the terminal (`/dev/tty`), default No:

```text
starting <id> will stop <ids>; continue? [y/N]
<server's message>; replace it with <id>? [y/N]
starting <id> loads it beside models of other providers:
  <id> — <memory>
  cannot tell whether <family> is serving a model
stop them first, keep them and start, or cancel? [s/k/N]
```

`--replace` answers the first two and `--others=keep|stop` the third. With no
terminal (stdin is not one) and no `--replace`, the launch exits 3 and nothing
is started:

```text
approval missing: starting <id> would stop <ids>; pass --replace to go ahead (there is no terminal to ask); nothing was started
approval missing: <server's message>; pass --replace to go ahead (there is no terminal to ask); nothing was started
```

With no terminal and no `--others`, other providers' models are kept and the
warning above (`wt: warning: starting <id> beside …`) is printed once. A "no"
at any question exits 3 with `declined — nothing was started`.

Ctrl+C during the start detaches: the job keeps running on the server, the
client session ends, and wt exits 2 with
`detached — job <job> keeps running on the model server (watch it with 'wt job <job>', cancel it with 'wt job <job> --cancel'); nothing was launched`.
A start that fails exits 2 with the server's message and `; nothing was launched`.
A server that does not answer, refuses the token (`check token_file under [model-server] in config.toml`)
or does not know the pin exits 1 before anything is registered or started.

## Start and stop on the model server

With a model server configured (`[model-server]` in `config.toml`, or
`WT_MODEL_URL`/`WT_MODEL_TOKEN_FILE`/`WT_MODEL_TOKEN`) `wt start` and `wt stop`
ask the server ([wt-model-server.md](wt-model-server.md)) instead of this
machine's providers. With none configured nothing here applies. The registry is
not read.

**`wt start [model]`** starts one local model the server lists.

- Without an id it opens the model picker over the server's local models and
  needs a terminal; without one it exits 1 with
  `the model picker needs a TTY; pass a model id (wt start ID) to start without it`.
  The picker lists the server's local rows, and a blocked row is refused by the
  start job rather than greyed out.
- A running model the gateway lists, or whose route the server could not read,
  is left alone: `<id> is already running` on stdout, exit 0. A model that is
  running but known not to be routed is started again, which adds the route.
- A start for a model whose start job is already running attaches to that job.
- A cloud model is the server's to refuse; its message is the error.
- The questions and their flags (`--replace`, `--others`) are those of "A
  launch on the model server" above, and so is the progress on stderr.
- Before the job wt registers a client session with the model, so the
  server's idle scan counts it; it ends the client session on every exit.
  After a successful start it says on stderr
  `wt: note: <id> is up; the model server stops it after its idle grace unless a client session uses it`
  and prints `<id> is running` on stdout.
- Ctrl+C detaches: the job keeps running, exit 2 with
  `detached — job <job> keeps running on the model server (watch it with 'wt job <job>', cancel it with 'wt job <job> --cancel')`.
  A "no" exits 3 with `declined — nothing was started`.

**`wt stop <model-id>`** stops one model through the server. A provider name and
`--all` are not stopped through the server: they are `wt-model stop <provider>`
and `wt-model stop --all` on the server's machine (above), and `wt stop` prints a
pointer to them and exits 1.

- Without an argument it opens a numbered picker over the server's running local
  models, marking each that client sessions use (`<id> (2 client session(s))`).
  It needs a terminal; without one it exits 1 with
  `the stop picker needs a TTY; pass a model id (wt stop ID) to stop without it`.
- A model other client sessions use asks first
  (`2 client session(s) use <id>; stop it anyway? [y/N]`). `--yes` answers it. With no
  terminal and no `--yes` the stop exits 3, nothing stopped:
  `approval missing: 2 client session(s) use <id>; pass --yes to stop it anyway (there is no terminal to ask); nothing was stopped`.
  The request names no client session: `wt stop` registers none.
- A stop the provider accepted but has not finished is a success (exit 0):
  `<id> — still finishing: …`.
- Success prints `<id> stopped` on stdout. Ctrl+C detaches as for a start.
- Picking several models stops them in turn. Each failure prints its reason once
  on stderr (`wt: <id>: …`) and the last line counts them (`2 of 3 stops failed`):
  exit 3 only when nothing was stopped and every stop was declined (nothing
  changed), otherwise 2 — a batch that stopped some and declined the rest is
  2. A detach ends the batch and the line says how many were not stopped.

**`wt job ID`** shows or cancels a job by id: [wt-job.md](wt-job.md).

## Selection screens

1. **Model screen** (`wt`, `wt start`, `wt smoke`): the shared model table.
   Rows that cannot start cannot be selected.
2. **Stop screen** (`wt stop`, and the exit flows after `wt` and
   `wt smoke`): running local models; the exit flows hide in-use models,
   `wt stop` shows them.

## `wt-model served <provider>`

A host command: it runs where `wt-model` runs, reading the registry there.
Under `wt`, `served` (its name before phase 4 stage 2) only points here and
exits 1.

Prints the model ids the provider's server is serving now, one per line, as
the server names them; `--json` prints `{"provider": …, "served": […]}`. It
answers for `omlx`, `mtplx`, `mlx_lm_server`, `rec` and `sushi` — the same probe `wt start`,
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

## `wt-model warm <provider> <model>`

A host command too; under `wt`, `warm` only points here and exits 1.

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
on a provider with `wt-model stop`) or a cancelled `wt stop` picker; `1` on any error, including a
cancelled `wt start` picker (`model selection cancelled`), Ctrl+C during a start
or stop, a provider wt stopped but that still answers, a `wt-model stop` that
could not tell what a provider is running and had no way to stop it regardless,
and `wt stop` given a provider name or `--all` (a pointer to `wt-model stop`).

`3` means approval missing, with nothing changed: a question that needed an
answer got none (no terminal and no `--yes`, `--replace` or `--others=keep|stop`
to answer it) or got a "no". The line names the flag
(`approval missing: pass --yes (there is no terminal to ask); nothing was stopped`).
The one question with a safe default is not a refusal: with no terminal and no
`--others`, `wt start` keeps other providers' models loaded, warns once and
goes on.

On the model server the codes are: `0` done (also "already running", and a stop
still finishing), `1` could not start (no terminal for a picker, a server that
does not answer or refuses the token, a model it does not know), `2` the job
failed or was detached with Ctrl+C, `3` declined or approval missing.

An error of `wt start` is printed once, on stderr, as `wt: <message>`. A
failed mtplx start ends with the server's last log lines
(`...; log tail: <lines>`): at most 512 bytes of the log, from the start of
a line (a last line longer than that is shown cut).

See also [`wt-smoke.md`](wt-smoke.md) and [`wt-job.md`](wt-job.md).
