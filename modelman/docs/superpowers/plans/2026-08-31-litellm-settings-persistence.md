# LiteLLM Settings Persistence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** modelman's writes to LiteLLM's `config.yaml` preserve everything it doesn't own (byte-identically, comments included) and *ensure* the two launcher-required settings on every write, saving/restarting only when something actually changed.

**Architecture:** Keep the three existing config writers (`expose_model`, `unexpose_model`, `apply_expose_queue`) in `src/modelman/litellm.py` as read-modify-write paths, but swap the YAML serialization from PyYAML to ruamel's round-trip mode (comments/layout/quote-style survive), add a pure `ensure_litellm_settings(config) -> bool` function that every writer runs before saving, and gate each save + proxy-restart on a deep-equality changed check of the parsed document.

**Tech Stack:** Python 3.13, ruamel.yaml (new pinned dependency), PyYAML (stays — other modules still use it), pytest, uv.

**Spec:** `docs/superpowers/specs/2026-08-31-litellm-settings-persistence-design.md` — read it first; Decisions 3 (owned settings rules), 4 (wiring + changed-detection), and 5 (ruamel round-trip) drive this plan.

**Verified ruamel properties (all confirmed working on this machine, Python 3.13):** a `YAML(typ="rt")` instance with `preserve_quotes = True` and `width = 4096` loads the live `~/.config/litellm/config.yaml` and dumps it back **byte-identically**; `CommentedMap`/`CommentedSeq` are `dict`/`list` subclasses (so `isinstance` checks and `==` against plain dicts work, and `copy.deepcopy` + `==` is a valid changed-detection); parse errors raise `ruamel.yaml.error.YAMLError`.

**Conventions:**
- Run tests with `uv run pytest <path> -v`; run everything with `make all` (format + test + check).
- Ruff enforces `line-length = 100`; mypy runs with `ignore_missing_imports = true` (no type-stub work needed for ruamel).
- Commit after every task with the exact message given in the task's final step.

---

### Task 1: ruamel.yaml round-trip load/save

Swap `load_litellm_config` / `save_litellm_config` in `src/modelman/litellm.py` from PyYAML to ruamel round-trip so comments, layout, and quote style survive the load→mutate→save cycle. No behavior change otherwise — every existing test must still pass.

**Files:**
- Modify: `pyproject.toml` (dependencies)
- Modify: `src/modelman/litellm.py:19` (import), `:123-138` (`load_litellm_config`), `:215-243` (`save_litellm_config`)
- Test: `tests/test_litellm.py`

- [ ] **Step 1: Add the dependency**

```bash
uv add "ruamel.yaml>=0.18"
```

Expected: `pyproject.toml` `[project] dependencies` gains `"ruamel.yaml>=0.18",` and `uv.lock` is updated. PyYAML **stays** — `config.py`, `manifest.py`, `migrate.py`, `settings.py` still import it.

- [ ] **Step 2: Write the failing tests**

Append to `tests/test_litellm.py` (the needed imports `load_litellm_config`, `save_litellm_config`, `set_exposed` are already at the top of the file):

```python
def test_roundtrip_preserves_comments_byte_identical(tmp_path):
    # ruamel round-trip must reproduce hand-written "why" comments and
    # layout byte-for-byte when modelman saves without touching them —
    # the settings-persistence spec's core guarantee (Decision 5).
    original = (
        "# Tolerate params the bridged provider does not support.\n"
        "model_list:\n"
        "- model_name: ollama/a\n"
        "  litellm_params:\n"
        "    model: ollama_chat/a\n"
        "litellm_settings:\n"
        "  drop_params: true\n"
    )
    path = tmp_path / "config.yaml"
    path.write_text(original)
    config = load_litellm_config(path)
    save_litellm_config(config, path)
    assert path.read_text() == original


def test_save_preserves_comments_when_other_rows_change(tmp_path):
    # Editing one row (set_exposed replaces it with a plain dict) must
    # not disturb comments or sections modelman didn't touch.
    original = (
        "# top-level why comment\n"
        "model_list:\n"
        "- model_name: ollama/a\n"
        "  litellm_params:\n"
        "    model: ollama_chat/a\n"
        "general_settings:\n"
        "  database_url: postgresql://x\n"
    )
    path = tmp_path / "config.yaml"
    path.write_text(original)
    config = load_litellm_config(path)
    set_exposed(
        config,
        "ollama/b",
        {"model_name": "ollama/b", "litellm_params": {"model": "ollama_chat/b"}},
    )
    save_litellm_config(config, path)
    text = path.read_text()
    assert "# top-level why comment" in text
    assert "database_url: postgresql://x" in text
    loaded = load_litellm_config(path)
    assert [r["model_name"] for r in loaded["model_list"]] == ["ollama/a", "ollama/b"]
```

- [ ] **Step 3: Run the new tests to verify they fail**

```bash
uv run pytest tests/test_litellm.py -k "roundtrip or preserves_comments" -v
```

Expected: both FAIL — PyYAML's `safe_dump` drops the comment, so `path.read_text() == original` is False.

- [ ] **Step 4: Swap the implementation to ruamel**

