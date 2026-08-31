# Modelman Family-Move Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let users move models between families from the TUI by adding a family selector to the model add/edit dialog, with the move queued (confirm/apply/discard) like every other change on the model screen.

**Architecture:** `ModelForm` gains a `Select` of known families and returns a new `ModelFormResult(spec, family)` instead of a bare `VariantSpec`. `ModelScreen` grows a fourth queue (`queued_moves`) that flows through the existing exit-confirm → `PendingChanges.apply()` → save path; moves are applied right after deletes, before downloads. Discard restores by model id (plus a session-added-ids set) so moved and out-of-family-added models revert cleanly.

**Tech Stack:** Python 3.13, Textual 8.2.8 (`Select`), pytest + pytest-asyncio, tomllib/tomli_w.

**Spec:** `docs/superpowers/specs/2026-08-29-modelman-family-move-design.md`

---

## Background for the implementing engineer

- `modelman` is a Textual TUI. Model ids (`ModelEntry.id`, e.g. `ollama/gemma4:26b-mlx`) are the global key for everything: `state.models` in `modelman.toml`, LiteLLM `model_list` entries, benchmark results. `family` is just a string column on `ModelEntry` in `registry.toml`, and families are *derived* (`registry.families() ∪ state.families`). Moving a model therefore touches only `registry.toml`.
- The problem being fixed: migration gave every model `family == model_name`, so `gemma4` has four bogus single-model "families". This feature lets the user create a real `gemma4` family and move each model into it from its stale family's screen.
- The model screen (`ModelScreen`) queues changes in dicts (`queued_downloads`, `queued_deletes`, `queued_exposes`), shows a pending bar, and on exit (`escape`) opens `ConfirmExitDialog` with **y** apply / **n** keep-queue / **d** discard. Apply runs on `StatusScreen` via a worker and persists with one `save_registry` + `save_state`. In-memory registry mutations for edits already happen immediately, but `family` must NOT be mutated in memory until apply (the queued-move convention; lets the row render with a `→` glyph in its current family).
- Test conventions: TUI tests use `pytest-asyncio` auto mode and `app.run_test()` pilots; `tests/screens/test_app_navigation.py` has a `_seed_registry_and_state(tmp_path, monkeypatch, ...)` and a `_make_screen(...)` helper; queue tests live in `tests/test_queue.py` with `_registry_with`, `_entry`, `_variant`, `_make_state` helpers; status-screen tests live in `tests/screens/test_status.py`.
- Every step below assumes repo root is the working directory and uses `uv run pytest` / `uv run python`. Commit after each task; the repo blocks direct commits to `main`, so work happens on branch `feat/modelman-family-move` (already created — verify with `git branch --show-current`; if missing, `git checkout -b feat/modelman-family-move`).

**Verification short-circuit:** at any point, run the full suite with `uv run pytest -q`. It must pass before every commit.

---

### Task 1: `PendingChanges.moves` — the apply layer

**Files:**
- Modify: `src/modelman/queue.py` (dataclass field + early return + apply loop)
- Test: `tests/test_queue.py`

- [ ] **Step 1: Write the failing tests**

Append to `tests/test_queue.py` (at module scope, after the existing tests; the helpers `_entry`, `_variant`, `_make_state`, `_registry_with` already exist there):

```python
# ---------------------------------------------------------------------------
# moves (family reassignment)
# ---------------------------------------------------------------------------


def test_apply_writes_queued_move_to_registry(tmp_path):
    """A queued move sets ModelEntry.family and persists it to
    registry.toml (move-only queues are legal and must still save)."""
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="m1", family="gemma4:26b-mlx", provider="ollama", name="gemma4:26b-mlx"),
    )
    state = _make_state()
    events: list[str] = []

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="gemma4:26b-mlx",
        registry_path=reg_path,
        state_path=tmp_path / "modelman.toml",
        providers={},
        moves=[("m1", "gemma4")],
    )
    pending.apply(on_event=events.append)

    assert reg.model("m1").family == "gemma4"
    # Move-only queues must still trigger the save (early-return guard
    # previously checked only downloads/deletes/exposes).
    assert load_registry(reg_path).model("m1").family == "gemma4"
    assert "move:start|m1|gemma4:26b-mlx|gemma4" in events
    assert "move:done|m1|gemma4:26b-mlx|gemma4" in events


def test_apply_move_for_deleted_model_is_dropped(tmp_path):
    """Deletes run before moves by design: a move for a model deleted
    in the same apply is moot and is skipped without failing."""
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="m1", family="gemma4:26b-mlx", provider="ollama", name="gemma4:26b-mlx"),
    )
    state = _make_state()
    provider = MagicMock()
    provider.name = "ollama"
    provider.delete.return_value = None
    events: list[str] = []

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="gemma4:26b-mlx",
        registry_path=reg_path,
        state_path=tmp_path / "modelman.toml",
        providers={"ollama": provider},
        deletes=[("m1", _variant(id="m1", provider="ollama", name="gemma4:26b-mlx"))],
        moves=[("m1", "gemma4")],
    )
    pending.apply(on_event=events.append)

    assert reg.models == []
    # The delete fired; the move produced no events at all.
    assert "delete:done|m1|gemma4:26b-mlx" in events
    assert not any(e.startswith("move:") for e in events)
    assert "apply:done" in events


def test_apply_move_only_queue_emits_apply_done(tmp_path):
    """A move-only queue is not treated as empty; apply runs the save
    and reports done."""
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="m1", family="a", provider="ollama", name="x"),
    )
    events: list[str] = []
    pending = PendingChanges(
        registry=reg,
        state=_make_state(),
        family="a",
        registry_path=reg_path,
        state_path=tmp_path / "modelman.toml",
        providers={},
        moves=[("m1", "b")],
    )
    pending.apply(on_event=events.append)
    assert "apply:done" in events
    assert "save:done" in events
```

