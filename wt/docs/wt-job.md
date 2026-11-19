# `wt job`

Show or cancel a job the model server is running or ran. A start or a stop on
the model server ([wt-model-server.md](wt-model-server.md)) is a job with an
id; a detach message names it (`detached — job 7f3a keeps running on the model
server …`), and `wt job` is how to look at it again or end it. It needs a
model server (`[model-server]` in `config.toml`, or `WT_MODEL_URL`).

```bash
wt job 7f3a             # job 7f3a — running — loading the model
wt job 7f3a --cancel    # cancel it, wait (progress on stderr) until it ends
wt job 7f3a --json      # the job as one JSON document
```

The result is one line on stdout, `job ID — STATE — STEP`, without the `wt:`
prefix: it is the answer, not a note. Progress while `--cancel` waits, and
every error, go to stderr (`wt: …`).

```text
job 7f3a — running — loading the model
```

A failed job also prints its problem as the error line on stderr; a cancelled
one prints nothing more, the result line says it.

## Flags

| Flag | Meaning |
|---|---|
| `--cancel` | Ask the server to cancel the job, then poll it (every half second, up to 30 seconds) until it is succeeded, failed or cancelled, with `wt: cancelling job ID — STATE (STEP)` on stderr whenever the state changes. Ctrl+C stops the wait; the cancel was already sent. |
| `--json` | Print `{"version": 1, "job": {…}}` (the server's job status) or, when the command fails, `{"version": 1, "error": {"message": …, "code": …}}`, one document on stdout with the same exit code and no error line on stderr. Shape: [job.sample.json](../../docs/contracts/job.sample.json). |

## Exit codes

| Code | Meaning |
|---|---|
| 0 | The job is queued, running or succeeded (for `--cancel`: it succeeded before the cancel took effect). |
| 1 | The model server does not know the job (it finished and was dropped from its list, or the server restarted), or it cannot be reached or refused the token. Nothing was cancelled. |
| 2 | The job failed or was cancelled (a cancel that worked ends here), or `--cancel` could not finish waiting after the cancel was sent (the wait failed, timed out or was interrupted): the error says so and `wt job ID` shows where the job is. |

An unknown id reads as
`the model server no longer knows job ID (it finished and was dropped from its list, or the server restarted) — run 'wt model list --server' to see whether the model came up`,
and with `--cancel` as
`no job ID on the model server — it is unknown, finished and dropped from the list, or the server restarted; nothing was cancelled`.

## Failure and recovery

- **Unknown job.** The server keeps finished jobs only for a while and forgets
  them all on a restart. Check the model itself with `wt model list --server`; start
  it again with `wt start ID` (a start for a model whose start job is still
  running joins that job).
- **Server unreachable.** Exit 1 with the server's address and what to fix
  (`url` or `token_file` under `[model-server]`). A job the server was running
  keeps running; look again once it is back.
- **A cancel that did not finish within 30 seconds** exits 2 and says the
  cancel was sent: the job may still be stopping. `wt job ID` shows where it
  is.

Design: [the session-split design](../../docs/superpowers/specs/2026-11-15-wt-model-session-split-design.md).
