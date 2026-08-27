# Downloading & Managing Hugging Face Models on macOS for Local LLM Inference (2026)

## TL;DR
- **Use the new `hf` CLI, not `huggingface-cli`.** The CLI was renamed from `huggingface-cli` to `hf` (announced July 25, 2025; `hf` was introduced in `huggingface_hub` v0.34.0), and in `huggingface_hub` v1.0 (released October 27, 2025) the old `huggingface-cli` binary was **removed entirely**. `hf download <repo> --include "*Q4_K_M*.gguf" --local-dir ./models` is the canonical way to grab a single GGUF quant. Acceleration is now automatic via the built-in `hf_xet` backend — the old `HF_HUB_ENABLE_HF_TRANSFER=1` flag is deprecated and ignored.
- **Everything shares one cache: `~/.cache/huggingface/hub`.** llama.cpp's `-hf` flag, mlx-lm/oMLX, and the Python library all read and write the same Hugging Face cache, so a model pulled by one tool is reused by the others. The two exceptions are **Ollama** (its own `~/.ollama/models` store) and **LM Studio** (`~/.lmstudio/models`), which keep separate copies.
- **For your stack:** pull GGUF for llama.cpp and MLX weights from `mlx-community` with the same `hf download` command into the shared cache, pick **Q4_K_M** as your default quant (Q5_K_M/Q6_K on 32 GB+, Q8_0 on 64 GB+), and use `hf cache ls` / `hf cache rm` / `hf cache prune` to audit and reclaim disk space.

## Key Findings

1. **The CLI was renamed and consolidated.** `huggingface-cli` → `hf`, announced in the blog post "Say hello to `hf`" (July 25, 2025), adopting an `hf <resource> <action>` grammar; `hf` shipped in v0.34.0. v1.0 (Oct 27, 2025) removed the old binary — scripts must migrate. Install with `pip install -U huggingface_hub` (the `[cli]` extra was folded into core in v1.0) or via `uv`, `pipx`, or the standalone installer `curl -LsSf https://hf.co/cli/install.sh | bash`.
2. **`hf_transfer` is obsolete; `hf_xet` is the default.** Since v0.32.0 the Rust-based Xet backend installs automatically and uses adaptive concurrency with no configuration. `HF_HUB_ENABLE_HF_TRANSFER=1` is now a silent no-op.
3. **One shared cache with a blob/snapshot/refs symlink layout**, relocatable via `HF_HOME` or `HF_HUB_CACHE` — important on storage-constrained Macs.
4. **LM Studio is the best GUI** for browsing/downloading GGUF and MLX with RAM-fit hints, but it stores files in its own `~/.lmstudio/models` tree (not the HF cache), so naive use means duplicate downloads.
5. **Quantization guidance for Apple Silicon:** Q4_K_M is the sweet spot; the quality cliff is between Q3 and Q4, not Q4 and Q8.

## Details

### 1. Official Hugging Face CLI tooling (`hf`)

The Hugging Face CLI was officially renamed from `huggingface-cli` to `hf` in the blog post "Say hello to `hf`: a faster, friendlier Hugging Face CLI ✨" (published July 25, 2025), adopting an `hf <resource> <action>` grammar; the `hf` entry point was introduced in `huggingface_hub` v0.34.0. In `huggingface_hub` **v1.0** (published October 27, 2025), the deprecated `huggingface-cli` was **removed entirely** — in v1.x, `huggingface-cli download` no longer works. Any older tutorial that says `huggingface-cli download` should be read as `hf download`.

**Installation.** The CLI ships inside the `huggingface_hub` package:
- pip: `pip install -U huggingface_hub`
- uv: `uv pip install -U huggingface_hub` (or `uv tool install huggingface_hub`)
- pipx: `pipx install huggingface_hub`
- Standalone installer (v1.0+): `curl -LsSf https://hf.co/cli/install.sh | bash` on macOS/Linux — this creates an isolated sandboxed environment and handles PATH.

The `[cli]` extra was removed in v1.0 ("the CLI now ships with the core `huggingface_hub` package"). Verify with `hf version`.

**Authentication.** Run `hf auth login`. By default it logs you in via browser (prints a URL and short code). All auth subcommands are grouped under `hf auth` (`login`, `logout`, `whoami`, `switch`, `list`). You can also authenticate non-interactively with a token from huggingface.co/settings/tokens:
```bash
hf auth login --token $HF_TOKEN --add-to-git-credential
```
The official guidance is to pass the token via the `HF_TOKEN` environment variable rather than pasting it into your shell history. Gated/private repos and llama.cpp's `-hf` flag both read `HF_TOKEN` automatically. Note: if you are logged in via the `HF_TOKEN` env var, `hf auth logout` will not log you out — you must unset the variable.