Note: three of the imports used here (`load_registry`, `_variant`, and the module-level `PendingChanges`) already exist in `tests/test_queue.py`; verify the import line `from modelman.queue import PendingChanges` and `from modelman.registry import (AuthConfig, ModelEntry, ProviderEntry, Registry, load_registry, save_registry)` at the top cover what you use.

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/test_queue.py -q -k move`
Expected: FAIL — `TypeError: PendingChanges.__init__() got an unexpected keyword argument 'moves'`.

- [ ] **Step 3: Implement `moves` in `src/modelman/queue.py`**

(a) Add the field after `exposes` in the `PendingChanges` dataclass:

```python
    # (model_id, target_exposed) pairs applied after downloads, before save.
    exposes: list[tuple[str, bool]] = field(default_factory=list)
    # (model_id, new_family) pairs. Pure registry metadata: applies right
    # after deletes (a same-apply delete wins; its queued move is moot),
    # needs no provider interaction. A move-only queue still triggers the
    # final save.
    moves: list[tuple[str, str]] = field(default_factory=list)
    litellm_path: Path = field(default_factory=Path)
```

(b) Update the empty-queue early return at the top of `apply()`:

```python
        if not self.downloads and not self.deletes and not self.exposes and not self.moves:
            emit("apply:done")
            return
```

(c) Insert the move loop between the deletes loop and the downloads loop in `apply()` (right after the deletes `emit(f"delete:done|...")` block closes):

```python
        for model_id, new_family in self.moves:
            if aborted():
                return
            try:
                entry = self.registry.model(model_id)
            except KeyError:
                # Deleted earlier in this apply; its move is moot.
                continue
            emit(f"move:start|{model_id}|{entry.model_name}|{new_family}")
            try:
                entry.family = new_family
            except Exception as exc:  # noqa: BLE001
                self.failures.append(f"move {model_id}: {exc}")
                emit(f"move:fail|{model_id}|{entry.model_name}|{_reason(exc)}")
                continue
            emit(f"move:done|{model_id}|{entry.model_name}|{new_family}")
```

Also update the docstring of `apply()`: describe the order as "deletes first, then moves, then downloads, then exposes, then save registry+state once", and update the module docstring's opening paragraph to mention that queued family moves mutate registry.toml only.

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/test_queue.py -q`
Expected: all PASS (existing + 3 new).

- [ ] **Step 5: Commit**

```bash
git add src/modelman/queue.py tests/test_queue.py
git commit -m "feat(queue): add queued family moves to PendingChanges.apply()"
```

---

### Task 2: `StatusScreen` renders `move:*` events

**Files:**
- Modify: `src/modelman/screens/status.py` (`_handle_event`)
- Test: `tests/screens/test_status.py`

- [ ] **Step 1: Write the failing test**

Append to `tests/screens/test_status.py` (imports used: `pytest`, `StatusScreen` imported inside the test, `RichLog` from textual.widgets):

```python
@pytest.mark.asyncio
async def test_status_screen_renders_move_events():
    """move:start|done carry the target family in the 4th field;
    move:fail carries the reason. Without adding `move:` to the
    lifecycle-verb set these tags render as generic dim lines."""
    from textual.widgets import RichLog

    from modelman.screens.status import StatusScreen

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        screen = StatusScreen(family="gemma4:26b-mlx", run_apply=lambda *_: None)
        app.push_screen(screen)
        await pilot.pause()
        screen._handle_event("move:start|ollama/m1|gemma4:26b-mlx|gemma4")
        screen._handle_event("move:done|ollama/m1|gemma4:26b-mlx|gemma4")
        screen._handle_event("move:fail|ollama/m2|gemma4:12b-mlx|boom")
        await pilot.pause()

        log = screen.query_one("#status-log", RichLog)
        text = "\n".join(strip.text for strip in log.lines)
        assert "Moving gemma4:26b-mlx → gemma4..." in text
        assert "Moved gemma4:26b-mlx → gemma4" in text
        assert "Failed to move gemma4:12b-mlx" in text
        assert "boom" in text
```

If `test_status.py` does not import `ModelmanApp`, add `from modelman.app import ModelmanApp` at the top (check the file first; the `app_with_apply` fixture suggests app-level tests may already import it).

- [ ] **Step 2: Run the test to verify it fails**

Run: `uv run pytest tests/screens/test_status.py::test_status_screen_renders_move_events -q`
Expected: FAIL — the log contains the raw dim tags, not the formatted lines.

- [ ] **Step 3: Implement in `src/modelman/screens/status.py`**

(a) In `_handle_event`, add `move:` to the lifecycle-prefix guard (the `if not tag.startswith(...)` chain at the top of the method):

```python
        if (
            not tag.startswith("delete:")
            and not tag.startswith("download:")
            and not tag.startswith("move:")
            and not tag.startswith("save:")
            and not tag.startswith("apply:")
        ):
```

(b) In the parsing chain, after the `download:done` size branch and before the final `else`, add:

```python
        elif verb in ("move:start", "move:done", "move:fail") and len(parts) == 4:
            # 4th field: target family for start/done, failure reason
            # for fail (reuses download:done's size-slot convention).
            label = parts[2]
            reason = parts[3]
```

(c) In the rendering chain, after the `download:cancelled` branch and before the `save:start` branch, add:

