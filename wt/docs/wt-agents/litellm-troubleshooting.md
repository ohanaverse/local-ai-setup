# LiteLLM Gateway Troubleshooting (wt drivers)

Learnings from the 2026-08-31 matrix test that launched every wt agent with
`ollama/glm-5.3-flash:cloud` through the LiteLLM proxy (`[gateway].mode =
"litellm"`, proxy at `http://localhost:4000`, model list in
`~/.config/litellm/config.yaml`). Each real launch went through `wt` and had
to answer a one-shot prompt on stdout.

## Matrix result (2026-08-31)

| Agent | Result | Notes |
|---|---|---|
| claude | ✅ works | `ANTHROPIC_BASE_URL` → gateway, `--model <registry-id>`; a `claude-code:unrecognized_model` notice is benign |
| pi | ✅ works | after the dedicated `litellm` provider fix (below) |
| opencode | ✅ works | after the custom-provider fix (below) |
| copilot | ✅ works | after proxy `litellm_settings.drop_params: true` (below) |
| codex | ✅ works | after per-model `additional_drop_params: ["reasoning_effort"]` (below) |
| agy | N/A | no model passthrough; the model is chosen inside its own TUI |
| shell | N/A | command executor, not an LLM agent |

Driver-level fixes shipped in wt: pi (`pi.go`, `pi_models.go`), codex
(`env_key` + gateway env var), opencode (custom provider + models map). The
copilot and codex fixes live in the LiteLLM proxy config, not in wt —
both are settings modelman may clobber (see the modelman settings-persistence
spec, `2026-08-31-litellm-settings-persistence-design.md`).

## Debugging playbook (what actually worked)

Work client-side → proxy-side → upstream, and check the client's own
wire-level truth before believing its error text:

1. **LiteLLM's own logs are the ground truth.**
   `~/.litellm.log` (access log: `POST /v1/responses 500`, `POST
   /v1/chat/completions 200`…) and `~/.litellm.err.log` (tracebacks with the
   exact exception, e.g. `TypeError: unhashable type: 'dict'`).
2. **Probe the gateway directly** to separate client bugs from gateway bugs:
   `curl http://localhost:4000/v1/models -H "Authorization: Bearer <key>"`,
   then `/v1/chat/completions` and `/v1/responses` with the model id.
3. **pi's model resolution is testable offline.** Load pi's bundle in node and
   ask it to resolve a `--model` value without launching anything:
   ```js
   const mod = await import("<pi-dist>/dist/bundle/chunks/chunk-OMWWHBTG.js");
   const rt  = await mod.ModelRuntime.create({ modelsPath: "/tmp/models.json" });
   const s   = await mod.resolveModelScopeWithDiagnostics(["litellm/ollama/glm-5.3-flash:cloud"], rt, {});
   // s.scopedModels[0].model is exactly what pi would send in the request body
   ```
   pi's session files under `~/.pi/agent/sessions/*/<session>.jsonl` record
   `model_change` (the resolved model id) and the per-message `error` — read
   them to see what a launch actually resolved.
4. **codex masks HTTP errors.** "We're currently experiencing high demand" is
   its retry-exhausted banner; `codex exec -c 'model_providers.…' …` with
   `RUST_LOG=debug` shows the request path, but the real status text is on the
   **server** side (litellm logs). Do not read "high demand" as throttling.
5. **`wt --cwd` avoids worktree churn** for launch tests; use a throwaway
   `-W <name>` when session-resume lookup must not find a prior session.

## Per-driver lessons

### pi — `--model <registry-id>` under provider `ollama` is unreachable

pi resolves `--model X/Y` by **splitting on the first slash** and matching the
remainder against the named provider's `models.json` entries; the matched
entry's `id` is then sent verbatim as the API model name (`model: model.id` in
its openai-completions streamer — verified in pi 0.84.4). Consequences:

- `--model ollama/glm-5.3-flash:cloud` = provider `ollama` + pattern
  `glm-5.3-flash:cloud`, which exact-matches the bare direct-mode entry →
  pi sends the **bare** name to LiteLLM → `Invalid model name passed in`.
- A `models.json` entry with `id: "ollama/glm-5.3-flash:cloud"` under the
  `ollama` provider is **unreachable by CLI resolution**: you cannot express
  the required double prefix, and the bare entry shadows it. (pi's TUI picker
  can still reach it — it keeps (provider, id) pairs internally.)

**Fix (shipped):** gateway entries live under a dedicated provider whose id
cannot appear as the first path segment of a registry model id — `litellm` —
keyed by full registry id, launched as `--model litellm/<registry-id>`. The
`ollama` provider stays local (`http://localhost:11434/v1`) so its bare-name
entries work directly.

### pi — empty `apiKey` invalidates pi's whole models.json