**Download usage examples:**
```bash
# Whole repo into the shared HF cache
hf download mlx-community/Qwen3.6-35B-A3B-4bit

# A single named GGUF file to a local folder
hf download bartowski/Llama-3.2-3B-Instruct-GGUF Llama-3.2-3B-Instruct-Q4_K_M.gguf --local-dir ./models

# Only files matching a quantization pattern (multi-file GGUF repos)
hf download unsloth/gpt-oss-20b-GGUF --include "*Q4_K_M*.gguf" --local-dir ./models

# Filter with include/exclude (official example pattern)
hf download stabilityai/stable-diffusion-xl-base-1.0 --include "*.safetensors" --exclude "*.fp16.*"

# A specific revision/branch/tag
hf download bigcode/the-stack --repo-type dataset --revision v1.1
```
Key flags per the CLI reference: `--include`/`--exclude` (glob patterns), `--local-dir`, `--cache-dir`, `--revision`, `--repo-type`, `--max-workers`, `--force-download`, and `--dry-run`.

**Resuming interrupted downloads.** The old `--resume-download` flag/parameter was **removed** in v1.0 (the migration guide states verbatim: "resume_download, force_filename, and local_dir_use_symlinks parameters have been removed from hf_hub_download and snapshot_download"). Resume-on-rerun is now the implicit default behavior: re-running the same `hf download` continues from where it left off rather than restarting, and `--force-download` is the flag to force a full re-download. Note there is no single official sentence stating "auto-resume by default," and some users report inconsistent auto-resume on very large single-file GGUF downloads — if a partial download is corrupt, use `--force-download` or clear `.incomplete` files with `hf cache prune`.

### 2. The `huggingface_hub` Python library

For programmatic use:
```python
from huggingface_hub import hf_hub_download, snapshot_download

# One file (e.g., one GGUF quant)
path = hf_hub_download(
    repo_id="unsloth/gemma-4-12b-it-GGUF",
    filename="gemma-4-12b-it-Q4_K_M.gguf",
    local_dir="./models",
)

# Whole repo, or filtered subset
snapshot_download(
    repo_id="mlx-community/Qwen3.6-35B-A3B-4bit",
    local_dir="./models/qwen",
    allow_patterns=["*.safetensors", "*.json"],
    ignore_patterns=["*.bin"],
)
```
`snapshot_download` is the programmatic equivalent of `hf download`; its `allow_patterns`/`ignore_patterns` correspond to `--include`/`--exclude`. Both default to the shared cache unless `local_dir` or `cache_dir` is set. For cache inspection/cleanup programmatically, use `scan_cache_dir()`, which returns an `HFCacheInfo` object with a `.delete_revisions(...).execute()` helper.

### 3. Where models are stored on macOS

The default cache directory is **`~/.cache/huggingface/hub`**. Inside it, each repo is a folder named `models--<org>--<name>` (double hyphens separate hierarchy), containing:
- `blobs/` — the actual file contents, named by hash
- `snapshots/<commit-hash>/` — the human-readable file tree, with entries that are **symlinks** into `blobs/`
- `refs/` — maps branch/tag names to commit hashes

This design lets multiple revisions share unchanged files (deduplication) and lets multiple tools share one copy. **Changing the location** (crucial on Macs with small SSDs): set `HF_HUB_CACHE` (just the hub cache) or `HF_HOME` (all HF data — cache, tokens, etc.). For llama.cpp's `-hf`, `HF_HUB_CACHE` has priority, then `$HF_HOME/hub`, then XDG defaults. To move an existing cache: stop tools, copy the directory to the new volume (e.g., external SSD), set the env var, test, then delete the old copy.

### 4. Cache management and cleanup