```python
        elif verb == "move:start":
            log.write(f"· Moving {label} → {reason}...")
        elif verb == "move:done":
            log.write(f"  [green]✓[/green] Moved {label} → {reason}")
        elif verb == "move:fail":
            log.write(f"  [red]✗[/red] Failed to move {label}")
            if reason:
                log.write(f"    [red dim]{reason}[/red dim]")
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/screens/test_status.py -q`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add src/modelman/screens/status.py tests/screens/test_status.py
git commit -m "feat(status): render move:* lifecycle events"
```

---

### Task 3: `ModelFormResult` + family `Select` in the form

**Files:**
- Modify: `src/modelman/screens/forms.py`
- Modify: `src/modelman/screens/models.py` (only the two result-callbacks — unpack the new result type so existing integration tests keep passing; full move bookkeeping lands in Task 4)
- Test: `tests/screens/test_forms.py` (update 6 result-asserting tests, add 4 new)

- [ ] **Step 1: Update the existing result-asserting form tests (mechanical unpack)**

`_submit` dismisses `ModelFormResult(spec, family)` after this task, so every test that reads the dismissed dict must unpack `.spec` first. In `tests/screens/test_forms.py`, make these exact changes:

1. `test_submit_ollama_tag_produces_correct_spec` — replace
   ```python
    spec = dismissed[0]
   ```
   with
   ```python
    result = dismissed[0]
    spec = result.spec
   ```
2. `test_submit_hf_repo_only_produces_correct_spec` — same replacement (`spec = dismissed[0]` → unpack via `.spec`).
3. `test_submit_hf_repo_and_file_produces_correct_spec` — same replacement.
4. `test_submit_after_fix_clears_error_and_dismisses` — replace
   ```python
    assert dismissed[0]["name"] == "goodname:tag"
   ```
   with
   ```python
    assert dismissed[0].spec["name"] == "goodname:tag"
   ```
5. `test_submit_in_edit_mode_preserves_id` — `spec = dismissed[0]` → `spec = dismissed[0].spec` (single line removal of the unpack shim).
6. `test_submit_in_edit_mode_preserves_quantizations` — same single-line unpack update.

(`test_submit_ollama_rejects_slash_with_inline_error`, `test_submit_hf_rejects_single_segment_with_inline_error`, and `test_submit_empty_model_does_not_dismiss` only assert `dismissed == []` and need no changes.)

- [ ] **Step 2: Add the new form tests**

Append to `tests/screens/test_forms.py`; update its import line to include the new symbols:

```python
from textual.widgets import Input, Label, Select

from modelman.screens.forms import ModelForm, ModelFormResult
```

New tests:

```python
# ---------------------------------------------------------------------------
# Family selector
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_modelform_shows_family_select_with_all_families():
    """The Family Select lists every family passed by the caller and
    pre-selects the current family."""
    form = ModelForm(
        providers=["ollama"],
        families=["deepseek-v4", "gemma4", "gemma4:26b-mlx"],
        family="gemma4:26b-mlx",
    )
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        sel = app.screen.query_one("#family-select", Select)
        assert str(sel.value) == "gemma4:26b-mlx"
        # Select._options is list[SelectOption]; private but stable in
        # Textual 8.x and far simpler than driving ArrowDown.
        assert [str(o.value) for o in sel._options] == [
            "deepseek-v4",
            "gemma4",
            "gemma4:26b-mlx",
        ]


@pytest.mark.asyncio
async def test_modelform_prepends_current_family_when_missing():
    """Defensive: if the current family isn't in the list (e.g. the
    caller's families list was stale), it must be prepended and chosen."""
    form = ModelForm(
        providers=["ollama"],
        families=["gemma4"],
        family="gemma4:26b-mlx",
    )
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        sel = app.screen.query_one("#family-select", Select)
        assert [str(o.value) for o in sel._options] == ["gemma4:26b-mlx", "gemma4"]
        assert str(sel.value) == "gemma4:26b-mlx"


@pytest.mark.asyncio
async def test_modelform_family_select_defaults_when_no_families_passed():
    """Legacy direct callers (tests) pass nothing: the selector shows
    exactly one entry. The TUI always passes real values."""
    form = ModelForm(providers=["ollama"])
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        sel = app.screen.query_one("#family-select", Select)
        assert [str(o.value) for o in sel._options] == ["unknown"]


@pytest.mark.asyncio
async def test_submit_returns_modelformresult_with_selected_family():
    """Save dismisses ModelFormResult: the spec plus the family value
    from the Select (unchanged here because #model keeps focus)."""
    form = ModelForm(
        providers=["ollama"],
        families=["gemma4", "gemma4:26b-mlx"],
        family="gemma4:26b-mlx",
    )
    dismissed: list = []

    def _capture(result):
        dismissed.append(result)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form, _capture)
        await pilot.pause()
        _fill_model(app, "gemma4:26b")
        await pilot.press("enter")
        await pilot.pause()

    result = dismissed[0]
    assert isinstance(result, ModelFormResult)
    assert result.family == "gemma4:26b-mlx"
    assert result.spec["provider"] == "ollama"
    assert result.spec["name"] == "gemma4:26b"


@pytest.mark.asyncio
async def test_submit_returns_family_switched_in_the_select():
    """Choosing a different family in the Select is what the result
    carries — that's the whole point of the family-move feature."""
    form = ModelForm(
        providers=["ollama"],
        families=["gemma4", "gemma4:26b-mlx"],
        family="gemma4:26b-mlx",
    )
    dismissed: list = []

    def _capture(result):
        dismissed.append(result)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form, _capture)
        await pilot.pause()
        sel = app.screen.query_one("#family-select", Select)
        sel.value = "gemma4"
        _fill_model(app, "gemma4:26b")
        await pilot.press("enter")
        await pilot.pause()

    result = dismissed[0]
    assert isinstance(result, ModelFormResult)
    assert result.family == "gemma4"
```

- [ ] **Step 3: Run tests to verify the new ones fail**

Run: `uv run pytest tests/screens/test_forms.py -q -k family`
Expected: FAIL — `ImportError: cannot import name 'ModelFormResult'`.

- [ ] **Step 4: Implement in `src/modelman/screens/forms.py`**

(a) Extend the imports:

```python
from typing import Literal, NamedTuple, cast

from textual.widgets import Button, Input, Label, Select
```

(b) Below `parse_model`, add:

```python
class ModelFormResult(NamedTuple):
    """ModelForm's dismiss payload: the VariantSpec plus the family the
    user chose. Family is deliberately separate from VariantSpec — the
    spec dict is the provider-facing contract and has no family field;
    ModelScreen maps family onto ModelEntry.family."""

    spec: VariantSpec
    family: str
