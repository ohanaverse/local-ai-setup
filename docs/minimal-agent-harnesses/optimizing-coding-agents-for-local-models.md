# Optimizing Coding Agents for Local Models: Porting little-coder's Techniques to Claude Code, Codex, Copilot, OpenCode, and Beyond

## TL;DR
- **Most of little-coder's ~20 optimizations are portable in spirit, not literally.** The concrete, model-facing ones (write guards, read truncation, output repair, thinking caps, context watchdog, tool pruning, per-turn skill/knowledge injection) map cleanly onto **Claude Code hooks** and **OpenCode's `tool.execute.before/after` plugin API**; Codex, Copilot CLI, Aider, Goose, and Cline/Roo have partial equivalents through config keys, edit-format choices, toolshims, and compact prompts. No mainstream harness reproduces little-coder's per-turn skill selection or output-parser repair out of the box.
- **The single highest-leverage lever is the serving layer, not the harness.** Setting a real context window (`num_ctx`/`-c` ≥ 32K, 64K sweet spot), quantizing the KV cache (`q8_0` + flash attention), enabling prompt-cache reuse (`--cache-reuse`, prefix caching), using the correct tool-call parser/chat template, and using the model's recommended sampling (Qwen3 coding: temp 0.6, top_p 0.95, top_k 20, min_p 0) fixes the majority of "local agent doesn't work" failures across every harness.
- **Workflow discipline matters as much as config:** short one-task sessions, clearing context between tasks, compacting at ~70–80%, writing a PLAN.md/NOTES.md handoff and restarting, avoiding giant file reads (grep first, read ranges), truncating tool output, minimizing MCP/tool count, and shrinking AGENTS.md/CLAUDE.md — because small models degrade far faster than frontier models as context fills ("context rot").

## Key Findings

**little-coder is pi + ~20–27 extensions + 30 skill markdown files.** It does not fork pi; it launches pi with `--no-extensions` and explicitly wires in a curated set of TypeScript extensions that hook pi's lifecycle events (`before_agent_start`, `context`, `before_provider_request`, `tool_call`, `tool_result`, `turn_end`, `session_compact`). Its canonical target is a local llama.cpp server hosting Qwen3.6-35B-A3B on an 8 GB-VRAM laptop. Reported results: a 9.7B Qwen3.5 scored 45.56% on Aider Polyglot vs. a matched-model vanilla Aider baseline of 19.11% — the paper's central claim is that **scaffold–model fit, not model scale, is the primary lever** for small-model coding.

**Each harness exposes a different subset of the needed control points.** Claude Code is the most complete: 30 lifecycle hook events (PreToolUse can block/modify, PostToolUse can truncate output), skills with progressive disclosure, settings.json env controls (MAX_THINKING_TOKENS, CLAUDE_CODE_MAX_OUTPUT_TOKENS), and native local-model support via ANTHROPIC_BASE_URL. OpenCode is nearly as capable through its TypeScript plugin API. Codex CLI, Copilot CLI, Aider, Goose, and Cline/Roo offer config-level knobs but no general-purpose pre-tool interception.

**The serving layer + prompt-cache stability is where local agents live or die.** KV-cache quantization, flash attention, prompt-cache reuse, correct tool-call parsers, and byte-stable system prompts (so caches hit) determine whether an agent is usable at all.

## Details

### PART 1 — little-coder's optimization catalog (mechanism + why it helps small models)

little-coder ships these as independent pi extensions (from the repo's architecture listing) plus 30 skill markdown files. Below, each mechanism and why it helps a small local model:

1. **`--no-extensions` launcher + explicit wiring.** pi runs with all auto-discovery off, and the launcher loads exactly the bundled extension set via `--extension`. *Why:* deterministic, minimal context; nothing unexpected inflates the system prompt. Add an extension by dropping a directory in `.pi/extensions/`, remove one by deleting its directory.

2. **`write-guard`.** The `Write` tool refuses to operate on a file that already exists and returns a *structured error directing the model to Edit instead*; it also rewrites root-bare `/foo.md` paths to the cwd. The paper reports this guard fires on ~57% of exercises. *Why:* small models love to "helpfully" rewrite an entire file, silently destroying partially-working code. A tool-level refusal converts a destructive whole-file overwrite into a targeted edit.

3. **`read-guard`.** A `Read` that would overflow the context window is trimmed to its first 30 lines plus a "search instead" directive. *Why:* a single giant file read can blow the small context window and induce hallucination; forcing grep/search-first keeps context lean.

4. **`read-guard-edit`.** `Edit` refuses until the file has been `Read` this session (read-before-edit invariant). *Why:* guarantees edits match the file's exact current text (small models otherwise invent an `old_string` that doesn't match).

5. **`skill-inject` — per-turn tool-skill selection.** Selects which of 14 tool-usage skill cards to inject each turn, prioritized *error > recency > intent*. *Why:* instead of a giant static system prompt describing every tool, it injects just-in-time, compact usage guidance relevant to the current step. This is little-coder's signature mechanism and the hardest to reproduce elsewhere.