**Important v1.0 change:** the old `hf cache scan` / `hf cache delete` (and `huggingface-cli scan-cache` / `delete-cache`) were **removed in v1.0** and replaced by a Docker-inspired trio (migration guide, verbatim: "The legacy hf cache scan and hf cache delete commands are also removed in v1.0 and are replaced with the new trio below"):
- **`hf cache ls`** — lists cache entries as table/JSON/CSV. Use `--revisions` to inspect individual revisions, `--filter` with expressions like `size>1GB` or `accessed>30d`, `--sort`, `--limit`, and `--quiet` (IDs only). Example: `hf cache ls --filter "size>30g" --revisions`.
- **`hf cache rm`** — deletes selected repos or revisions. Pass repo IDs (e.g., `model/bert-base-uncased`) or revision hashes; add `--dry-run` to preview or `--yes` to skip confirmation. Example: `hf cache rm $(hf cache ls --filter "accessed>1y" -q) -y`.
- **`hf cache prune`** — deletes all detached (unreferenced) revisions plus leftover `.incomplete` files in one shot; supports `--dry-run`/`--yes`.

There is also `hf cache verify` to validate local files against Hub checksums. (If you're still on a 0.x install, the older `hf cache scan`/`delete` and `huggingface-cli scan-cache`/`delete-cache` still exist.) **GUI cache tools:** there is no official HF desktop app; LM Studio, Jan, and Msty each provide their own in-app model deletion for their own stores, and general Mac cleanup utilities exist, but the CLI is the reliable path for the HF cache.

### 5. GUI/desktop apps for macOS

**LM Studio** is the standout native macOS app (Apple Silicon, built on llama.cpp with an MLX runtime). Its **Discover** tab is a built-in Hugging Face browser filtered to compatible GGUF (and MLX) files, showing quantization options, file sizes, and RAM-fit hints; you can even paste full HF URLs (⌘+Shift+M opens model search). It ships two engines — llama.cpp for GGUF and Apple MLX for MLX-format models — and highlights the recommended quant for your hardware.

**Storage & reuse:** LM Studio stores models under **`~/.lmstudio/models/<publisher>/<model>/<file>.gguf`** as **plain, named files** (preserving the HF publisher/model directory structure), which you can relocate via **My Models → change directory** or the `lms` CLI. Crucially, LM Studio does **not** use the shared `~/.cache/huggingface/hub` cache — so a model downloaded in LM Studio is a separate copy from one pulled by `hf download` or llama.cpp's `-hf`. The good news: because LM Studio stores plain GGUF files, **llama.cpp/llama-server can load them directly** by pointing `-m` at the file path (e.g., `~/.lmstudio/models/lmstudio-community/…/model-Q4_K_M.gguf`), and Jan can point at LM Studio's model directory to avoid re-downloading. You can also import external GGUFs into LM Studio with `lms import <path>`. The reverse (LM Studio reading llama.cpp's HF-cache symlink tree) is not automatic because LM Studio expects its own folder layout.

**Other apps:** **Jan** (Apache-2.0, open source) bundles llama.cpp with an experimental MLX path, has its own HF-connected model hub with fit pills (Fits / May be slow / Won't fit) and Recommended-quant tags, and stores GGUF files that are cross-compatible with LM Studio. **Msty** (closed-source, free desktop tier; Aurum paid tier reported at $149/user/yr or $349 lifetime as of June 2026) is a "manager" that supervises a bundled Ollama by default plus optional llama.cpp (since Nov 2025) and MLX (since Mar 2026) services, with Split Chats for side-by-side model comparison. **GPT4All** is the most simplified. None of these use the HF shared cache by default — each keeps its own store.

### 6. Fast/parallel download acceleration

The Rust-based **`hf_transfer`** library (enabled with `HF_HUB_ENABLE_HF_TRANSFER=1`) is **no longer recommended or needed in 2026**. Since `huggingface_hub` **v0.32.0**, the **`hf_xet`** package (bindings to `xet-core`) installs automatically and is the default transfer backend, using chunk-based deduplication and adaptive concurrency ("Adaptive concurrency is on by default… no configuration required. The default settings will saturate most network paths without any tuning"). In v1.0, support for `hf_transfer` was fully removed and `HF_HUB_ENABLE_HF_TRANSFER` is ignored; the replacement tuning knob is `HF_XET_HIGH_PERFORMANCE=1`. Known issues: setting the legacy flag is a silent no-op (no warning), which confuses users following old tutorials; advanced users can pin concurrency with `HF_XET_FIXED_DOWNLOAD_CONCURRENCY`. To explicitly disable Xet, set `HF_HUB_DISABLE_XET=1`. On SSD/NVMe (all modern Macs) the default parallel writes are optimal.

### 7. Best practices for your LiteLLM/Ollama/llama.cpp/MLX stack

**(a) llama.cpp / llama-server `-hf`.** `llama-cli` and `llama-server` can download-and-run directly from the Hub:
```bash
llama-server -hf bartowski/Llama-3.2-3B-Instruct-GGUF:Q8_0
llama-cli --hf-repo unsloth/phi-4-GGUF:q4_k_m -p "…"
```
The `:QUANT` selector picks a matching file. **Current llama.cpp releases cache `-hf` downloads in the standard Hugging Face Hub cache** (`hf-cache.cpp` mirrors the official layout), so a model you already pulled with `hf download` is reused, and vice-versa. The cache path resolves via `HF_HUB_CACHE` → `$HF_HOME/hub` → XDG defaults. (The older `LLAMA_CACHE` variable is legacy and should not be treated as the primary path for the standard `-hf` cache in current builds.)

**(b) Ollama is different.** Ollama uses its **own** model store at **`~/.ollama/models`** (blobs named by SHA256 + JSON manifests) and its own registry (`ollama pull <name>`). It does **not** read or write the HF cache, so models pulled with Ollama are entirely separate copies. It can import external GGUFs via a `Modelfile` (`FROM ./model.gguf`), but there's no automatic sharing with the HF cache. In a LiteLLM proxy, treat Ollama as a self-contained backend and expect its disk usage to be independent of your HF cache. (Caveat: Ollama sometimes bundles GGUFs for brand-new architectures before llama.cpp master supports them, so an Ollama blob may not load in a slightly older llama.cpp — in that case download the GGUF from HF directly.)

**(c) Avoiding duplicate downloads.** The single biggest win is to **route llama.cpp/llama-server, mlx-lm/oMLX, and `hf download` all through the same shared HF cache** (they already do by default) and set one `HF_HOME`/`HF_HUB_CACHE` on your largest volume. Concretely:
- Use `hf download` (or `-hf`) as the single source of truth; both write to `~/.cache/huggingface/hub`.
- For MLX servers (oMLX, mlx-omni-server, `mlx_lm.server`): `mlx-lm` and `vllm-mlx` share the same HF cache, so a model pulled by one is not re-downloaded by the other. A space-saving pattern is to `hf download mlx-community/<model>` once, then have oMLX symlink into the HF cache rather than keeping a second copy.
- For LM Studio/Jan/Ollama, accept that these keep separate stores — but you can point llama.cpp/MLX at LM Studio's or Jan's plain GGUF files by path to avoid a second download, and import existing GGUFs into LM Studio via `lms import`.
- Don't `git clone` GGUF repos (pulls every quant); use `--include` or a single filename.

**(d) Recommended quantization by RAM (Apple Silicon unified memory).** Weight memory ≈ params × bytes/weight (Q4_K_M ≈ 0.55 bytes/weight → an 8B model ≈ 4.9 GB; Q8_0 ≈ 1.06; FP16 = 2.0), plus KV cache and 1–2 GB overhead. Quality retention vs FP16 (community WikiText-2 perplexity, ~17B model): Q3_K_M ~93%, Q4_K_M ~98%, Q5_K_M ~99%, Q6_K ~99.5%, Q8_0 ~99.8% — the meaningful cliff is between Q3 and Q4.
- **8 GB:** Q3_K_M or a small 3–4B model at Q4_K_M only.
- **16 GB:** Q4_K_M for 7B–13B.
- **32 GB:** Q5_K_M for 7B–13B; Q4_K_M for ~34B.
- **64 GB+:** Q6_K broadly, or Q8_0 on 7B–14B for near-lossless quality; ~70B at Q4_K_M (~40 GB).

Aim for 15+ tokens/sec for a comfortable chat; if below ~10 tok/s, prefer a smaller model at the same quant rather than a lower quant. On MLX, 4-bit and 8-bit builds from `mlx-community` are the common analogues; MLX often runs faster than GGUF on the same Mac because weights load directly into unified memory.

### 8. Filtering only certain quant variants from multi-file GGUF repos

Quantized GGUF repos often contain a dozen+ files; you almost never want the whole repo. Options, best to worst for your workflow:
1. **`hf download <repo> --include "*Q4_K_M*.gguf" --local-dir ./models`** — the cleanest CLI method; `--include` takes glob patterns.
2. **Name the exact file:** `hf download <repo> <exact-filename>.gguf --local-dir ./models`.
3. **`hf_hub_download(repo_id=…, filename="…-Q4_K_M.gguf")`** in Python for a single file.
4. **llama.cpp `-hf <repo>:Q4_K_M`** to download+run in one step (into the shared cache).
5. **git-lfs selective pull** (bandwidth-saving for edge cases): `GIT_LFS_SKIP_SMUDGE=1 git clone <url>` then `git lfs pull --include="*Q4_K_M.gguf"`.

Watch for **split/sharded GGUFs** (`model-00001-of-00003.gguf` …): you must fetch **all** shards — a pattern like `--include "*Q4_K_M*"` will catch them, but a single-filename download will not. llama.cpp's `-hf` and downloader auto-detect and fetch all parts.

## Recommendations

**Stage 1 — Set up once.**
1. `pip install -U huggingface_hub` (or `uv tool install huggingface_hub`); confirm `hf version` shows v1.x.
2. `hf auth login` (or export `HF_TOKEN`) so gated models and `-hf` downloads authenticate.
3. Decide your cache volume. If your internal SSD is tight, put the cache on a fast external: add `export HF_HOME=/Volumes/FastSSD/hf` (and optionally `HF_HUB_CACHE`) to your shell profile **before** downloading anything. This one variable makes llama.cpp `-hf`, mlx-lm/oMLX, and `hf download` all share the same location.

**Stage 2 — Download models the deduplicated way.**
4. Pull GGUF for llama.cpp with `hf download <repo> --include "*Q4_K_M*.gguf" --local-dir …` (or let `llama-server -hf <repo>:Q4_K_M` do it into the shared cache).
5. Pull MLX weights with `hf download mlx-community/<model>` (4-bit default; 8-bit if you have RAM headroom). Point oMLX/mlx-omni-server at the same HF cache; symlink rather than copy if a server wants a local path.
6. Start at **Q4_K_M**; move to **Q5_K_M/Q6_K** on 32 GB and **Q8_0** on 64 GB+ when quality matters. Prefer MLX builds on Apple Silicon for speed where the architecture is supported.

**Stage 3 — Keep it lean.**
7. Audit monthly: `hf cache ls --filter "size>5g" --revisions`.
8. Reclaim space: `hf cache prune --dry-run` then `hf cache prune`; remove specific stale repos with `hf cache rm <repo> --dry-run` then `--yes`.
9. For Ollama and LM Studio, remember they don't use the HF cache — clean them via `ollama rm <model>` and LM Studio's My Models, and check `du -sh ~/.ollama/models ~/.lmstudio/models ~/.cache/huggingface`.

**Benchmarks/thresholds that change the plan.**
- If free disk on your cache volume drops below ~20%, run `hf cache prune` and consolidate duplicated LM Studio/Ollama copies.
- If a download stalls or corrupts repeatedly, add `--force-download` once, and check whether a proxy/HDD is defeating Xet parallel writes (set `HF_XET_HIGH_PERFORMANCE=1` on very fast links, or `HF_HUB_DISABLE_XET=1` to diagnose).
- If inference is below ~10 tok/s, step down a model size at the same quant rather than dropping to a lower quant.

## Caveats
- **Version-dependent behavior:** the cache command names (`hf cache ls/rm/prune`) and the removal of `huggingface-cli`, `hf_transfer`, and `--resume-download` all apply to `huggingface_hub` **v1.x**. If you (or a dependency) pin an older 0.x release, the legacy `hf cache scan`/`delete`, `huggingface-cli`, and `HF_HUB_ENABLE_HF_TRANSFER` still function.
- **Auto-resume is implied, not explicitly documented:** official docs confirm `--resume-download` was removed and only `--force-download` remains, which implies resume-on-rerun, but there is no single official sentence guaranteeing it, and some users report imperfect resume on very large single GGUF files.
- **The `*Q4_K_M*.gguf` include example** is a valid extrapolation of the documented `--include` glob syntax; HF's own docs illustrate filtering with `--include "*.safetensors" --exclude "*.fp16.*"`.
- **Third-party figures** (quantization byte/weight ratios, perplexity-retention percentages, tokens/sec) come from community benchmarks and vary by model family, context length, and Mac generation — treat them as guidance, not guarantees.
- **App pricing/features** (Msty Aurum tiers, Jan/GPT4All specifics) are as reported in 2026 sources and may change; verify current LM Studio/Jan/Msty docs before standardizing a team workflow.
- Fast-moving area: llama.cpp ships continuous builds and Ollama sometimes leads llama.cpp on new architectures, so exact cache behavior and format support can shift between releases.