```

(c) In `ModelForm.__init__`, keep the existing signature prefix and append the new params (after `default_provider`):

```python
    def __init__(
        self,
        providers: list[str],
        variant: VariantSpec | None = None,
        default_provider: str | None = None,
        families: list[str] | None = None,
        family: str | None = None,
    ) -> None:
        super().__init__()
        self._providers = providers
        self._variant = variant  # None for add
        # Used only in add mode (variant is None): the provider to
        # pre-fill in the static provider label. Edit mode always
        # uses the variant's own provider since provider is
        # immutable on edit.
        self._default_provider = default_provider
        # Families the selector offers (sorted by the caller) and the
        # pre-selected family. When a caller passes neither, the
        # selector degrades to a single "unknown" entry — the TUI
        # always passes real values; only direct test callers hit
        # this default.
        self._families: list[str] = (
            list(families) if families else ([family] if family else ["unknown"])
        )
        self._family = family
```

(d) In `compose()`, add the selector between the provider label and the model label, and extend the CSS: inside `ModelForm.DEFAULT_CSS` (the block with `ModelForm Label { margin-top: 1; }`) add a rule for Select. Edit `compose()`:

```python
        with Vertical():
            yield Label(f"Provider: {initial_provider}", id="provider-label")
            yield Label("Family:")
            yield Select(
                options=[(f, f) for f in self._families],
                value=(
                    self._family
                    if self._family in self._families
                    else self._families[0]
                ),
                allow_blank=False,
                id="family-select",
            )
            yield Label("Model:")
```

(add to `ModelForm.DEFAULT_CSS`, after the `ModelForm Input` line):

```css
    ModelForm Select { margin-bottom: 1; }
```

Note on the defensive prepend: the `self._family in self._families` check only covers selection; when the spec says "prepend the current family if missing", implement it here — extend the `self._families` assignment in `__init__` instead so the option list is also correct (the compose value fallback stays harmless):

```python
        self._families: list[str] = (
            list(families) if families else ([family] if family else ["unknown"])
        )
        if self._family is not None and self._family not in self._families:
            self._families.insert(0, self._family)
```

(e) In `_submit()`, read the family from the Select and dismiss the result object. Replace the final line `self.dismiss(spec)` with:

```python
        family = str(self.query_one("#family-select", Select).value)
        self.dismiss(ModelFormResult(spec=spec, family=family))
```

Also update `ModelForm`'s class docstring: result is now `ModelFormResult(spec, family)`; add one sentence: the family Select defaults to the family the model screen is showing, and can target any known family (registry families + explicitly created empty ones).

- [ ] **Step 5: Update the screen-side result consumers (keeps integration tests green)**

In `src/modelman/screens/models.py`, `dismiss` results are now `ModelFormResult`. Update both callbacks:

```python
    def _on_add_model(self, result) -> None:
        if result is None:
            return
        variant = result.spec
        if any(m.id == variant["id"] for m in self.registry.models):
            self.app.notify("Model ID already exists")
            return
        entry = _variant_to_model_entry(variant, family=result.family, registry=self.registry)
        self.registry.models.append(entry)
        self.queued_downloads[variant["id"]] = variant
        self.reload()
        self._refresh_pending_bar()
```

```python
    def _on_edit_model(self, result) -> None:
        if result is None:
            return
        updated = result.spec
        new_entry = _variant_to_model_entry(updated, family=self.family, registry=self.registry)
        for i, m in enumerate(self.registry.models):
            if m.id == updated["id"]:
                self.registry.models[i] = new_entry
                break
        if updated["id"] in self.queued_downloads:
            self.queued_downloads[updated["id"]] = updated
        self.reload()