6. **`knowledge-inject` — algorithm cheat-sheet scoring.** Scores 13 algorithm cheat sheets against the prompt (word=1.0, bigram=2.0, injection threshold=2.0) and injects the best match; also a conditional-injection knowledge entry that fires on coding keywords and directs the model to surface local docs (`.docs/instructions.md`, `README.md`) before editing. *Why:* small models have weaker parametric knowledge; a targeted cheat sheet at the point of need substitutes for what a frontier model would know innately.

7. **`output-parser` — malformed tool-call repair.** Recognizes and repairs malformed tool calls: fenced ```` ```tool ```` blocks, `<tool_call>` XML, bare JSON, and native Pythonic formats (e.g., LFM2/Liquid's `<|tool_call_start|>[Read(path=…)]`). *Why:* small/quantized models frequently emit tool calls in slightly wrong formats; repairing them instead of erroring turns a failed turn into a successful one.

8. **`quality-monitor`.** Detects empty responses, hallucinated tool names, and repetitive loops, then issues a correction follow-up. *Why:* small models get stuck in loops and hallucinate; catching this in-harness prevents wasted turns and runaway context.

9. **`thinking-budget` cap.** Caps thinking tokens per turn (the paper cites a 2,048-token budget); when exceeded, generation is aborted, the partial reasoning trace is preserved and reinjected as assistant context, and the request is retried with thinking *disabled* — forcing the model to commit to an implementation. Fires ~0.90 times per exercise. *Why:* small reasoning models over-deliberate, consuming the entire window without ever writing code.

10. **`permission-gate`.** A bash safe-prefix whitelist (`ls`, `cat`, `head`, `tail`, `git log/status/diff`, `find`, `grep`, `cp`, `mv`, `mkdir`, `touch`, etc.) checked before pi's own confirmation. `rm`/`sudo` excluded. Controlled by `LITTLE_CODER_PERMISSION_MODE` (auto/accept-all/manual) and `LITTLE_CODER_BASH_ALLOW` (trailing whitespace is meaningful). *Why:* safe autonomy without a human in the loop on every step.

11. **`checkpoint`.** Snapshots files before Write/Edit. *Why:* cheap revert when a small model makes a bad edit.

12. **`tool-gating`.** Enforces an `_allowed_tools` set at both exec and schema levels. *Why:* fewer tools = fewer tool-schema tokens in the prompt and less chance the model picks the wrong tool.

13. **`turn-cap`.** `max_turns` abort (Polyglot unbounded, Terminal-Bench 40, GAIA 30). *Why:* bounds runaway loops.

14. **`benchmark-profiles` / per-model profiles in `.pi/settings.json`.** Reads settings → systemPromptOptions and sets temperature per `<provider>/<model-id>` key (context_limit, thinking_budget, temperature, benchmark_overrides). *Why:* each model needs its own sampling and budget.

15. **Context-overflow watchdog (compaction).** little-coder watches context usage at *every turn boundary* and triggers pi's compaction once usage crosses **80%** of the window (tunable via `LITTLE_CODER_COMPACT_AT_PERCENT`; disable with `LITTLE_CODER_NO_COMPACT_WATCHDOG=1`). As of v1.11.0 it also guards against a compaction loop (pauses automatic compaction if a compaction frees too little). *Why:* pi only re-checked compaction when the model went idle, so a long autonomous run could overflow mid-run; the watchdog prevents the overflow crash.

16. **`evidence` + `evidence-compact`.** A per-session evidence store (1 KB snippet cap) preserved across pi's auto-compaction. *Why:* keeps load-bearing facts alive through summarization (compaction is lossy).

17. **`extra-tools`, `shell-session`, `browser`, `subagent`/dispatch, `plan-mode`.** glob/webfetch/websearch; tmux-proxy shell; Playwright browser; isolated read/browse-only sub-coders (concurrency via `LITTLE_CODER_SUBCODER_CONCURRENCY`, default 2); an alt+p "research → ask → plan" flow. *Why:* sub-coders isolate research context from the main conversation; plan-mode separates planning from editing.

18. **Auto-detected context window.** little-coder reads the live `n_ctx` from llama.cpp's `/props` at startup and budgets against it. *Why:* prevents the classic silent-truncation failure where the harness assumes a bigger window than the server actually serves.

19. **Small-model extensions auto-disable for large/cloud models.** *Why:* the scaffolding is a crutch that would only get in a frontier model's way.

20. **~1000-token base system prompt (inherited from pi) + a short AGENTS.md, ~7k-token cold-start budget.** pi's minimal base is a deliberate design invariant. *Why:* every token of fixed overhead is re-sent every turn and competes with actual work.

### PART 2 — General cross-harness optimizations

#### Serving layer

**Ollama.** Set a real context window — the default is small (docs variously cite 2048/4096) and Ollama *silently truncates* longer prompts. Options: `OLLAMA_CONTEXT_LENGTH=32768 ollama serve` (server-wide), or `PARAMETER num_ctx 32768` baked into a Modelfile (most reliable), or `num_ctx` per request. Quantize the KV cache with `OLLAMA_KV_CACHE_TYPE=q8_0` (≈½ memory, negligible quality loss) or `q4_0` (≈¼, modest loss) — **requires flash attention** (`OLLAMA_FLASH_ATTENTION=1`; auto-on in current builds, now a three-state override). KV-quant only applies on flash-attention-supported architectures — otherwise Ollama silently falls back to f16 (unexpected OOM). Other knobs: `OLLAMA_KEEP_ALIVE` (avoid reload overhead), `OLLAMA_NUM_PARALLEL=1` (single agent → preserve cache locality and VRAM), `num_predict`.

**llama.cpp / llama-server.** Key flags: `-c` context size; `-ngl 99` GPU layers; `--flash-attn on`; `--cache-type-k`/`--cache-type-v` (`q8_0`/`q4_0` KV quant — needs flash attn); `--jinja` + the *matching* `--chat-template`/`--chat-template-file` (essential for tool calling); `--reasoning-format`/`--reasoning-budget`; prompt caching via `--cache-reuse 256` (KV shifting across shared chunks, min 256-token chunk), `--cache-ram` (default 8192 MiB; raise or `-1` for more resident cache, or `0` on memory-constrained boxes), `--ctx-checkpoints`/`--checkpoint-min-step` (slot save/restore); `--parallel`; `--n-cpu-moe` (offload MoE experts to CPU — the "22 GB model on 8 GB VRAM" trick); speculative decoding (`--spec-type draft-mtp`). Pitfalls: `--kv-unified` + `--cache-reuse` can conflict (issue #23493); changing the *beginning* of the prompt busts the cache and forces full reprocessing; a Metal + KV-offload crash (workaround `--cache-ram 0` / `--no-kv-offload`).

**vLLM.** Enable tool calling with `--enable-auto-tool-choice --tool-call-parser <parser>` (`hermes` for Qwen2.5, `qwen3_coder` or the newer/recommended `qwen3_xml` for Qwen3-Coder, `llama3_json` for Llama). Enable `--enable-prefix-caching` (radix prefix cache — big win for agent prompts that share a long prefix). Reasoning: `--reasoning-parser qwen3`; to disable thinking, `--default-chat-template-kwargs '{"enable_thinking": false}'`. Set `--max-model-len` to your real budget; `--max-num-seqs 1` for a single agent (parallelism steals cache locality). Known bug: the default `qwen3_coder` parser can emit an infinite `!!!!` stream on long tool-call inputs — switch to `qwen3_xml`; the streaming `hermes` parser has a bug returning raw text instead of parsed tool_calls (issue #31871).

**LM Studio.** GUI server on port 1234; exposes OpenAI-compatible and (0.4.1+) an Anthropic-compatible endpoint. Set context window in the loader; enable KV-quant/flash-attention in loader settings. "Serve on local network" to bind 0.0.0.0.

#### Prompt-cache-friendly design
KV/prefix caches only hit if the prompt prefix is byte-stable across turns. Keep the system prompt and tool definitions fixed; **avoid dynamic timestamps at the start of the prompt**; put any per-turn injected content at the *end* of context. A single changed byte near the front forces the server to reprocess the whole prompt (expensive prefill). This is why little-coder injects skills/knowledge per-turn at the end, and why Claude Code's per-turn attribution header notoriously invalidates local KV caches (~90% slowdown).

#### Sampling settings (small models, agentic coding)
Qwen's official model-card recommendations (representative for the class) — verbatim: *"For thinking mode, use Temperature=0.6, TopP=0.95, TopK=20, and MinP=0 … DO NOT use greedy decoding, as it can lead to performance degradation and endless repetitions"*; non-thinking mode *"Temperature=0.7, TopP=0.8, TopK=20, MinP=0"*, and raise `presence_penalty` toward **1.5 for quantized models to suppress repetitive outputs** (but too high causes language mixing). For Qwen3.6:
- **Thinking, general:** temp 1.0, top_p 0.95, top_k 20, min_p 0.0, presence_penalty 0.0–1.5, repetition_penalty 1.0.
- **Thinking, precise coding (WebDev):** temp 0.6, top_p 0.95, top_k 20, min_p 0.0, presence_penalty 0.0.
- **Non-thinking / Instruct:** temp 0.7, top_p 0.80, top_k 20, min_p 0.0, presence_penalty 1.5.

Pitfall: aggressive `repeat_penalty` can corrupt tool-call JSON/XML (it penalizes structural tokens like braces/quotes) — keep it at 1.0 for tool-calling steps. For most non-Qwen coding models, temp 0–0.2 is fine; follow the model card.

#### Tool-calling reliability
Choose models with strong *native* tool calling: Qwen3-Coder, Devstral, GLM-4.x, gpt-oss, recent Gemma. The three things that break tool calls: (1) wrong/missing chat template — serve with `--jinja` and the model's proper template; (2) wrong parser on the server (`qwen3_xml` vs `qwen3_coder`, `hermes` for Qwen2.5); (3) too many tools — each tool schema costs tokens and dilutes attention. For models without native tool calling, use a *toolshim* (Goose `GOOSE_TOOLSHIM=true`) that reformats a text protocol into tool calls. Shorten tool descriptions and limit the exposed tool set.

#### Thinking / reasoning control
For tool-calling steps, thinking usually *hurts* small models (they deliberate instead of acting). Cap or disable it: little-coder's thinking-budget cap; Qwen `/no_think` soft switch or `enable_thinking=false` hard switch; vLLM `--default-chat-template-kwargs '{"enable_thinking": false}'`; Codex `model_reasoning_effort = "low"`; Claude Code `MAX_THINKING_TOKENS`. Reserve thinking for a dedicated plan step, then turn it off for execution.

#### Context management & workflow
- **Shorter sessions, one task per session** — small models degrade faster than frontier models as context fills.
- **Compact/clear proactively** at ~70–80% (not the default late trigger).
- **Handoff notes:** write a PLAN.md / NOTES.md, then `/clear` and restart with the note — a cheap, lossless "compaction."
- **Avoid giant reads:** grep/glob first, read line ranges, truncate command output (head/tail), cap tool-output size.
- **git checkpoints** for cheap revert.
- **Sub-agent / fresh-context delegation** for research so the main thread stays lean.
- **Keep AGENTS.md/CLAUDE.md very short;** disable unused MCP servers/tools (each adds thousands of tokens of schema, re-sent every turn).

#### Model-size guidance
- **~4B–8B:** only viable for tightly-scoped edits; expect editing-format errors; use whole-file edit format, compact prompts, 8–16K context.
- **14B–32B / MoE like Qwen3-Coder-30B-A3B:** the practical sweet spot for local agents; 32–64K context, native tool calling, works with compact prompts. Qwen3-Coder-32B wants ~24 GB VRAM; the 14B at Q4 fits ~8.7 GB. **AMD's testing (Cline blog) found "models smaller than Qwen3 Coder 30B consistently fail with Cline, producing broken outputs or refusing to execute commands properly"** — the named failures include gpt-oss-20b, bytedance/seed-oss-36b, and deepseek-r1-0528-qwen3-8b.
- **70B+ / large MoE (gpt-oss-120b, GLM-4.x):** best quality, needs a server or high-RAM Apple Silicon; MoE active-param count drives latency.

#### Effective vs advertised context ("context rot")
Advertised context ≫ usable context, and this hits small models hardest:
- **NVIDIA RULER** (Hsieh et al., COLM 2024, arXiv:2404.06654): *"while all models claim context size of 32k tokens or greater, only half of them can effectively handle sequence length of 32K by exceeding a qualitative threshold"* — usable context is well under the advertised figure.
- **NoLiMa** (Modarressi et al., Adobe Research, ICML 2025, arXiv:2502.05167): *"At 32K … 11 [of 13] models drop below 50% of their strong short-length baselines"* — e.g., GPT-4o fell from 99.3% short-context to 69.7% by 32K.
- **"Lost in the Middle"** (Liu et al. 2023): a U-shaped accuracy curve — accuracy drops 20–30 points when the answer sits mid-context, replicated across GPT-3.5-Turbo, GPT-4, Claude-1.3, LongChat-13B, MPT-30B, and Cohere Command; Chroma's context-rot study confirms it and finds smaller (7–8B) models degrade most.

Implication: **don't fill the window just because you can** — keep the working set small and near the ends.

#### Hardware/VRAM tradeoffs
KV cache grows linearly with context and, at f16, can exceed the model's own weight footprint past 32K. Three independent knobs: model quant (Q4/Q5/Q8), KV-cache quant (f16/q8_0/q4_0), and context length. On an 8–12 GB card: q4_K_M weights + q8_0 KV + 16–32K context is a workable combo; drop to q4_0 KV or 16K context if you OOM.

### PART 3 — Per-harness sections

#### a) Claude Code
**Local model wiring.** Claude Code speaks the Anthropic Messages API. Ollama (v0.14.0+) and LM Studio (0.4.1+) expose native Anthropic-compatible endpoints; otherwise put LiteLLM or claude-code-router in front of an OpenAI-compatible server. Minimal `~/.claude/settings.json`:
```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "http://localhost:11434",
    "ANTHROPIC_AUTH_TOKEN": "ollama",
    "CLAUDE_CODE_ATTRIBUTION_HEADER": "0",
    "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"
  }
}
```
Map model tiers with `ANTHROPIC_DEFAULT_SONNET_MODEL` / `ANTHROPIC_DEFAULT_HAIKU_MODEL` / `ANTHROPIC_DEFAULT_OPUS_MODEL` to your local model name (otherwise Claude Code requests a nonexistent `claude-sonnet-…` and the server rejects it). **Critical gotcha:** the attribution header must be `0` in settings.json (not a shell env var) — otherwise it changes every request and invalidates the local KV cache (~90% slower).

**Hooks (the little-coder-equivalent control plane).** Claude Code has ~30 lifecycle events. The load-bearing ones:
- **PreToolUse** — fires *before* a tool runs; exit code 2 (or `permissionDecision: "deny"`) *blocks* the call and feeds stderr back to the model. This is your **write-guard** (match `Write|Edit`, deny on existing files) and **bash permission gate**. It can also *modify* tool input.
- **PostToolUse** — runs after; can truncate verbose output via `hookSpecificOutput.updatedToolOutput`. This is your **read-guard / output-truncator**. Since v2.1.133 hooks receive the active `/effort` level, enabling effort-aware truncation.
- **UserPromptSubmit** — inject per-turn context (a lightweight **knowledge-inject** equivalent).

Configured in `.claude/settings.json` (project), `~/.claude/settings.json` (global), or `.claude/settings.local.json`:
```json
{
  "hooks": {
    "PreToolUse": [
      { "matcher": "Bash", "hooks": [{ "type": "command", "command": ".claude/hooks/guard.sh" }] }
    ],
    "PostToolUse": [
      { "matcher": "Write|Edit", "hooks": [{ "type": "command", "command": "npx prettier --write \"$CLAUDE_TOOL_INPUT_FILE_PATH\"" }] }
    ]
  }
}
```
**Output-repair** (little-coder's output-parser) is *not* directly reproducible — Claude Code parses tool calls internally; the closest mitigation is a correct server-side chat template/parser, or routing through claude-code-router/LiteLLM to normalize formats.

**Skills (SKILL.md) + progressive disclosure = the skill-inject equivalent.** At startup Claude loads only each skill's YAML name+description (~100 tokens each) into the system prompt; the full SKILL.md body (keep under ~500 lines) loads only when the skill triggers; bundled scripts load only when needed. This is the closest mainstream analog to little-coder's per-turn skill selection, though triggering is intent-based rather than error/recency-ranked.

**Shrinking the fixed overhead.** Community measurements put a bare "hi" at ~20,000–31,000 tokens (system prompt + CLAUDE.md + memory + MCP tool schemas + skill listings), all re-sent every turn (issue #46526, #52979). Built-in tools ~10k tokens; each MCP server adds thousands (GitHub MCP measured in the tens of thousands). Levers: keep CLAUDE.md tiny; disconnect unused MCP servers; use `/context` to audit; rely on deferred/allow-listed tool loading. Thinking/output caps: `MAX_THINKING_TOKENS`, `CLAUDE_CODE_MAX_OUTPUT_TOKENS`. Context management: `/compact`, `/clear`. **Known local-model failure modes:** search/tool loops, silent tool-call format mismatches, context truncation when the served window is smaller than Claude Code assumes. Fixes: correct parser/template, a LiteLLM fallback to a cloud Haiku on tool-call failure, 32K floor / 64K sweet spot.

#### b) OpenAI Codex CLI
**Local wiring.** Config at `~/.codex/config.toml`. Current Codex builds speak the **Responses API** (`wire_api = "responses"`), not Chat Completions — point at Ollama's Responses-compatible endpoint or a translating proxy:
```toml
model = "gpt-oss:120b"
model_provider = "ollama"