pi's models.json schema requires `apiKey` ≥ 1 char **per provider block**; a
violating provider makes pi reject the **entire catalog** ("No models
available", every model gone). wt's provider-revert must therefore write
pi's documented keyless placeholder `"ollama"`, never `""`.

### pi — legacy litellm artifacts are migrated, not kept

Older wt syncs wrote gateway values into the `ollama` provider block plus
`ollama/…`-prefixed entries under it. On the next litellm-mode sync wt now:
restores `ollama` to the local endpoint (bare entries 400 through the proxy),
prunes the wt-generated prefixed entries (unlaunchable by the grammar above;
bare and user-made entries are never touched), and creates the `litellm`
provider. Idempotent; safe to re-run.

### codex — custom providers read the API key from an env var

codex custom model providers have no inline `api_key`; the key comes from the
env var named by `-c model_providers.<id>.env_key="<ENV>"` and must be in the
child environment. Without it LiteLLM replies `401 Authentication Error, No
api key passed in`. wt sets `AGENT_WT_GATEWAY_API_KEY` and the matching
`env_key` override in litellm mode (direct local ollama needs no key, so the
direct mode still uses the four base overrides only).

### codex — litellm ≤ 1.98.0 responses → ollama_chat bridge crash (workaround: additional_drop_params)

codex ≥ 0.148 **removed `wire_api = "chat"`** (config parse error; see
[openai/codex#7782](https://github.com/openai/codex/discussions/7782)), so
codex always speaks the **responses API** to custom providers. LiteLLM's
responses→completion bridge for `ollama_chat/*` models crashes in
`OllamaChatConfig.map_openai_params`:

```
optional_params["think"] = value in {"low", "medium", "high"}
TypeError: unhashable type: 'dict'
```

codex sends `reasoning` as a structured object; litellm turns it into a dict
`reasoning_effort` and the `in` check explodes → **500** per attempt (litellm
retries 2×, codex retries 5×, then prints the generic "high demand" banner —
**not** actually provider throttling; `/v1/chat/completions` for the same
model answered 200 in the same minute). `litellm_settings.drop_params: true`
does NOT help (the param maps; the value type crashes), and
`-c model_supports_reasoning_outputs=false` does not stop codex from sending
it.

**Fix paths:**

1. **Workaround (applied & verified 2026-08-31):** add
   `additional_drop_params: ["reasoning_effort"]` to the affected model's
   `litellm_params` in `~/.config/litellm/config.yaml`, then restart the
   proxy. litellm then drops `reasoning_effort` before the ollama mapping
   runs, so the dict never reaches the broken `in` check:

   ```yaml
   - model_name: ollama/glm-5.3-flash:cloud
     litellm_params:
       model: ollama_chat/glm-5.3-flash:cloud
       api_base: http://localhost:11434
       additional_drop_params: ["reasoning_effort"]
   ```

   Cost: `reasoning_effort` is no longer mapped into `think` — the model's
   thinking level reverts to its default. Verified with a direct
   `/v1/responses` curl probe (200, was 500) and a `codex exec` one-shot
   through `wt` (green). Every `ollama_chat/*` entry needs this key for
   codex to work with it.

2. **Proper fix (upstream, unreleased):**
   [BerriAI/litellm#37452](https://github.com/BerriAI/litellm/issues/37452);
   fix PRs [#37465](https://github.com/BerriAI/litellm/pull/37465) /
   [#37467](https://github.com/BerriAI/litellm/pull/37467) (open as of
   2026-08-31, not in any release — PyPI latest = 1.98.0 = the crashing
   version). They extract `value.get("effort")` when the value is a dict.
   Once a release ships it, remove the `additional_drop_params` entries.

3. **Alternative:** change modelman's proxy entries to route these models
   through the OpenAI-compatible passthrough
   (`openai/<model>` + `api_base: http://localhost:11434/v1`) instead of
   `ollama_chat/*`, which skips the broken mapping entirely.

Once litellm applies the workaround, codex also prints two harmless lines to
stderr: `Model metadata for ... not found. Defaulting to fallback metadata`
(codex's local model db has never heard of the id) and some
`OutputTextDelta without active item` SSE noise (bridge streams text deltas
outside a declared item boundary); neither affects the result.

### copilot — `drop_params` is required at the proxy

copilot sends `parallel_tool_calls`, which litellm's `ollama_chat` mapping
rejects with `400 UnsupportedParamsError` ("To drop these, set
`litellm.drop_params=True`…"). LiteLLM prescribes the global fix:

```yaml
# ~/.config/litellm/config.yaml (requires proxy restart to take effect)
litellm_settings:
  drop_params: true
```

Applied 2026-08-31; copilot verified green with `ollama/glm-5.3-flash:cloud`
after `drop_params`. Note: `~/.config/litellm/config.yaml` is written by
modelman — if modelman reconciliation drops the block, re-add it or teach
modelman the setting.

### opencode — builtin `openai` provider is unusable for gateway models

opencode resolves model ids against its own catalog: the builtin `openai`
provider rejects a registry id (`Model not found: openai/ollama/glm-…`), and
its models.dev path speaks the **responses API** — the bridged stream maps
back broken (`"text part <uuid> not found"`). wt therefore declares a fully
custom provider in `OPENCODE_CONFIG_CONTENT`: `npm: "@ai-sdk/openai-compatible"`
(chat-completions wire), explicit `models` map keyed by the registry id,
model ref `agent-wt/<registry-id>` (opencode splits on the first slash), and
`small_model` pinned to the same gateway model — opencode's default
summarization model (`gpt-5-nano`) otherwise queries the proxy with a model
name it does not expose (400 noise in litellm's log on every run).

### claude / agy / shell

claude needed no changes in litellm mode. agy takes its model inside its own
TUI (no `--model`/env contract), and shell is not an LLM agent — neither is
routable to a specific gateway model by wt.

## Ops notes

- The proxy loads `~/.config/litellm/config.yaml` **only at startup** — after
  editing it, restart the proxy (locally: `kill <pid>` +
  `nohup litellm --config ~/.config/litellm/config.yaml --port 4000
  >> ~/.litellm.log 2>> ~/.litellm.err.log &`. On machines managed by
  modelman, restart via `MODELMAN_LITELLM_RESTART_CMD` — see
  [README · LiteLLM proxy lifecycle](README.md#litellm-proxy-lifecycle)).
- The litellm key for probes lives in `[gateway].api_key`
  (`~/.config/agent-wt/config.toml`) and is also in pi's models.json.
- Direct-mode ollama keeps working as before: the `ollama` provider block in
  pi's models.json must stay on `http://localhost:11434/v1` (wt reverts it if
  a stale gateway redirect is present); codex/copilot direct branches are
  unchanged.