In `src/modelman/litellm.py`, replace the `import yaml` line (line 19) with:

```python
from ruamel.yaml import YAML
from ruamel.yaml.error import YAMLError
```

Add this helper function immediately **after** the `LiteLLMConfigError` class definition (after line 80):

```python
def _rt_yaml() -> YAML:
    """A round-trip YAML instance: preserves comments, layout, and quote
    style across the load→mutate→save cycle (settings-persistence spec,
    Decision 5). The wide `width` keeps long values (api keys) on one
    line instead of folding them across lines the way PyYAML would."""
    yaml = YAML(typ="rt")
    yaml.preserve_quotes = True
    yaml.width = 4096
    return yaml
```

Replace the body of `load_litellm_config` with (keep the signature and docstring's `Errors if missing, malformed, or not a mapping` summary):

```python
def load_litellm_config(path: Path) -> dict[str, Any]:
    """Read LiteLLM's config.yaml. Errors if missing, malformed, or not a mapping.

    Round-trip mode so comments and formatting survive modelman's own
    writes; a CommentedMap is a dict subclass, so callers see the
    document they've always seen.
    """
    if not path.exists():
        raise LiteLLMConfigError(f"LiteLLM config not found: {path}")
    with open(path) as f:
        try:
            data = _rt_yaml().load(f)
        except YAMLError as exc:
            # Hand-edited configs can be syntactically invalid; surface
            # that as a LiteLLMConfigError so the CLI prints "error: ..."
            # instead of a raw yaml traceback.
            raise LiteLLMConfigError(f"LiteLLM config is not valid YAML: {path}\n{exc}") from None
    if not isinstance(data, dict):
        raise LiteLLMConfigError(f"LiteLLM config is not a mapping: {path}")
    return data
```

Replace the body of `save_litellm_config` with (keep the signature; the docstring below replaces the old NOTE about PyYAML comment loss):

```python
def save_litellm_config(config: dict[str, Any], path: Path) -> None:
    """Write config.yaml atomically (temp file + rename).

    Round-trip mode preserves the comments and layout of content
    modelman did not touch; only targeted mutations re-serialize.

    The replacement file inherits the existing file's permission bits:
    mkstemp creates 0600 and os.replace preserves that mode, so without
    the chmod a config readable by a LiteLLM service running as another
    user would silently tighten to 0600 on the first write.
    """
    path.parent.mkdir(parents=True, exist_ok=True)
    try:
        existing_mode = path.stat().st_mode & 0o777
    except OSError:
        existing_mode = None
    fd, tmp_name = tempfile.mkstemp(dir=path.parent, prefix=f".{path.name}.", suffix=".tmp")
    try:
        if existing_mode is not None:
            os.fchmod(fd, existing_mode)
        with os.fdopen(fd, "w") as f:
            _rt_yaml().dump(config, f)
        os.replace(tmp_name, path)
    except BaseException:
        with contextlib.suppress(OSError):
            os.unlink(tmp_name)
        raise
```

- [ ] **Step 5: Run the new tests to verify they pass**

```bash
uv run pytest tests/test_litellm.py -k "roundtrip or preserves_comments" -v
```

Expected: both PASS.

- [ ] **Step 6: Run the focused tests and checks**

```bash
uv run pytest tests/test_litellm.py tests/commands/test_expose.py tests/usage/test_db.py tests/usage/test_cli.py -q && make check
```

Expected: all pass, ruff + mypy clean. (These are the files that exercise `load_litellm_config`/`save_litellm_config`. Existing tests are unaffected: ruamel parses PyYAML-style YAML; `CommentedMap == plain dict` equality holds, so `loaded == config` assertions still pass; the invalid-YAML test still gets `LiteLLMConfigError` because ruamel parse errors raise `ruamel.yaml.error.YAMLError`.) The full suite runs once at the end (Task 5's `make all`).

- [ ] **Step 7: Commit**

```bash
git add pyproject.toml uv.lock src/modelman/litellm.py tests/test_litellm.py
git commit -m "feat(litellm): ruamel round-trip config writes preserve comments"
```

---

### Task 2: `ensure_litellm_settings()` — the owned-settings pass

A pure function over the parsed config document. Two curated rules from the spec's Decision 3: `drop_params` is **value-enforced** (missing or not `true` → set to `true`); `additional_drop_params: ["reasoning_effort"]` is **presence-based** (added to every `ollama_chat/*` row missing it; an existing key of any value is untouched).

**Files:**
- Modify: `src/modelman/litellm.py` (new function, placed after `remove_exposed` around line 213)
- Test: `tests/test_litellm.py`

- [ ] **Step 1: Write the failing tests**

Append to `tests/test_litellm.py`, and add `ensure_litellm_settings` to the import list at the top of the file:

```python
def test_ensure_adds_drop_params_when_section_missing():
    config = {"model_list": []}
    assert ensure_litellm_settings(config) is True
    assert config["litellm_settings"] == {"drop_params": True}


def test_ensure_corrects_drop_params_false():
    # Value-enforced: any non-true value is a launcher break waiting to
    # happen (copilot 400s), so modelman corrects it, not just missing keys.
    config = {"litellm_settings": {"drop_params": False}}
    assert ensure_litellm_settings(config) is True
    assert config["litellm_settings"]["drop_params"] is True


def test_ensure_leaves_correct_drop_params_untouched():
    config = {"litellm_settings": {"drop_params": True}}
    assert ensure_litellm_settings(config) is False


def test_ensure_preserves_other_litellm_settings():
    config = {"litellm_settings": {"drop_params": False, "num_workers": 4}}
    assert ensure_litellm_settings(config) is True
    assert config["litellm_settings"] == {"drop_params": True, "num_workers": 4}


def test_ensure_adds_bridge_drop_params_to_ollama_chat_rows():
    config = {
        "model_list": [
            {"model_name": "a", "litellm_params": {"model": "ollama_chat/a"}},
        ]
    }
    assert ensure_litellm_settings(config) is True
    params = config["model_list"][0]["litellm_params"]
    assert params["additional_drop_params"] == ["reasoning_effort"]


def test_ensure_leaves_existing_bridge_drop_params_untouched():
    # Presence-based: an existing key — an extended list or a deliberate
    # empty list — marks the row user-managed; only missing keys are added.
    config = {
        "model_list": [
            {
                "model_name": "a",
                "litellm_params": {
                    "model": "ollama_chat/a",
                    "additional_drop_params": ["reasoning_effort", "other"],
                },
            },
            {
                "model_name": "b",
                "litellm_params": {
                    "model": "ollama_chat/b",
                    "additional_drop_params": [],
                },
            },
        ]
    }
    assert ensure_litellm_settings(config) is False


def test_ensure_never_touches_non_bridge_rows():
    config = {
        "model_list": [
            {"model_name": "x", "litellm_params": {"model": "openai/x"}},
            {"model_name": "y", "litellm_params": {"model": "openrouter/y"}},
        ],
        "litellm_settings": {"drop_params": True},
    }
    assert ensure_litellm_settings(config) is False


def test_ensure_covers_rows_modelman_did_not_write():
    # The ensure runs over the whole parsed model_list, not just newly
    # created rows — hand-added ollama_chat rows are repaired too.
    config = {
        "litellm_settings": {"drop_params": True},
        "model_list": [
            {"model_name": "hand/row", "litellm_params": {"model": "ollama_chat/hand"}},
        ],
    }
    assert ensure_litellm_settings(config) is True


def test_ensure_skips_degenerate_shapes():
    # Hand-edited scalars aren't modelman's to fix; skip rather than
    # crash — the module's established tolerance pattern.
    config = {"litellm_settings": "broken", "model_list": ["just-a-string"]}
    assert ensure_litellm_settings(config) is False


def test_ensure_is_idempotent():
    # A second run over a healed document changes nothing (spec
    # Decision 3: no save, no proxy bounce).
    config = {
        "model_list": [
            {"model_name": "a", "litellm_params": {"model": "ollama_chat/a"}},
        ]
    }
    assert ensure_litellm_settings(config) is True
    assert ensure_litellm_settings(config) is False
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
uv run pytest tests/test_litellm.py -k ensure -v
```

Expected: all FAIL with `ImportError: cannot import name 'ensure_litellm_settings'`.

- [ ] **Step 3: Implement `ensure_litellm_settings`**

In `src/modelman/litellm.py`, add these module constants right after the `PROVIDER_POLICIES` dict (after line 61):

```python
# Launcher-required settings modelman owns and ensures on every config
# write (settings-persistence spec, 2026-08-31).
_BRIDGE_PREFIX = "ollama_chat/"  # LiteLLM's chat-completions→ollama bridge
_BRIDGE_DROP_PARAMS = ("reasoning_effort",)  # BerriAI/litellm#37452 workaround
```

Add this function after `remove_exposed` (around line 213):

```python
def ensure_litellm_settings(config: dict[str, Any]) -> bool:
    """Ensure launcher-required LiteLLM settings are present.

    Returns True iff the document changed. Two curated rules
    (settings-persistence spec, Decision 3):

    - `litellm_settings.drop_params` is **value-enforced** — set to
      `True` when missing or not `true`. copilot sends
      `parallel_tool_calls`, which the ollama backend rejects with
      `400 UnsupportedParamsError` unless LiteLLM drops it.
    - `additional_drop_params: ["reasoning_effort"]` is
      **presence-based** — added to every model_list row whose
      `litellm_params.model` starts with `ollama_chat/` and lacks the
      key. codex's `/v1/responses` `reasoning` object crashes LiteLLM
      1.98.x's bridge (BerriAI/litellm#37452); an existing key of any
      value (an extended list, a deliberate `[]`) marks the row
      user-managed and is left untouched.

    Tolerates hand-edited degenerate shapes the way the rest of the
    module does: a scalar `litellm_settings` or non-dict `model_list`
    rows are skipped, never crashed on.
    """
    changed = False
    settings = config.get("litellm_settings")
    if settings is None:
        settings = config["litellm_settings"] = {}
    if isinstance(settings, dict):
        if settings.get("drop_params") is not True:
            settings["drop_params"] = True
            changed = True
    rows = config.get("model_list")
    if isinstance(rows, list):
        for row in rows:
            if not isinstance(row, dict):
                continue
            params = row.get("litellm_params")
            if not isinstance(params, dict):
                continue
            model = params.get("model")
            if (
                isinstance(model, str)
                and model.startswith(_BRIDGE_PREFIX)
                and "additional_drop_params" not in params
            ):
                params["additional_drop_params"] = list(_BRIDGE_DROP_PARAMS)
                changed = True
    return changed
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
uv run pytest tests/test_litellm.py -k ensure -v
```

Expected: all 10 PASS.

- [ ] **Step 5: Run the focused tests and checks**

```bash
uv run pytest tests/test_litellm.py -q && make check
```

Expected: everything green (`ensure_litellm_settings` is not wired into any writer yet — no behavior change). The full suite runs once at the end (Task 5's `make all`).

- [ ] **Step 6: Commit**

```bash
git add src/modelman/litellm.py tests/test_litellm.py
git commit -m "feat(litellm): ensure launcher-required settings on config writes"
```

---

### Task 3: Wire ensure + changed-detection into `expose_model` / `unexpose_model`

Spec Decision 4: every writer snapshots the parsed document, mutates, runs the ensure, then saves **iff** the document changed and restarts the proxy **iff** the document *or* a state flag changed. This also fixes a latent gap: a re-expose that rewrites an entry with new content (e.g. a changed `api_base`) but leaves the flag set currently skips the restart, leaving the proxy stale.

**Files:**
- Modify: `src/modelman/litellm.py:11-19` (add `import copy`), `:286-306` (`expose_model`), `:308-328` (`unexpose_model`)
- Test: `tests/test_expose.py`

- [ ] **Step 1: Update the stale test and write the new tests**

In `tests/test_expose.py`, the existing `test_unexpose_model_noop_does_not_restart_proxy` pins pre-spec behavior that this task deliberately changes: its config row **exists** (only the state entry is absent), so removing the route is a real config change and the proxy **must** now restart. Replace that entire test (keep its old docstring's cascade context in the replacement's first test) with these two, and append the three new tests after `test_unexpose_model_restarts_proxy`:

```python
def test_unexpose_stale_row_restarts_proxy(tmp_path, monkeypatch):
    # The row exists in config but state says not exposed (flag reset by
    # hand, or the model was deleted): removing the route IS a real
    # config change, so the proxy restarts even though no flag flips.
    # Pre-spec behavior skipped the restart because only flag changes
    # triggered it — the settings-persistence spec's changed-detection
    # fixes that.
    state = StateStore()
    path = tmp_path / "config.yaml"
    save_litellm_config(
        {"model_list": [{"model_name": "ollama/a", "litellm_params": {"model": "x"}}]},
        path,
    )
    calls = []
    monkeypatch.setattr(
        "modelman.litellm.restart_litellm_proxy", lambda: calls.append("restart") or []
    )
    unexpose_model(state, "ollama/a", path)
    assert calls == ["restart"]


def test_unexpose_true_noop_skips_save_and_restart(tmp_path, monkeypatch):
    # Row absent, flag already false, settings already ensured: the file
    # is not rewritten (mtime and bytes unchanged) and the proxy is not
    # bounced. A needless `launchctl kickstart -k` would disrupt
    # in-flight proxy requests for zero config change.
    state = _state()  # ollama/a with litellm_exposed defaulting to False
    path = tmp_path / "config.yaml"
    save_litellm_config(
        {
            "model_list": [],
            "general_settings": {},
            "litellm_settings": {"drop_params": True},
        },
        path,
    )
    before = path.read_bytes()
    mtime = path.stat().st_mtime_ns
    calls = []
    monkeypatch.setattr(
        "modelman.litellm.restart_litellm_proxy", lambda: calls.append("restart") or []
    )
    unexpose_model(state, "ollama/a", path)
    assert calls == []
    assert path.read_bytes() == before
    assert path.stat().st_mtime_ns == mtime


def test_unexpose_ensures_launcher_required_settings(tmp_path):
    # The unexpose path runs the same ensure: every write opportunity is
    # a settings-repair opportunity, even when the operation is a removal.
    state = StateStore()
    path = tmp_path / "config.yaml"
    save_litellm_config(
        {"model_list": [{"model_name": "ollama/a", "litellm_params": {"model": "x"}}]},
        path,
    )
    unexpose_model(state, "ollama/a", path)
    config = load_litellm_config(path)
    assert config["litellm_settings"]["drop_params"] is True


def test_expose_ensures_launcher_required_settings(tmp_path):
    # The ensure runs over the whole parsed model_list: pre-existing
    # hand rows are repaired on the same write, not just the new entry,
    # and the fresh ollama entry gains the bridge workaround too.
    registry = _registry()
    state = _state()
    path = tmp_path / "config.yaml"
    save_litellm_config(
        {
            "model_list": [
                {
                    "model_name": "hand/row",
                    "litellm_params": {"model": "ollama_chat/hand"},
                }
            ],
            "general_settings": {"database_url": "x"},
        },
        path,
    )
    expose_model(registry, state, "ollama/a", path)
    config = load_litellm_config(path)
    assert config["litellm_settings"]["drop_params"] is True
    hand = next(r for r in config["model_list"] if r["model_name"] == "hand/row")
    assert hand["litellm_params"]["additional_drop_params"] == ["reasoning_effort"]
    fresh = next(r for r in config["model_list"] if r["model_name"] == "ollama/a")
    assert fresh["litellm_params"]["additional_drop_params"] == ["reasoning_effort"]


def test_reexpose_identical_entry_skips_save_and_restart(tmp_path, monkeypatch):
    # Idempotent re-expose: the rebuilt entry is identical to the saved
    # row, the flag is already set, and the settings are already
    # ensured — so nothing is saved and the proxy is not bounced.
    registry = _registry()
    state = _state()
    path = _seed_config(tmp_path)
    calls = []
    monkeypatch.setattr(
        "modelman.litellm.restart_litellm_proxy", lambda: calls.append("restart") or []
    )
    expose_model(registry, state, "ollama/a", path)
    assert calls == ["restart"]
    before = path.read_bytes()
    mtime = path.stat().st_mtime_ns
    calls.clear()
    expose_model(registry, state, "ollama/a", path)
    assert calls == []
    assert path.read_bytes() == before
    assert path.stat().st_mtime_ns == mtime


def test_reexpose_with_changed_content_restarts_proxy(tmp_path, monkeypatch):
    # Latent-gap fix: a re-expose that rewrites the row with NEW content
    # (here: the provider's base_url changed) must bounce the proxy even
    # though the flag never flips — the running proxy serves the old row.
    registry = _registry()
    state = _state()
    path = _seed_config(tmp_path)
    expose_model(registry, state, "ollama/a", path)
    registry.providers[0].auth.base_url = "http://localhost:11435"
    calls = []
    monkeypatch.setattr(
        "modelman.litellm.restart_litellm_proxy", lambda: calls.append("restart") or []
    )
    expose_model(registry, state, "ollama/a", path)
    assert calls == ["restart"]
```

Also add `load_litellm_config` to the top-level import list at the head of `tests/test_expose.py` (the existing tests import it locally inside functions; the new tests use it directly, so add it to the `from modelman.litellm import (...)` block).

- [ ] **Step 2: Run the tests to verify they fail**

```bash
uv run pytest tests/test_expose.py -v
```

Expected: `test_expose_ensures_launcher_required_settings`, `test_unexpose_ensures_launcher_required_settings`, `test_reexpose_identical_entry_skips_save_and_restart`, `test_reexpose_with_changed_content_restarts_proxy`, `test_unexpose_true_noop_skips_save_and_restart` FAIL (ensure not wired, unconditional save, flag-only restart). `test_unexpose_stale_row_restarts_proxy` FAILS (currently no restart when no flag flips). Every other pre-existing test in the file still passes.

- [ ] **Step 3: Implement the wiring**

In `src/modelman/litellm.py`, add `import copy` to the stdlib import block (after `import contextlib`, alphabetical). Replace the bodies of `expose_model` and `unexpose_model` (keep signatures):

```python
def expose_model(
    registry: Registry,
    state: StateStore,
    model_id: str,
    litellm_path: Path,
) -> list[str]:
    """Expose a model through LiteLLM: write its model_list entry and flip
    the modelman.toml flag. Errors if the model is unknown, its provider
    has no LiteLLM mapping, or it isn't ready (unless cloud).

    Read-modify-write with settings-ensure: the config is saved only when
    the operation or the ensure actually changed it, and the proxy is
    restarted only when the config or the flag changed — an idempotent
    re-expose of a byte-identical entry writes nothing and does not
    bounce the proxy.

    Returns non-fatal proxy-restart warnings (command unset or failed) so
    the CLI can surface them; the TUI path uses apply_expose_queue instead.
    """
    entry = _validated_entry(registry, state, model_id)
    config = load_litellm_config(litellm_path)
    before = copy.deepcopy(config)
    set_exposed(config, model_id, entry)
    ensure_litellm_settings(config)
    changed = config != before
    if changed:
        save_litellm_config(config, litellm_path)
    if changed or _set_exposed_flag(state, model_id, True):
        return restart_litellm_proxy()
    return []
```

```python
def unexpose_model(
    state: StateStore,
    model_id: str,
    litellm_path: Path,
) -> list[str]:
    """Remove a model's model_list entry and clear its modelman.toml flag.

    A true no-op — row absent, flag already false, settings already
    ensured — saves nothing and does not bounce the proxy. A stale row
    (in config but not exposed per state) is still a real change and
    does restart. A model missing from state (e.g. deleted earlier in
    the same apply cycle) unexposes cleanly without materializing a
    state row for the corpse.

    Returns non-fatal proxy-restart warnings (command unset or failed) so
    the CLI can surface them; the TUI path uses apply_expose_queue instead.
    """
    config = load_litellm_config(litellm_path)
    before = copy.deepcopy(config)
    remove_exposed(config, model_id)
    ensure_litellm_settings(config)
    changed = config != before
    if changed:
        save_litellm_config(config, litellm_path)
    if changed or _set_exposed_flag(state, model_id, False):
        return restart_litellm_proxy()
    return []
```

Note the ordering: the save (if any) happens **before** `_set_exposed_flag` runs, preserving the existing invariant that a failed save never leaves state claiming an exposure the config file lost.

- [ ] **Step 4: Run the tests to verify they pass**

```bash
uv run pytest tests/test_expose.py -v
```

Expected: all PASS, including every pre-existing test in the file (they don't assert save/no-save or restart on the scenarios this task changes).

- [ ] **Step 5: Run the focused tests and checks**

```bash
uv run pytest tests/test_expose.py tests/commands/test_expose.py tests/screens/test_app_navigation.py -q && make check
```

Expected: all green. (These are the files that exercise `expose_model`/`unexpose_model`; the TUI navigation test's seeded config lacks `litellm_settings`, so the ensure adds it — that test only asserts `model_list` content and state flags, so it passes.) The full suite runs once at the end (Task 5's `make all`).

- [ ] **Step 6: Commit**

```bash
git add src/modelman/litellm.py tests/test_expose.py
git commit -m "feat(litellm): ensure + changed-detection in expose/unexpose paths"
```

---

### Task 4: Wire ensure + changed-detection into `apply_expose_queue`

The TUI's apply-time path. Same rules as Task 3, with one queue-specific guarantee from the spec: save and restart run iff any queue item succeeded **or** the document changed — a fully-failed queue still persists the settings fix.

**Files:**
- Modify: `src/modelman/litellm.py:330-377` (`apply_expose_queue`)
- Test: `tests/test_queue.py`

- [ ] **Step 1: Write the failing tests**

Append to `tests/test_queue.py` (follow the file's existing local-import style inside each test):

```python
def test_apply_expose_queue_ensures_settings_when_all_items_fail(tmp_path, monkeypatch):
    # A fully-failed queue (model not ready) must still persist the
    # owned-settings fix and bounce the proxy: the ensure is orthogonal
    # to the queue's per-model outcomes (settings-persistence spec,
    # Decision 4).
    from modelman.litellm import apply_expose_queue, load_litellm_config, save_litellm_config
    from modelman.registry import AuthConfig, ModelEntry, ProviderEntry, Registry
    from modelman.state import ModelState, StateStore

    registry = Registry(
        providers=[
            ProviderEntry(
                id="ollama", name="Ollama",
                auth=AuthConfig(type="none", base_url="http://localhost:11434"),
            )
        ],
        models=[
            ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")
        ],
    )
    state = StateStore()
    state.set("ollama/a", ModelState(ready=False))
    path = tmp_path / "config.yaml"
    save_litellm_config({"model_list": [], "general_settings": {}}, path)

    calls = []
    monkeypatch.setattr(
        "modelman.litellm.restart_litellm_proxy", lambda: calls.append("restart") or []
    )
    outcomes, warnings = apply_expose_queue(registry, state, [("ollama/a", True)], path)
    assert outcomes[0][0] == "ollama/a"
    assert "not ready" in outcomes[0][2]
    config = load_litellm_config(path)
    assert config["litellm_settings"]["drop_params"] is True
    assert calls == ["restart"]


def test_apply_expose_queue_identical_reexpose_does_not_restart(tmp_path, monkeypatch):
    # Queueing an expose for a model whose entry and flag are already
    # correct changes nothing: no save (bytes unchanged), no flag flip,
    # no proxy bounce. The rebuilt entry temporarily drops the ensured
    # `additional_drop_params`, but the post-loop ensure re-adds it, so
    # the document compares equal and nothing is written.
    from modelman.litellm import apply_expose_queue, save_litellm_config
    from modelman.registry import AuthConfig, ModelEntry, ProviderEntry, Registry
    from modelman.state import ModelState, StateStore

    registry = Registry(
        providers=[
            ProviderEntry(
                id="ollama", name="Ollama",
                auth=AuthConfig(type="none", base_url="http://localhost:11434"),
            )
        ],
        models=[
            ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")
        ],
    )
    state = StateStore()
    state.set("ollama/a", ModelState(ready=True))
    path = tmp_path / "config.yaml"
    save_litellm_config({"model_list": [], "general_settings": {}}, path)

    calls = []
    monkeypatch.setattr(
        "modelman.litellm.restart_litellm_proxy", lambda: calls.append("restart") or []
    )
    outcomes, warnings = apply_expose_queue(registry, state, [("ollama/a", True)], path)
    assert calls == ["restart"]  # first apply: row added

    before = path.read_bytes()
    calls.clear()
    outcomes, warnings = apply_expose_queue(registry, state, [("ollama/a", True)], path)
    assert outcomes == [("ollama/a", True, None)]
    assert warnings == []
    assert calls == []
    assert path.read_bytes() == before
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
uv run pytest tests/test_queue.py -k apply_expose_queue -v
```

Expected: the two new tests FAIL (no ensure → no `litellm_settings` written / restart fires unconditionally on success). The two pre-existing `apply_expose_queue` tests still pass at this point.

- [ ] **Step 3: Implement the wiring**

Replace `apply_expose_queue`'s docstring and body in `src/modelman/litellm.py` (keep the signature; the docstring's `Returns` paragraph is preserved verbatim):

```python
def apply_expose_queue(
    registry: Registry,
    state: StateStore,
    exposes: list[tuple[str, bool]],
    litellm_path: Path,
) -> tuple[list[tuple[str, bool, str | None]], list[str]]:
    """Apply a queue of (model_id, target_exposed) pairs with a single
    config load and a single atomic save.

    This is the apply()-time path: one YAML parse and one rename for the
    whole queue instead of one per model, and config.yaml transitions
    from before to after in one step rather than risking a half-updated
    file if the process dies mid-queue. The standalone expose_model /
    unexpose_model remain for the CLI's one-model-at-a-time use.

    The owned-settings ensure runs over the whole parsed document before
    the save, and the save (and proxy restart) happen only when the
    document — the queue's rows or the ensured settings — actually
    changed: an idempotent re-expose writes nothing and does not bounce
    the proxy, while a fully-failed queue still persists a settings fix.

    Returns (outcomes, warnings). `outcomes` is one (model_id, target,
    error) per item, error None when applied. Config-level failures
    (missing/unwritable file) propagate to the caller; per-model
    validation failures are reported per item and don't block the rest
    of the queue. Flags flip only after the save succeeds, so state
    never claims an exposure the config file lost. `warnings` carries
    non-fatal proxy-restart notices (command unset or failed) so the
    caller can surface them on the UI thread instead of printing to
    stderr from a worker thread.
    """
    if not exposes:
        return [], []
    config = load_litellm_config(litellm_path)
    before = copy.deepcopy(config)
    outcomes: list[tuple[str, bool, str | None]] = []
    succeeded: list[tuple[str, bool]] = []
    for model_id, target in exposes:
        try:
            if target:
                set_exposed(config, model_id, _validated_entry(registry, state, model_id))
            else:
                remove_exposed(config, model_id)
        except ExposeError as exc:
            outcomes.append((model_id, target, str(exc)))
            continue
        outcomes.append((model_id, target, None))
        succeeded.append((model_id, target))
    ensure_litellm_settings(config)
    changed = config != before
    if changed:
        save_litellm_config(config, litellm_path)
    flags_changed = False
    for model_id, target in succeeded:
        if _set_exposed_flag(state, model_id, target):
            flags_changed = True
    if changed or flags_changed:
        return outcomes, restart_litellm_proxy()
    return outcomes, []
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
uv run pytest tests/test_queue.py -k apply_expose_queue -v
```

Expected: all four `apply_expose_queue` tests PASS (including the two pre-existing ones: a fresh expose still changes the document → save + one restart; an empty queue early-returns before any load).

- [ ] **Step 5: Run the focused tests and checks**

```bash
uv run pytest tests/test_queue.py tests/screens/test_app_navigation.py -q && make check
```

Expected: all green — this is the last code change; `queue.py`, the CLI, and the TUI call these functions with unchanged signatures. The full suite runs once at the end (Task 5's `make all`).

- [ ] **Step 6: Commit**

```bash
git add src/modelman/litellm.py tests/test_queue.py
git commit -m "feat(litellm): ensure + changed-detection in apply_expose_queue"
```

---

### Task 5: Documentation + final verification

Update the three docs that describe modelman's LiteLLM behavior: the module docstring (it still says modelman only touches `model_list`), the README's LiteLLM section, and CLAUDE.md's `litellm.py` bullets.

**Files:**
- Modify: `src/modelman/litellm.py:1-8` (module docstring)
- Modify: `README.md` (LiteLLM exposure section, ~line 213)
- Modify: `CLAUDE.md` (Architecture `litellm.py` bullet + repo-map bullet + implementation-notes bullet)

- [ ] **Step 1: Update the module docstring**

Replace the first paragraph of `src/modelman/litellm.py`'s module docstring (lines 1-8) with:

```python
"""LiteLLM exposure — build model_list entries and update config.yaml.

modelman manages the `model_list` section of LiteLLM's config.yaml,
keyed by registry model id as `model_name`, plus a small curated set of
launcher-required settings it *owns* and ensures on every write (see
`ensure_litellm_settings`). Everything else — `general_settings`,
`router_settings`, unknown keys, hand-written comments — passes through
untouched (ruamel round-trip keeps layout byte-identical), and a write
that changes nothing saves nothing. Known limitation: comments attached
to a specific `model_list` row (a comment on the line before a row) are
best-effort — they survive when the list is untouched, but are dropped
when modelman removes or replaces a row in that list. Top-level and
section-level comments (e.g. the hand-written `drop_params` note) always
survive. See
docs/superpowers/specs/2026-08-31-litellm-settings-persistence-design.md
and docs/superpowers/specs/2026-08-28-modelman-litellm-exposure-design.md.
"""
```

- [ ] **Step 2: Update the README's LiteLLM section**

In `README.md`, replace this paragraph:

```markdown
LiteLLM's `config.yaml` lives at `~/.config/litellm/config.yaml` by default
(override with `MODELMAN_LITELLM_CONFIG`). modelman only touches the
`model_list` section; `general_settings` and unrecognized rows are preserved.
```

with:

```markdown
LiteLLM's `config.yaml` lives at `~/.config/litellm/config.yaml` by default
(override with `MODELMAN_LITELLM_CONFIG`). Writes are read-modify-write with
ruamel round-trip, so sections modelman doesn't own (`general_settings`,
`router_settings`, unknown keys, hand-written comments) survive untouched.
modelman additionally owns two launcher-required settings and ensures them on
every write: `litellm_settings.drop_params: true` (without it, copilot's
`parallel_tool_calls` gets rejected with `400 UnsupportedParamsError`) and
`additional_drop_params: ["reasoning_effort"]` on every `model_list` entry
routed through the `ollama_chat/` bridge (a workaround for a LiteLLM 1.98.x
responses-bridge crash hit by codex, BerriAI/litellm#37452; an entry already
carrying the key, whatever its value, is left as-is). A write that changes
nothing saves nothing and does not restart the proxy.
```

- [ ] **Step 3: Update CLAUDE.md**

Three targeted edits. In the Architecture section, extend the `litellm.py` bullet — replace:

```markdown
- `src/modelman/litellm.py` — `expose_model`/`unexpose_model` add/remove entries in the LiteLLM config's `model_list` (one load/save per CLI call; `unexpose_model` is a no-op for ids missing from the registry). `PendingChanges.apply()` batches its queued exposes through `apply_expose_queue` instead — one config load/save for the whole queue.
```

with:

```markdown
- `src/modelman/litellm.py` — `expose_model`/`unexpose_model` add/remove entries in the LiteLLM config's `model_list` (one load/save per CLI call; `unexpose_model` is a no-op for ids missing from the registry). `PendingChanges.apply()` batches its queued exposes through `apply_expose_queue` instead — one config load/save for the whole queue. Every writer runs `ensure_litellm_settings()` before save (value-enforces `litellm_settings.drop_params: true`; adds `additional_drop_params: ["reasoning_effort"]` to every `ollama_chat/*` row missing it — the BerriAI/litellm#37452 codex workaround) and saves/restarts only when the parsed document or an exposed flag actually changed.
```

In the repo map section, replace:

```markdown
- `src/modelman/litellm.py` — owns all LiteLLM knowledge: provider→`model` prefix mapping, `model_list` entry construction, atomic `config.yaml` read/write, and the `expose_model`/`unexpose_model` orchestration used by both the CLI and TUI.
```

with:

```markdown
- `src/modelman/litellm.py` — owns all LiteLLM knowledge: provider→`model` prefix mapping, `model_list` entry construction, atomic `config.yaml` read/write (ruamel round-trip so comments and foreign sections survive byte-identically; PyYAML remains for the legacy modules), the `ensure_litellm_settings()` owned-settings pass, and the `expose_model`/`unexpose_model`/`apply_expose_queue` orchestration used by both the CLI and TUI.
```

In the implementation notes, extend the `save_litellm_config` bullet — replace:

```markdown
- `save_litellm_config` preserves the existing config.yaml's permission bits on rewrite (mkstemp's 0600 would otherwise silently tighten them); malformed or non-dict `model_list` content is preserved or refused, never crashed on.
```

with:

```markdown
- `save_litellm_config` preserves the existing config.yaml's permission bits on rewrite (mkstemp's 0600 would otherwise silently tighten them); malformed or non-dict `model_list` content is preserved or refused, never crashed on. Config writes use ruamel round-trip (`_rt_yaml()`: `typ="rt"`, `preserve_quotes=True`, `width=4096`) so hand-written comments and untouched sections survive byte-identically; changed-detection is `copy.deepcopy` before the mutation + ensure, `!=` after — save iff changed, proxy restart iff changed or a flag flipped.
```

- [ ] **Step 4: Run everything (full-suite gate)**

```bash
make all
```

Expected: formatting clean, full test suite green, lint + typecheck clean. This is the one place the full suite runs — the per-task steps above ran only the focused test files.

- [ ] **Step 5: Commit**

```bash
git add src/modelman/litellm.py README.md CLAUDE.md
git commit -m "docs: litellm settings-persistence behavior (README, CLAUDE.md)"
```

---

## Post-implementation smoke test (optional, manual)

After all tasks, run a real expose against the live config to watch the ensure repair the 6 drifted `ollama_chat/*` entries (back up first — this rewrites `~/.config/litellm/config.yaml` and restarts the proxy if `MODELMAN_LITELLM_RESTART_CMD` is set):

```bash
cp ~/.config/litellm/config.yaml /tmp/config.yaml.bak
uv run modelman expose <an-actually-ready-model-id>
uv run python -c "
from modelman.litellm import load_litellm_config
import pathlib
config = load_litellm_config(pathlib.Path('~/.config/litellm/config.yaml').expanduser())
for row in config['model_list']:
    if isinstance(row, dict):
        p = row.get('litellm_params') or {}
        if str(p.get('model', '')).startswith('ollama_chat/'):
            print(row.get('model_name'), '->', p.get('additional_drop_params'))
"
```

Expected: every `ollama_chat/*` entry (including the 6 that were missing it) prints `-> ['reasoning_effort']`, and the hand-written comment block is still present in the file.