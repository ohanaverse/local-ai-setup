# Spike: cloud rows as LiteLLM database rows (Task 0 of the plan, #694)

Answers the five open questions of `../../plans/2026-11-24-cloud-rows-in-database.md` on LiteLLM 1.109.1, so
the plan's Decision 1 and the migration's order rest on measurements. Run on 2026-11-24.

**Method, and one deviation from the plan.** The plan said to run on the real proxy. This ran on a **scratch
LiteLLM** (the installed 1.109.1, port 14000) with its own scratch database `wt_spike_694` in the local
Postgres 16 (dropped afterwards), its own master key and salt key, and two stand-in upstreams on ports 14101 and
14102 (`upstream.py`). The restart and salt-key checks would have cut the live proxy's sessions, and the
earlier spike used a scratch proxy for the same reason. Nothing of the real proxy, its database, its plist or
any real key was read or changed. Every key below is a labelled sentinel (`sk-spike-upstream-SENTINEL` for the
upstream, `sk-spike-master` for the master key) or a `<placeholder>`.
The scratch proxy was started as
`env LITELLM_MASTER_KEY=sk-spike-master LITELLM_SALT_KEY=<salt> DATABASE_URL=postgresql://<user>@localhost:5432/wt_spike_694 WT_SPIKE_KEY=<sentinel> litellm --config config.yaml --port 14000`
(the database URL needs the user; without it Prisma answers P1010). Its log shows one warning at start,
`'Prisma' object has no attribute '_Prisma__engine'`, and every database operation below worked regardless.

## Results

| # | Question | Measured |
|---|---|---|
| 1 | Does LiteLLM resolve `os.environ/<NAME>` in a **database** row's `api_key`? | **Yes**, at request time, from the proxy's own environment: the upstream received the sentinel. With the variable **not set**, the row is accepted (HTTP 200) and a request fails with HTTP 500 `Missing credentials`; the upstream is never contacted, so no placeholder text is sent as a credential. |
| 2 | Where does the key, or the reference, appear? | Nowhere in plaintext. `/model/info` lists the row with **no `api_key` field at all** and neither the sentinel nor the reference text in the body. In the database table both `model` and `api_key` are stored as ciphertext (the `api_key` column was 84 characters of it): neither the sentinel nor `WT_SPIKE_KEY` appears in `litellm_params`. The one spend-log row has neither, and nothing in the proxy log has the sentinel. A **literal** key stored in a row (the alternative) behaves the same: sent upstream as the sentinel, absent from the listing, the table, the spend log and the proxy log. |
| 3a | Does a row survive a restart? | **Yes** with the same salt key: listed again and answering. |
| 3b | And with a different salt key? | The rows **vanish from the listing** (an empty list) and requests fail `400 Invalid model name`; nothing reaches the upstream. Under that wrong salt, **re-adding an existing id fails with HTTP 500** `Failed to add model to db`, and **deleting a stale id works** (200; the table row count went 2 to 1). Restarting with the original salt brought the remaining rows back. |
| 4 | Does a `PATCH` of a row's `api_base` take effect without a restart? | **Yes**: `PATCH /model/<id>/update` took 0.16 s, and the next three requests all went to the new upstream. The `os.environ/` reference was still resolved afterwards (the sentinel arrived). |
| 5 | A `config.yaml` row and a database row under one `model_name`? | The proxy lists **two deployments** under the name and splits the traffic between them (16 requests: 9 to the database row's upstream, 7 to the file row's). A silent two-deployment group, as phase 1 warned. |

## What this changes in the plan

1. **Decision 1 is not forced; its recommendation should be revisited.** Both a reference and a resolved key are feasible and both end as ciphertext in the database. The plan recommended the reference. The measurements add a cost to it that the draft did not have: `wt` today resolves `secret_ref` from **its own environment** and writes the value (the owner's note in the plan), so a reference would make every provider key a variable the **proxy's** environment must hold (its LaunchAgent plist), with a `bootout` and `bootstrap` to change one. That is a new operator duty and a restart for each key change. Storing the resolved key in the row keeps today's semantics, needs no change to the proxy's plist, and is a strict improvement on today's plaintext in `config.yaml` (ciphertext in the database, absent from the listing). This is reasoning from the measurements and the owner's note, not a measurement; whether a `PATCH` of `api_key` also takes effect without a restart was **not measured**. The owner decides.
2. **The salt key becomes load-bearing for cloud rows too.** Under a wrong salt every database row (the local routes and aliases wt already writes, and cloud rows after this change) silently disappears from the listing, and wt's add-first reconcile then fails with HTTP 500 for every id, because the stale rows are still in the table. The plan's Task 3 and 4 should treat "the listing is empty but adding a managed id answers 500" as a refusal that names the salt key as the likely cause, and must not delete rows it cannot see. Deleting a stale id by its id works, which is the repair.
3. **The migration's order is confirmed.** While both rows exist the two deployments are identical, so traffic is split harmlessly; adding the database row, confirming it by listing, and only then removing the file row never leaves a model unrouted. Removing the file row first would.
4. **Hot edits work**: a database cloud row can be re-pointed in about 0.2 s with no restart, which is the point of the issue.

## Not measured

A stream already running while a row is `PATCH`ed (the earlier spike measured it for an alias: it finished on the old backend); a `PATCH` of `api_key`; a real provider (OpenRouter, ollama cloud) or a real Claude login; the keyless `anthropic/` row of a subscription; a Redis-coordinated multi-proxy deployment; LiteLLM versions other than 1.109.1.

## Run it again

```bash
createdb -h localhost wt_spike_694
python3 -I upstream.py 14101 sk-spike-upstream-SENTINEL upstream.log &
python3 -I upstream.py 14102 sk-spike-upstream-SENTINEL upstream.log &
# config.yaml: model_list: [] with general_settings.store_model_in_db: true; start the proxy as above, then
# POST /model/new with litellm_params {model: openai/gpt-spike, api_base: http://127.0.0.1:14101/v1,
# api_key: os.environ/WT_SPIKE_KEY} and model_info {id: wt-spike:ref}, and POST /v1/chat/completions.
```

Stop everything afterwards and `dropdb wt_spike_694`.