[model_providers.ollama]
name = "Ollama"
base_url = "http://localhost:11434/v1"
wire_api = "responses"
```
Or use `--oss` with `oss_provider = "ollama"` (or `lmstudio`). Since Codex 0.134.0, profiles are independent `~/.codex/<profile>.config.toml` files (Codex no longer reads `[profiles.xxx]` tables). Launch: `codex --oss --profile <name>`. Note: Ollama support has assumed localhost and ignored a LAN `base_url` in some builds (issue #8240).

**Relevant config keys:**
```toml
model_reasoning_effort = "low"          # minimal|low|medium|high|xhigh — cap thinking
model_context_window = 32768            # override advertised window (see caveat)
model_auto_compact_token_limit = 24000  # force earlier compaction
tool_output_token_limit = 12000         # cap runaway file reads — read-guard equivalent
compact_prompt = "Summarize focusing on file paths and decisions"
model_verbosity = "low"
```
`tool_output_token_limit` is Codex's built-in **read-guard/output-truncator**. `model_reasoning_effort = "low"` is the **thinking-budget** analog (also `Alt+,`/`Alt+.` mid-session). `model_auto_compact_token_limit` is the **compaction watchdog** analog. **Caveat:** issues report `model_context_window`/`model_auto_compact_token_limit` not being honored (Codex clamps to ~258K/272K; setting a custom window can break auto-compaction by poisoning the token counter — issues #16068, #19185). **AGENTS.md** is auto-discovered (nearest to the edited file wins in monorepos); keep it short. **Sandbox/approval:** `sandbox_mode = "read-only"|"workspace-write"|"danger-full-access"`, `approval_policy`. **Hooks:** Codex has no general pre-tool hook system; it has a multi-agent/subagent system (`[features] multi_agent = true`) and MCP support. **No write-guard/output-repair equivalent** beyond sandbox + `tool_output_token_limit`. Community experience running gpt-oss-20b/120b and Qwen locally is positive for scoped tasks; 32K is the safe window for Qwen3 27B per community guides.

#### c) GitHub Copilot
**Copilot CLI BYOK/local (since April 7, 2026).** Configure via environment variables before launching `copilot`:
```bash
export COPILOT_PROVIDER_BASE_URL=http://localhost:11434   # Ollama/vLLM/Foundry Local/OpenAI-compatible
export COPILOT_MODEL=qwen3-coder
export COPILOT_PROVIDER_TYPE=openai                        # openai (default) | azure | anthropic
# export COPILOT_PROVIDER_API_KEY=...                      # not needed for local Ollama
export COPILOT_OFFLINE=true                                # air-gapped: no contact with GitHub servers
```
Models **must support tool calling and streaming.** With BYOK, GitHub login is optional (adding it re-enables `/delegate`, GitHub Code Search, GitHub MCP). All built-in sub-agents (explore, task, code-review) inherit the provider config — no per-agent routing. GitHub recommends a 128K context window for complex tasks. **VS Code / Visual Studio 2026 / JetBrains agent mode** support Ollama as a BYOK provider (VS 2026: install the official Ollama Marketplace extension, not the deprecated built-in provider; JetBrains added Ollama BYOK Aug 11, 2026). Inline completions still require a GitHub account and stay cloud-side. **Customization:** `.github/copilot-instructions.md` (the AGENTS.md analog). **No documented hook/pre-tool-interception API, no write-guard/output-repair/skill-inject equivalent** as of 2026 — Copilot CLI is the most closed of the five for small-model scaffolding. If the BYOK config is wrong, the CLI errors out (no silent fallback to GitHub-hosted models).

#### d) OpenCode
**Local wiring** in `~/.config/opencode/opencode.json` (global) or `./opencode.json` (project):
```json
{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "ollama": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "Ollama (local)",
      "options": { "baseURL": "http://localhost:11434/v1" },
      "models": { "qwen3-coder": { "name": "Qwen3 Coder (local)" } }
    }
  }
}
```
Same pattern works for llama.cpp/LM Studio/vLLM (change `baseURL`). Plugins like `opencode-models-discovery` auto-list local models. Docs note: **if tool calls aren't working, increase `num_ctx` in Ollama (start 16K–32K).**

**Plugin API = the little-coder extension-hook equivalent (the strongest match of any mainstream harness).** Plugins live in `.opencode/plugins/` (project) or `~/.config/opencode/plugins/` (global; singular `plugin/` also works). Confirmed hooks: `tool.execute.before`, `tool.execute.after`, `chat.message`, `chat.params`, `event`, `experimental.session.compacting`. A `tool.execute.before` hook can **throw to block** (write-guard, bash-gate) or **mutate `output.args`** (read-truncation, command sanitizing) — the direct analog of little-coder's write-guard/read-guard/permission-gate. Official example pattern:
```js
// .opencode/plugins/guard.js
export const Guard = async ({ project, $ }) => ({
  "tool.execute.before": async (input, output) => {
    if (input.tool === "read" && output.args.filePath.includes(".env"))
      throw new Error("Do not read .env files")
    if (input.tool === "write") throw new Error("Use edit on existing files")
  }
})
```
`chat.params` sets temperature/top_p per request (a sampling-profile analog); `experimental.session.compacting` can inject preserved context (an evidence-compact analog). **Caveats:** `tool.execute.before` does *not* intercept subagent (task-tool) calls (issue #5894); the `permission.ask` hook is dead code since v1.3.0 (use the `permission.asked` event instead).

**Custom agents/modes + per-agent tool disabling = tool-gating equivalent.** Define agents in `opencode.json` under `"agent"` or as markdown in `agents/` (filename = agent name, YAML frontmatter). Built-ins: **Build** (primary, all tools) and **Plan** (primary, edits/bash default to `ask`); subagents General/Explore/Scout. Disable tools per agent via the `permission` field (preferred) or the deprecated-but-working `tools` map:
```json
{
  "$schema": "https://opencode.ai/config.json",
  "agent": {
    "reviewer": {
      "mode": "subagent",
      "permission": { "edit": "deny", "bash": { "*": "ask", "git status *": "allow" } },
      "tools": { "write": false, "edit": false }
    }
  }
}
```
**Context/compaction:** the `compaction` key — `{"compaction": {"auto": true, "prune": false, "reserved": 10000}}` (`prune` removes old tool outputs — a token saver; there is no top-level `autocompact`). **`small_model`** routes lightweight tasks (title generation) to a cheaper model. **Global tool disabling:** `{"tools": {"write": false, "bash": false}}`. **AGENTS.md** is auto-discovered (generate via `/init`); the `instructions` array merges extra files/globs. Keep AGENTS.md short — OpenCode re-sends it each turn.

#### e) Others (briefly)
**Aider.** Use the `ollama_chat/` prefix (not `ollama/`) and set `OLLAMA_API_BASE`; Aider auto-sizes Ollama context to request+8K but still raise `num_ctx` (32K) via `.aider.model.settings.yml` or a Modelfile. **Edit format is the key small-model lever:** default is `diff`/`udiff`; for weaker/quantized models force `--edit-format whole` (whole-file output is more reliable, catching the "search/replace fails" failure). `--map-tokens` controls repo-map size (e.g., `--map-tokens 1024`; force it on if auto-disabled). Use a **conventions file** (read-only) for persistent instructions, `/read-only` + selective `/add` to keep the working set to 1–3 files, and **architect mode** (a reasoning "architect" plans, a cheaper "editor" emits diffs) to split plan/execute. Aider's docs are blunt: most local models are "just barely capable" and quantization worsens edit errors.

**Goose.** Extensions (MCP) are the tool surface — keep them minimal. `GOOSE_CONTEXT_STRATEGY` (`summarize`/`truncate`/`clear`) sets auto-behavior at the ceiling; `GOOSE_AUTO_COMPACT_THRESHOLD` (default 0.8) sets the compaction trigger; **`GOOSE_CONTEXT_LIMIT`** must be set manually for local models — Goose falls back to a hardcoded 128K for unknown model names (issues #8835, #10058), which overflows small windows. **`GOOSE_TOOLSHIM=true`** enables a tool-call shim for models without native tool calling (Qwen/DeepSeek/Kimi/local Ollama). Max Turns (default 1000; set ~10 for local) bounds loops.

**Cline / Roo Code / Kilo Code.** Cline's **"Use Compact Prompt"** reduces the system prompt to ~10% of full size (an ~90% cut) — explicitly built for local/small-context models; the trade-off (per Cline/AMD guide) is *"you lose access to MCP tools, Focus Chain, and MTP features."* Set the context window to match the server (the guide says *"Match your context window to LM Studio's setting: 262,144 tokens"* for Qwen3-Coder-30B) and raise the request **timeout** (60–180s) since local prefill is slow. Use Plan mode first, approve checkpoints (don't auto-approve), keep sessions short, start a new task when context grows. Roo Code historically sent a large prompt (issue #7550 requests a compact-prompt option); Kilo Code and Void send more compact prompts by default. Auto-compact can silently fail on some local models served via llama-server (issue #7772).

**Continue.dev.** Config-driven (`config.yaml`/`config.json`) with Ollama/OpenAI-compatible providers; primarily autocomplete + chat + agent; context providers are explicit and prunable. Fewer autonomous-agent guardrails than the above — a lighter-weight assistant for local models.

**gptme.** Minimal, tool-first local-friendly agent; small toolset by design, good for scripted single-task runs; lacks the hook/skill machinery of Claude Code/OpenCode.

**Pi (what little-coder did *not* change).** pi's agent loop, multi-provider API, TUI, session tree, compaction engine, extension model, four built-in tools (read/write/edit/bash), and ~1000-token system prompt are all upstream pi. little-coder adds only extensions and skills on top; it deliberately keeps pi's minimal base rather than enlarging the system prompt.

### PART 4 — Mapping table: little-coder optimization → equivalent per harness

| little-coder mechanism | Claude Code | Codex CLI | Copilot CLI | OpenCode | Aider / Goose / Cline |
|---|---|---|---|---|---|
| Write-guard (refuse whole-file overwrite) | PreToolUse hook (deny on `Write` to existing) | Not directly (sandbox only) | Not possible | `tool.execute.before` throw on `write` | Aider `--edit-format whole` mitigates; others no |
| Read-guard / truncated reads | PostToolUse `updatedToolOutput` | `tool_output_token_limit` | Not possible | `tool.execute.before` mutate args / `.after` | Goose context strategy; no per-read guard |
| Read-before-edit | PreToolUse guard (custom) | Not built-in | Not possible | `tool.execute.before` custom | Aider tracks added files; partial |
| Output-parser (repair malformed tool calls) | No (fix at server template/parser) | No | No | No (server-side) | Goose `GOOSE_TOOLSHIM` (closest) |
| skill-inject (per-turn skill selection) | Skills + progressive disclosure (intent-triggered) | No | No | Skills + custom plugin | No |
| knowledge-inject (cheat sheets) | UserPromptSubmit hook / Skills | AGENTS.md (static) | copilot-instructions.md (static) | `chat.message` plugin / AGENTS.md | conventions file (static) |
| quality-monitor (loop/hallucination) | Stop/PostToolUse hooks (custom) | No | No | `event`/`tool.execute.after` plugin | Goose Max Turns (loop bound only) |
| thinking-budget cap | `MAX_THINKING_TOKENS` | `model_reasoning_effort` | No | `chat.params` / model config | vLLM/Ollama `enable_thinking=false`; `/no_think` |
| permission-gate (bash whitelist) | PreToolUse deny + permissions | `approval_policy`/sandbox | Approval prompts | `permission.bash` glob rules | Goose tool permissions; Cline approvals |
| checkpoint (snapshot before edit) | Hook + git | git | git | `snapshot` (default on) | Aider auto-commits; Cline checkpoints |
| tool-gating (limit tools) | Disable tools/MCP, allow-list | MCP config | Fixed sub-agents | Per-agent `tools`/`permission` | Goose: fewer extensions |
| turn-cap | Not built-in (hook) | Subagent limits | No | `subagent_depth` | Goose Max Turns |
| per-model profiles (temp etc.) | settings.json env / model map | profiles + `model_reasoning_effort` | env vars only | `chat.params` / `small_model` | Aider model settings yml |
| compaction watchdog (~80%) | Auto-compact + `/compact` | `model_auto_compact_token_limit` | Auto (128K rec.) | `compaction.auto`/`reserved` | Goose `GOOSE_AUTO_COMPACT_THRESHOLD` |
| evidence store across compaction | Skills/files on disk | AGENTS.md persist | No | `experimental.session.compacting` | NOTES.md handoff (manual) |
| auto-detect served context | Manual (set model map) | `model_context_window` (buggy) | Manual | Manual / discovery plugin | Aider auto-sizes Ollama ctx |
| short base system prompt | Large (~20–31k) — minimize CLAUDE.md/MCP | Moderate | Large/opaque | Moderate | Cline "Compact Prompt" (~10%) |

### PART 5 — Workflow playbook for local-model agent sessions
1. **Serve correctly first.** Real context window (≥32K, 64K if RAM allows); flash attention on; KV cache `q8_0`; correct chat template + tool-call parser; prompt-cache reuse enabled; single sequence (`num_parallel`/`max_num_seqs = 1`).
2. **Pick a capable model.** Qwen3-Coder-30B-A3B / Devstral / GLM-4.x class for real agent work; 14B only for scoped edits; sub-8B for autocomplete-style help.
3. **Set sampling per the model card** (Qwen coding: temp 0.6, top_p 0.95, top_k 20, min_p 0; never greedy; repeat_penalty 1.0 for tool calls; presence_penalty 1.5 on quantized models if it loops).
4. **Shrink fixed overhead.** Tiny AGENTS.md/CLAUDE.md; disconnect unused MCP servers; enable the harness's compact/small-model prompt (Cline "Use Compact Prompt", pi minimal base).
5. **Cap thinking for execution;** allow it only in a dedicated plan step.
6. **Scope the task.** One task per session; give exact file paths and a test to run; ask for diffs, not whole-file rewrites (or force whole-file if the model can't diff).
7. **Guard tools.** Write-guard/read-truncation via hooks (Claude Code) or plugins (OpenCode); bash whitelist; `tool_output_token_limit` (Codex).
8. **Grep before read;** read ranges; truncate command output.
9. **Watch context;** compact or, better, write a NOTES.md/PLAN.md handoff and `/clear` at ~60–70%.
10. **Checkpoint with git;** revert freely.
11. **Delegate research** to a sub-agent/fresh context to keep the main thread lean.
12. **Escalate on failure:** on repeated tool-call failures or loops, fall back to a cloud model for that step (LiteLLM router) or simplify the task.

## Recommendations
- **Start with the serving layer, not the harness.** If you do nothing else: set `num_ctx`/`-c` ≥ 32K, enable flash attention + `q8_0` KV cache, and verify your tool-call parser/chat template. This resolves the majority of "the agent just doesn't work locally" reports. *Threshold that changes this:* if a bare "hi" or a single tool call emits raw/garbled tool syntax, your template/parser is wrong and must be fixed before anything else matters.
- **Choose the harness by how much scaffolding you need.** For maximum little-coder-style control, use **OpenCode** (`tool.execute.before/after`, per-agent tool permissions, `small_model`, compaction) or **Claude Code** (30 hook events, skills/progressive disclosure) — the only two that let you reproduce write-guards, read-truncation, and per-turn injection cleanly. Use **Codex CLI** for `tool_output_token_limit` + reasoning-effort caps with minimal setup. Treat **Copilot CLI** as convenient but closed (no hooks/guards). Use **Aider** for git-native scoped edits with whole-file format. Use **Goose** when you need the toolshim for a non-tool-calling model.
- **Adopt the workflow playbook regardless of harness.** The workflow levers (short sessions, handoff notes, grep-first, tool-output truncation, tiny memory files, minimal MCP) deliver more reliability on small models than any single config flag, because they directly counter context rot.
- **If you want little-coder's exact behavior, just run little-coder** (or lift its extensions) — it's Apache-2.0, and its output-parser, quality-monitor, and skill-inject are the parts no other harness reproduces. The extensions are documented as transplantable reference implementations.
- **Escalation threshold:** if a local model fails a task twice with correct serving + guards, the fix is a larger-context or larger model, not more scaffolding — little-coder's own compaction-loop guard makes the same point ("the real fix is a larger-context model or `-c` window").

## Caveats
- **Version drift.** These tools ship weekly. Several keys are community-reported and not fully cross-checked against official docs — e.g., Codex's `model_context_window`/`model_auto_compact_token_limit` are reported *not* honored in some builds (clamped ~258K/272K; can break auto-compaction), and Codex profile format changed at 0.134.0 (independent `<profile>.config.toml` files). Verify against your installed version with `/context`, `--list-models`, `llama-server --help`, etc.
- **little-coder internals** are described from its README/architecture listing and the "Honey, I Shrunk the Coding Agent" Substack paper; exact scoring constants (word=1.0/bigram=2.0/threshold=2.0; 2,048-token thinking budget; 80% compaction; 30-line read trim; ~57% write-guard fire rate; ~0.90 thinking-cap fires/exercise) are from those sources and extension descriptions, not a line-by-line code audit.
- **Ollama Responses API for Codex** requires a Responses-compatible endpoint; older Ollama serves only Chat Completions and needs a translating proxy. Ollama-on-LAN with Codex has an open localhost-assumption bug (#8240).
- **KV-cache quant** only applies when flash attention is supported for that architecture; otherwise the server silently falls back to f16 (unexpected VRAM/OOM). Metal + KV-offload has a known crash.
- **Copilot inline completions and some IDE features remain cloud-side** even with BYOK; "local" applies to chat/agent requests only, and a remote BYOK URL still sends code off-machine.
- **Context-rot figures** (RULER: only half of "32K" models effectively handle 32K; NoLiMa: 11/13 models below 50% of short-context baseline by 32K; GPT-4o 99.3%→69.7%) are benchmark-derived generalizations; your effective context depends on the specific model and quant.
- **"Not possible" in the mapping table** means no first-class mechanism as of 2026 — you can often still approximate via an external proxy (LiteLLM/claude-code-router) or a wrapper script.