```

(In-memory `family` stays `self.family` for edits; the queued-move bookkeeping is Task 4. Add-mode now honors the form's family, which is this task's actual behavior change — the integration test `test_model_screen_add_appends_model_entry_to_registry` still passes because it never touches the Select.)

- [ ] **Step 6: Run the test suites to verify everything passes**

Run: `uv run pytest tests/screens/test_forms.py tests/screens/test_app_navigation.py -q`
Expected: all PASS (30 existing form tests with the 6 unpack updates + 4 new; navigation tests untouched).

- [ ] **Step 7: Commit**

```bash
git add src/modelman/screens/forms.py src/modelman/screens/models.py tests/screens/test_forms.py
git commit -m "feat(forms): ModelForm family select + ModelFormResult(spec, family)"
```

---

### Task 4: `ModelScreen` — queued moves bookkeeping, families list, visibility

**Files:**
- Modify: `src/modelman/screens/models.py`
- Modify: `tests/screens/test_app_navigation.py` (`_make_screen` gains a `families` kwarg)

- [ ] **Step 1: Extend `_make_screen` in `tests/screens/test_app_navigation.py`**

 so tests can seed state-only families (exactly created-but-empty ones):

```python
def _make_screen(tmp_path, monkeypatch, *, family: str = "ornith", entries=(), families=()):
    """Build a ModelScreen with registry.toml + modelman.toml in tmp_path
    and seed registry with the given ModelEntries. `families` seeds
    StateStore.families (explicitly created, possibly empty families).
    Returns (ms, registry_path, state_path)."""
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[
            ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none")),
            ProviderEntry(id="llamacpp", name="L", auth=AuthConfig(type="none")),
            ProviderEntry(id="omlx", name="X", auth=AuthConfig(type="omlx")),
        ],
        models=list(entries),
    )
    save_registry(reg, reg_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    from modelman.screens.models import ModelScreen
    from modelman.state import FamilyState

    ms = ModelScreen(
        registry=reg,
        state=StateStore(families={f: FamilyState() for f in families}),
        family=family,
        registry_path=reg_path,
        state_path=state_path,
        available_providers=["ollama", "llamacpp", "omlx"],
    )
    return ms, reg_path, state_path
```

- [ ] **Step 2: Write the failing screen tests**

Append to `tests/screens/test_app_navigation.py`. Imports needed at the test-file level (most already exist): `pytest`, `ModelmanApp`, `ModelEntry`, `DataTables`, `Input`, and `ModelScreen` via `_make_screen`. Add one import helper for the Select:

```python
from textual.widgets import Select
```

Tests:

```python
# ---------------------------------------------------------------------------
# Family moves (queued)
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_edit_model_changing_family_queues_move(tmp_path, monkeypatch):
    """Editing a model and picking a different family queues a move:
    pending bar shows it, the row shows a → glyph, and the in-memory
    registry family is untouched until apply."""
    entry = ModelEntry(
        id="ollama/gemma4:26b-mlx",
        family="gemma4:26b-mlx",
        provider_id="ollama",
        model_name="gemma4:26b-mlx",
    )
    ms, _reg, _state = _make_screen(
        tmp_path,
        monkeypatch,
        family="gemma4:26b-mlx",
        entries=[entry],
        families=["gemma4"],
    )

    from modelman.app import ModelmanApp
    from modelman.screens.forms import ModelForm

    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        ms.query_one("#model-table", DataTable).focus()
        mt = ms.query_one("#model-table", DataTable)
        mt.cursor_coordinate = (0, 0)
        await pilot.press("e")
        await pilot.pause()
        assert isinstance(app.screen, ModelForm)

        from textual.widgets import Select

        sel = app.screen.query_one("#family-select", Select)
        assert str(sel.value) == "gemma4:26b-mlx"  # re-edit preselect = current family
        sel.value = "gemma4"
        await pilot.press("enter")  # submit the (prefilled) edit form
        await pilot.pause()

    assert ms.queued_moves == {"ollama/gemma4:26b-mlx": "gemma4"}
    # Registry NOT mutated in memory — move applies at apply time.
    assert ms.registry.models[0].family == "gemma4:26b-mlx"
    mt = ms.query_one("#model-table", DataTable)
    rows = {r[0]: r for r in [mt.get_row_at(i) for i in range(mt.row_count)]}
    assert "→" in rows["gemma4:26b-mlx"][1]  # STATUS column
    bar = ms.query_one("#pending-bar")
    assert "move 1" in bar.content


@pytest.mark.asyncio
async def test_edit_model_same_family_drops_queued_move(tmp_path, monkeypatch):
    """Editing a model back to the screen family cancels a pending move
    instead of queueing a no-op."""
    entry = ModelEntry(
        id="ollama/gemma4:26b-mlx",
        family="gemma4:26b-mlx",
        provider_id="ollama",
        model_name="gemma4:26b-mlx",
    )
    ms, _reg, _state = _make_screen(
        tmp_path, monkeypatch, family="gemma4:26b-mlx", entries=[entry]
    )
    ms.queued_moves["ollama/gemma4:26b-mlx"] = "gemma4"

    from modelman.app import ModelmanApp
    from modelman.screens.forms import ModelForm

    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        mt = ms.query_one("#model-table", DataTable)
        mt.focus()
        mt.cursor_coordinate = (0, 0)
        await pilot.press("e")
        await pilot.pause()
        assert isinstance(app.screen, ModelForm)
        # The re-edit Select honors the already-queued move's target…
        from textual.widgets import Select

        sel = app.screen.query_one("#family-select", Select)
        assert str(sel.value) == "gemma4"
        sel.value = "gemma4:26b-mlx"  # ...moved back to the screen family
        await pilot.press("enter")
        await pilot.pause()

    assert ms.queued_moves == {}


def test_families_list_includes_state_only_families(tmp_path, monkeypatch):
    """`_families_list()` = registry families ∪ state.families — so a
    family created empty with `a` (like gemma4/deepseek-v4) is targetable
    before it has any models."""
    entry = ModelEntry(
        id="ollama/gemma4:26b-mlx",
        family="gemma4:26b-mlx",
        provider_id="ollama",
        model_name="gemma4:26b-mlx",
    )
    ms, _reg, _state = _make_screen(
        tmp_path,
        monkeypatch,
        family="gemma4:26b-mlx",
        entries=[entry],
        families=["gemma4", "deepseek-v4"],
    )
    assert ms._families_list() == ["gemma4", "gemma4:26b-mlx"]
```

(`gemma4 < "gemma4:26b-mlx"` lexicographically: ":" (0x3a) sorts after digits, so the state-only family comes first.)

- [ ] **Step 3: Run tests to verify they fail**

Run: `uv run pytest tests/screens/test_app_navigation.py -q -k "queues_move or drops_queued_move or families_list"`
Expected: FAIL — `AttributeError: 'ModelScreen' object has no attribute 'queued_moves'`.

- [ ] **Step 4: Implement in `src/modelman/screens/models.py`**

(a) In `ModelScreen.__init__`, after the `self.queued_exposes` assignment, add:

```python
        # model_id -> target family. Applied to the registry at apply()
        # time; the in-memory family is untouched until then so the row
        # stays visible in this family's table with a → glyph.
        self.queued_moves: dict[str, str] = {}
        # Ids of models created this session via the add dialog. Used by
        # _restore_snapshot: a model added into a *different* family
        # isn't caught by the family-scoped restore filter.
        self._added_ids: set[str] = set()
```

(b) Add the families helper next to `_provider_list`:

```python
    def _families_list(self) -> list[str]:
        """Every family the add/edit dialogs may target: families with
        models in the registry plus explicitly created-but-empty ones
        (state.families), sorted."""
        return sorted(set(self.registry.families()) | set(self.state.families))
```

(c) Update `action_add_model` to pass the family args:

```python
        self.app.push_screen(
            ModelForm(
                providers=providers,
                default_provider=default_provider,
                families=self._families_list(),
                family=self.family,
            ),
            self._on_add_model,
        )
```

(d) In `_on_add_model`, track session adds (after `self.registry.models.append(entry)`):

```python
        self._added_ids.add(variant["id"])
```

(e) Update `action_edit_model` to pass families and honor an already-queued move in the pre-selection (the spec's re-edit rule — otherwise re-editing a moved model would silently drop the pending move):

```python
        spec = _model_entry_to_variant(entry)
        self.app.push_screen(
            ModelForm(
                providers=self._provider_list(),
                variant=spec,
                families=self._families_list(),
                family=self.queued_moves.get(mid, self.family),
            ),
            self._on_edit_model,
        )
```

(f) In `_on_edit_model`, after the entry-replacement loop, add the move bookkeeping:

```python
        if result.family != self.family:
            self.queued_moves[updated["id"]] = result.family
        else:
            self.queued_moves.pop(updated["id"], None)
```

(g) In `_load_models_for_provider`, insert the move glyph between the download and downloaded states (priority `✗ delete > ↓ download > → move > ✓ > ○`):

```python
            if m.id in self.queued_deletes:
                status = "[red]✗[/red]"
            elif m.id in self.queued_downloads:
                status = "[yellow]↓[/yellow]"
            elif m.id in self.queued_moves:
                status = "[magenta]→[/magenta]"
            elif downloaded:
                status = "[green]✓[/green]"
            else:
                status = "[dim]○[/dim]"
```

(h) `_refresh_pending_bar` gains the move count:

```python
        bar.update(
            f"Pending: download {len(self.queued_downloads)} · delete {len(self.queued_deletes)}"
            f" · move {len(self.queued_moves)} · expose {len(self.queued_exposes)}"
        )
```

(i) `action_back`'s early-pop gate includes moves:

```python
        if (
            not self.queued_downloads
            and not self.queued_deletes
            and not self.queued_moves
            and not self.queued_exposes
        ):
            self.app.pop_screen()
            return
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `uv run pytest tests/screens -q`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add src/modelman/screens/models.py tests/screens/test_app_navigation.py
git commit -m "feat(tui): queue family moves from the model edit dialog"
```

---

### Task 5: Exit path — ConfirmExitDialog, apply hand-off, discard safety

**Files:**
- Modify: `src/modelman/screens/forms.py` (`ConfirmExitDialog`)
- Modify: `src/modelman/screens/models.py` (`action_back`, `_on_exit_confirm`, `_run_apply`, `_restore_snapshot`)
- Test: `tests/screens/test_app_navigation.py`

- [ ] **Step 1: Write the failing tests**

Append to `tests/screens/test_app_navigation.py`:

```python
@pytest.mark.asyncio
async def test_exit_dialog_lists_move_and_apply_persists_it(tmp_path, monkeypatch):
    """Escape with a queued move opens the exit dialog (move N + a
    listed line), and apply persists the new family to registry.toml."""
    entry = ModelEntry(
        id="ollama/gemma4:26b-mlx",
        family="gemma4:26b-mlx",
        provider_id="ollama",
        model_name="gemma4:26b-mlx",
    )
    ms, reg_path, _state = _make_screen(
        tmp_path,
        monkeypatch,
        family="gemma4:26b-mlx",
        entries=[entry],
        families=["gemma4"],
    )

    from modelman.app import ModelmanApp
    from modelman.registry import load_registry
    from modelman.screens.status import StatusScreen

    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        # Queue the move the same way _on_edit_model does (the dialog
        # flow itself is covered by Task 4's tests).
        ms.queued_moves["ollama/gemma4:26b-mlx"] = "gemma4"
        await pilot.press("escape")
        await pilot.pause()
        from textual.widgets import Label

        labels = "\n".join(str(l.visual) for l in app.screen.query(Label))
        assert "move 1" in labels
        assert "→ ollama/gemma4:26b-mlx → gemma4" in labels
        from textual.widgets import Button

        for btn in app.screen.query(Button):
            if btn.id == "apply":
                btn.press()
                break
        for _ in range(50):
            await pilot.pause()
            cur = app.screen
            if isinstance(cur, StatusScreen) and cur.done:
                break

    reloaded = load_registry(reg_path)
    assert reloaded.model("ollama/gemma4:26b-mlx").family == "gemma4"


@pytest.mark.asyncio
async def test_discard_after_move_reverts_without_duplicates(tmp_path, monkeypatch):
    """`d` at the exit dialog must drop the queued move AND restore the
    registry exactly as at mount — no duplicated models (the old
    family-scoped restore logic would duplicate a moved model between
    the snapshot and the 'kept' list)."""
    entry = ModelEntry(
        id="ollama/gemma4:26b-mlx",
        family="gemma4:26b-mlx",
        provider_id="ollama",
        model_name="gemma4:26b-mlx",
    )
    ms, reg_path, _state = _make_screen(
        tmp_path, monkeypatch, family="gemma4:26b-mlx", entries=[entry]
    )

    from modelman.app import ModelmanApp
    from modelman.registry import load_registry

    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        ms.queued_moves["ollama/gemma4:26b-mlx"] = "gemma4"
        await pilot.press("escape")
        await pilot.pause()
        await pilot.press("d")  # discard
        await pilot.pause()

    ids = [m.id for m in ms.registry.models]
    assert ids == ["ollama/gemma4:26b-mlx"]
    assert ms.registry.models[0].family == "gemma4:26b-mlx"
    # Disk untouched.
    assert load_registry(reg_path).model("ollama/gemma4:26b-mlx").family == "gemma4:26b-mlx"


@pytest.mark.asyncio
async def test_discard_removes_out_of_family_added_model(tmp_path, monkeypatch):
    """A model added into a *different* family this session isn't caught
    by the family-scoped restore filter; discard must remove it."""
    entry = ModelEntry(
        id="ollama/keep",
        family="ornith",
        provider_id="ollama",
        model_name="keep",
    )
    ms, _reg_path, _state = _make_screen(
        tmp_path, monkeypatch, family="ornith", entries=[entry]
    )

    from modelman.app import ModelmanApp
    from modelman.screens.forms import ModelFormResult

    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        # Out-of-family add, applied exactly as _on_add_model does.
        ms._on_add_model(
            ModelFormResult(
                spec={"id": "ollama/moved", "provider": "ollama", "name": "moved:9b"},
                family="mamba",
            )
        )
        assert "ollama/moved" in [m.id for m in ms.registry.models]
        await pilot.press("escape")
        await pilot.pause()
        await pilot.press("d")  # discard
        await pilot.pause()

    ids = [m.id for m in ms.registry.models]
    assert ids == ["ollama/keep"]
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/screens/test_app_navigation.py -q -k "exit_dialog_lists_move or discard_after_move or out_of_family_added"`
Expected: FAIL — `ConfirmExitDialog.__init__() got an unexpected keyword argument 'moves'` (first test) / registry-restore assertion failures (others).

- [ ] **Step 3: Implement `ConfirmExitDialog.moves` in `src/modelman/screens/forms.py`**

```python
    def __init__(
        self,
        downloads: list,
        deletes: list,
        exposes: list[tuple[str, bool]] | None = None,
        moves: list[tuple[str, str]] | None = None,
    ) -> None:
        super().__init__()
        self._downloads = downloads
        self._deletes = deletes
        self._exposes = exposes or []
        self._moves = moves or []
```

and in `compose()`:

```python
        with Vertical():
            yield Label(
                f"Pending: download {len(self._downloads)} · delete {len(self._deletes)}"
                f" · move {len(self._moves)} · expose {len(self._exposes)}"
            )
            for v in self._downloads:
                yield Label(f"  ↓ {v['id']} ({v['provider']})")
            for v in self._deletes:
                yield Label(f"  × {v['id']} ({v['provider']})")
            for model_id, target in self._moves:
                yield Label(f"  → {model_id} → {target}")
            for model_id, exposed in self._exposes:
                mark = "L" if exposed else "–"
                yield Label(f"  {mark} {model_id}")
```

- [ ] **Step 4: Wire the exit path in `src/modelman/screens/models.py`**

(a) `action_back` passes the moves:

```python
        self.app.push_screen(
            ConfirmExitDialog(
                downloads=list(self.queued_downloads.values()),
                deletes=list(self.queued_deletes.values()),
                exposes=list(self.queued_exposes.items()),
                moves=list(self.queued_moves.items()),
            ),
            self._on_exit_confirm,
        )
```

(b) `_on_exit_confirm`'s discard branch also clears moves:

```python
        if choice == "discard":
            self._restore_snapshot()
            self.queued_downloads.clear()
            self.queued_deletes.clear()
            self.queued_moves.clear()
            self.queued_exposes.clear()
            self._added_ids.clear()
            self.app.pop_screen()
            return
```

(c) `_run_apply` passes moves to `PendingChanges` and clears them after the run:

```python
        pending = PendingChanges(
            registry=self.registry,
            state=self.state,
            family=self.family,
            registry_path=self.registry_path,
            state_path=self.state_path,
            providers=providers,
            downloads=[(mid, spec) for mid, spec in self.queued_downloads.items()],
            deletes=[(mid, spec) for mid, spec in self.queued_deletes.items()],
            moves=list(self.queued_moves.items()),
            exposes=list(self.queued_exposes.items()),
            litellm_path=default_litellm_config_path(),
        )
        register(pending)
        pending.apply(on_event=on_event, on_progress=on_progress)
        # The closure runs on the StatusScreen's worker thread; mutate
        # in-memory queue state from here too so subsequent opens of this
        # screen see an empty queue.
        self.queued_downloads.clear()
        self.queued_deletes.clear()
        self.queued_moves.clear()
        self.queued_exposes.clear()
        self._added_ids.clear()
```

(d) Rewrite `_restore_snapshot` (id-keyed restore — the discard-safety fix from the spec):

```python
    def _restore_snapshot(self) -> None:
        """Restore the in-memory registry/state to the snapshot taken on
        mount, dropping any queued mutations.

        Restore is keyed by model id, not family: models with queued
        (unapplied) moves still belong to this family in the registry, and
        keying by family would duplicate them (snapshot entry + live
        entry). _added_ids kills the second gap: a model added into a
        *different* family this session isn't caught by the family-scoped
        filter and would otherwise survive discard.
        """
        restore_ids = {m.id for m in self._snapshot_models} | self._added_ids
        keep = [
            m
            for m in self.registry.models
            if m.id not in restore_ids and m.family != self.family
        ]
        self.registry.models = keep + self._snapshot_models
        # Replace state entries that were in the snapshot.
        for mid in self._snapshot_state_entries:
            self.state.set(mid, self._snapshot_state_entries[mid])
        # Drop state entries that were added during this session but
        # weren't in the snapshot, scoped to this family.
        for mid in list(self.state.models):
            if mid not in self._snapshot_state_entries and any(
                m.id == mid and m.family == self.family for m in self.registry.models
            ):
                self.state.models.pop(mid, None)
```

- [ ] **Step 5: Run the full test suite**

Run: `uv run pytest -q`
Expected: all PASS (note `test_discard_pending_exits_without_applying` and `test_model_screen_discard_restores_fetch_dataclass` must still pass under the rewritten restore).

- [ ] **Step 6: Commit**

```bash
git add src/modelman/screens/forms.py src/modelman/screens/models.py tests/screens/test_app_navigation.py
git commit -m "feat(tui): family moves ride the exit-confirm/apply flow with id-keyed discard"
```

---

### Task 5b: end-to-end sanity pass (manual, 2 minutes)

**Files:** none (manual verification)

- [ ] **Step 1: Reproduce the actual gemma4 fix in a scratch registry**

All state paths are env-var redirectable, so this never touches real config:

```bash
rm -f /tmp/mm-reg.toml /tmp/mm-state.toml
MODELMAN_REGISTRY=/tmp/mm-reg.toml MODELMAN_STATE=/tmp/mm-state.toml uv run modelman
```

Keystroke walkthrough against the scratch registry:

1. Family screen (empty): press `a`, type `gemma4`, Enter → empty `gemma4` row appears.
2. Press `a` on the scratch-family default (first row is auto-created? No — press `a` on the family screen twice: first edit registry-free families. Simpler: with only `gemma4` open, press `enter`, then `a` in the model screen and submit ollama tag `scratch:test` (no real download — Escape out of the queue with `d` if prompted... nothing is queued until you toggle `x`, so plain escape pops).
3. Open family `scratch:test` (or whichever family the add dialog's Family Select defaulted to), edit the model with `e`, set Family to `gemma4`, Save.
4. Confirm: model row shows `→`, bar shows `move 1`, escape → exit dialog lists `→ ollama/scratch:test → gemma4`, apply, and the family screen shows `gemma4` with 1 model (press `r` or wait for resume-reconcile).

Expected: no tracebacks; the emptied scratch family drops out of the list (it was never in state.families).

---

### Task 6: Docs + full check

**Files:**
- Modify: `CLAUDE.md` (Textual TUI + queue bullets)
- Modify: `README.md` (only if it documents the pending set — line 198 lists queue order)
- Test: none (full-suite run)

- [ ] **Step 1: Update `CLAUDE.md`**

1. In the "### Pending changes queue" bullet for `src/modelman/queue.py`, change the ordering sentence to:

```markdown
- `src/modelman/queue.py` — `PendingChanges(registry, state, providers, downloads, deletes, moves, exposes, failures)`. `apply()` runs deletes first (so downloads free up disk), then moves (pure registry metadata: `ModelEntry.family = new_family`; a move for a model deleted in the same apply is dropped), then downloads (each calls `provider.download(variant)` → `state.mark_downloaded(...)`), then exposes (each writes a LiteLLM `model_list` entry), then a single `save_registry()` + `save_state()`. Failures are captured per-step, processing continues.
```

2. In the "src/modelman/screens/forms.py — modal screens" bullet, replace `ModelForm (add/edit with disabled=editing on provider/id for immutability)` with:

```markdown
`ModelForm` (add/edit with `disabled=editing` on provider/id for immutability and a family `Select`; dismisses `ModelFormResult(spec, family)`)
```

3. In the `screens/models.py` bullet, extend the action list with: `a` add model (`family` selectable), `e` edit (provider/id fixed, family change queues a `move`), and note the fifth status glyph `→` for queued moves in the models-table columns description (`name · status · size · path · EXPOSED` stays, but mention moves render `→` in STATUS).

- [ ] **Step 2: Update `README.md`**

Only one sentence exists about apply ordering (line 198, architecture notes). Update it to mention moves:

```markdown
- `src/modelman/queue.py` — `PendingChanges` orchestrates queued edits: deletes run before moves, then downloads, then exposure changes, failures are collected, then a single save.
```

- [ ] **Step 3: Lint + typecheck + full tests**

Run: `make check && uv run pytest -q`
Expected: no lint/type errors; full test suite passes.

Fix anything `ruff`/`mypy` flag in the touched files (typical candidates: unused imports in tests, the `NamedTuple` import in forms.py, `_options` private-access nags — silence only with a targeted `# type: ignore[comment]`/noqa if the tooling genuinely complains, never bypass broadly).

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md README.md
git commit -m "docs: document queued family moves (edit-form selector, apply ordering)"
```

---

## Summary of file changes

| File | Change |
|---|---|
| `src/modelman/queue.py` | `moves` field, early-return guard, delete→move→download ordering, `move:*` tags |
| `src/modelman/screens/status.py` | render `move:start/done/fail` |
| `src/modelman/screens/forms.py` | `ModelFormResult`, family `Select` (with `families`/`family` args + prepend guard), `ConfirmExitDialog.moves` |
| `src/modelman/screens/models.py` | `queued_moves`, `_added_ids`, `_families_list()`, form invocations, callbacks unpack `(spec, family)`, `→` glyph, pending bar, exit wiring, id-keyed `_restore_snapshot` |
| `tests/test_queue.py` | 3 new tests |
| `tests/screens/test_status.py` | 1 new test |
| `tests/screens/test_forms.py` | 6 unpack updates + 5 new tests |
| `tests/screens/test_app_navigation.py` | `_make_screen` families kwarg + 6 new tests |
| `CLAUDE.md`, `README.md` | docs |

## Self-review checklist (run before declaring done)

1. **Spec coverage:** moves queued not saved immediately (Task 4 + 5); add-or-edit picker (Task 3/4); existing-families-only list incl. state-only families (Task 4 `_families_list`); discard safety (Task 5); StatusScreen tags (Task 2); no bullets from the spec left unimplemented.
2. **Type consistency:** `ModelFormResult(spec: VariantSpec, family: str)` used identically in forms.py and models.py; `PendingChanges.moves: list[tuple[str, str]]` matches `queued_moves.items()`; event tag order `move:<status>|<id>|<label>|<payload>` matches the StatusScreen parser.
3. **Behavioral gotchas verified by tests:** re-edit preselect honors queued move's target (Task 4 Step 2, first test asserts `sel.value` in two tests); move-back = queue drop (Task 4 test 2); out-of-family add revert (Task 5 test 3).

## How the user runs the fix afterwards

`git checkout feat/modelman-family-move && uv run modelman` → family screen `a` → create `gemma4` → for each stale family (`gemma4:26b-mlx`, `gemma4:12b-mlx`, `gemma4:31b-mlx`, `gemma4:cloud`) open it, `e` on the model, pick `gemma4`, Save, escape, apply. The emptied stale families disappear from the list automatically since they were never in `state.families`.