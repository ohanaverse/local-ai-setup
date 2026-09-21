# `wt start` and `wt stop`

Start or stop local models directly, without launching an agent.

```bash
wt start                         # pick from every local model (needs a TTY)
wt start ollama/qwen3.8:27b-mlx  # start one model, no TTY needed
wt start <model> --replace       # replace a running model without asking
wt stop                          # pick from running models (needs a TTY)
wt stop ollama/qwen3.8:27b-mlx   # stop one model
wt stop ollama                   # stop every running model of a provider
wt stop <target> --yes           # skip the in-use confirmation
```

## `wt start [model]`

- `model` — `<provider>/<name>`, as with `-M`. Omitted: the screen-1 model
  picker over every configured and detected local model, running ones
  included (needs a TTY).
- Idle model: started through the same driver as `wt -M` (progress on
  stderr, Ctrl+C cancels). If the provider's single slot is occupied wt
  asks before replacing the running model; `--replace` skips the question.
- Already running: does nothing, exits 0 (`wt: <id> is already running`).
- Cannot start (not on disk, no lifecycle backend): exits 1 with the
  reason. In the picker these rows are unselectable and show the
  reason as a notice.
- Cloud or unknown id: exits 1.

## `wt stop [model|provider]`

- `<provider>/<name>` stops one running model; not running or unknown is
  an error. omlx and mtplx serve one process for all models, so stopping
  one also stops every other running model of that provider; wt prints
  the extra models it stops.
- A bare provider (`ollama`, `omlx`, `omlx-6bit`, `mtplx`) stops all its
  running models; nothing running exits 0 with a note. Other names are an
  error.
- No argument: the stop picker (needs a TTY). Unlike the exit-flow pickers
  it also lists models in use by other wt sessions, marked with their
  session count.

### In-use confirmation

If live wt sessions use the target, wt asks
`... in use by N live wt session(s) (models); stop anyway? [y/N]` on the
controlling terminal, default No. N is the total across the models being
stopped (a single-model provider counts once). `--yes` skips the question; with no terminal and no
`--yes`, the command fails and says to rerun with `--yes`.

## Selection screens

1. **Model screen** (`wt`, `wt start`, `wt smoke`): the shared model table.
   Rows that cannot start cannot be selected.
2. **Stop screen** (`wt stop`, and the exit flows after `wt` and
   `wt smoke`): running local models; the exit flows hide in-use models,
   `wt stop` shows them.

## Exit codes

`0` on success, no-op (already running, nothing to stop on a provider) or
a cancelled `wt stop` picker; `1` on any error, including a declined
confirmation and a cancelled `wt start` picker (`model selection canceled`).

See also [`wt-smoke.md`](wt-smoke.md).
