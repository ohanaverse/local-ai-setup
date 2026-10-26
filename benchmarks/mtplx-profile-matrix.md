# mtplx-profile-matrix — does an mtplx `--profile` change how a Claude Code slot copes with memory?

`mtplx-profile-matrix` is the experiment behind [#393](https://github.com/ohanaverse/local-ai-setup/issues/393):
an mtplx build serving a Claude Code slot sometimes answered HTTP 507
`insufficient memory` on a large prefill. wt always starts mtplx with `--model-id`, which makes
mtplx run its `sustained` profile, so the question was whether another profile (`turbo`,
`stable`) behaves differently. The script automates one run of that comparison. It is not a
throughput benchmark: it counts refusals and guard actions, and checks that the work got done.

## What one run does

For each round and each profile (interleaved: `sustained-1`, `turbo-1`, `stable-1`,
`sustained-2`, ...):

1. `wt model edit <model> --serve-arg --profile --serve-arg <profile>` (none for `sustained`,
   which is what wt starts), then `wt stop mtplx`, so every run begins with a cold server.
2. Resets a small scratch repo: a `median` that is wrong for even lengths, with four `unittest`
   tests, two of which fail. The **heavy** task adds `notes/`, five files of seeded filler
   words (about 117 KB), which Claude is asked to read first, so prompts grow to 40k tokens and
   more.
3. Runs two Claude Code turns non-interactively through the slot profile you name
   (`wt --cwd -A claude -M <slot profile> -- -p "..." --allowedTools ...`, then `--continue -p`).
4. Records, since the start of the run: LiteLLM error-log lines naming `insufficient memory`
   (one failed request writes several, so it **overcounts**); mtplx's memory-guard actions
   (`prefill_admission_shed`, `allocation_failure_shed`, `prefill_system_abort`) and
   `pressure_trim` lines from its own log; the proxy's request count, failures, average and
   maximum seconds and the largest prompt that succeeded; the wall time; and whether the tests
   pass at the end.

On exit, normal or not, it clears the `serve_args` it set and stops mtplx. It stops at once if
the very first turn fails (the setup is probably wrong), and refuses to start while any other
local model is loaded. Every command is echoed with a `+` before it runs.

## Usage

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
benchmarks/mtplx-profile-matrix --model mtplx/<org>/<name> --profile profile/<slot profile> --dry-run
benchmarks/mtplx-profile-matrix --model mtplx/<org>/<name> --profile profile/<slot profile> --runs 1
benchmarks/mtplx-profile-matrix --model mtplx/<org>/<name> --profile profile/<slot profile> \
    --task heavy --conditions "sustained turbo" --runs 3
```

- `--model` is the registered mtplx model; `--profile` a slot profile (`wt profile list`) that
  puts it on the slots Claude Code sends its prompt to (sonnet and haiku, say).
- `--conditions` names mtplx profiles (`stable`, `performance-cold`, `sustained`, `turbo`,
  `exact`, `max-diagnostic`); `sustained` means "wt's default".
- Results go to `--out` (default `/tmp/mtplx-profile-matrix-<timestamp>/`): `results.tsv`,
  `turns.tsv`, `outcomes.tsv` and each turn's output under `logs/`. Archive what matters into
  `results/`, as for the other benchmarks.
- A run loads the model (about 28 GB for a 27B) and turns take one to three minutes. Leave the
  machine alone and close memory-heavy apps while it runs; the result depends on what else holds
  memory.

The script uses `#!/bin/bash` (it needs nothing newer than bash 3.2), unlike the other scripts
here, which need bash 4 for associative arrays. It reads the LiteLLM Postgres database for the
request columns (`psql -d litellm`) and prints `n/a` there when the database is unreachable.

## What it can and cannot tell you

It tells you whether a profile refuses requests, aborts prefill, or leaves the work undone **on
the machine as it is when you run it**. It does not put the machine under memory pressure: the
first set of runs (see `results/mtplx-profile-matrix-2026-10-26.md`) found no refusals from
`sustained` or `turbo` on an idle machine, so it could not say whether `turbo` helps when memory
is tight. Testing that needs a deliberate memory ballast, which the script does not have.

Three runs per profile is a small sample; compare counts across profiles, not against the
numbers quoted in #393.
