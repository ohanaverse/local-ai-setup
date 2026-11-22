# Spike harness: LiteLLM aliases, groups and a subscription behind wt-model

Reproduces the checks in `../2026-11-22-litellm-aliases-groups-design.md`. Nothing here touches the live proxy, its database or any real credential: a scratch LiteLLM on port 4099 with a scratch Postgres database, and mock model backends on port 9100.

| File | What it is |
|---|---|
| `mock.py` | One server, one path prefix per backend (`/b/<name>/v1/...`), speaking OpenAI chat, Anthropic messages and the OpenAI Responses wire, with a per-backend failure mode (`POST /ctl/<name>/mode {"mode":"ok\|429\|500\|slow"}`) and a request log (`GET /ctl/log`) that masks credential headers to their first 12 characters. |
| `cfg.yaml` | The scratch proxy's config: static slot aliases, a subscription stand-in (`anthropic/` route with no `api_key`), a group with a fallback. |
| `lib.py` | Small helpers the spike scripts share. |
| `gateway-header-mode.patch` | The throwaway change to `internal/modelgateway/gateway.go` that spike 5 ran: accept the wt token in `x-wt-model-token`, forward the client's own `Authorization`, give LiteLLM its key in `x-litellm-api-key`. A feasibility probe, not a proposed diff. |

## Run it

```bash
createdb -h localhost litellm_alias_spike
python3 -I mock.py 9100 &
env -i HOME="$HOME" PATH="$PATH" \
  DATABASE_URL="postgresql://$(whoami)@localhost:5432/litellm_alias_spike" \
  LITELLM_MASTER_KEY=sk-spike-master LITELLM_SALT_KEY=sk-spike-salt \
  litellm --config cfg.yaml --host 127.0.0.1 --port 4099 &
```

LiteLLM 1.109.1 was the version tested. Wait for `curl localhost:4099/health/liveliness` to answer 200 (about 8 seconds), then drive it with the helpers in `lib.py`: `call("/v1/messages", ...)`, `mock("/ctl/sub/mode", {"mode": "429"})`, `last()` for what the backends received.

To test the gateway, build `wt-model` from a checkout with the patch applied, point a scratch HOME at the scratch proxy with `wt-model litellm set --url http://127.0.0.1:4099 --api-key sk-spike-master`, and run `wt-model serve --listen 127.0.0.1:8792 --token-file <file>` under that HOME with `WT_REGISTRY` and `WT_LITELLM_CONFIG` redirected (see the repo `CLAUDE.md`).

Stop everything afterwards and `dropdb litellm_alias_spike`.
