# Family Screen DOWNLOADED Column Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rename the modelman family screen's READY column to DOWNLOADED and make it count only local (non-cloud) models, so cloud-only families no longer show a phantom on-disk count.

**Architecture:** Two surgical changes to `FamilyScreen` — a column-header rename in `on_mount()` and a location guard at the top of the per-model counting loop in `reload()`. No reconcile-worker, state, registry, or CLI changes. Spec: `docs/superpowers/specs/2026-09-01-family-downloaded-column-design.md`.

**Tech Stack:** Python 3, Textual TUI (DataTable), pytest + pytest-asyncio, uv, ruff.

**Branch:** `feat/family-downloaded-column` (already checked out; spec committed).

**Worktree:** not needed — the feature branch is the isolation boundary for this 2-file change.

---

### Task 1: Rename READY → DOWNLOADED header

**Files:**
- Modify: `modelman/src/modelman/screens/families.py:95` (one string)
- Test: `modelman/tests/screens/test_families.py`

- [ ] **Step 1: Write the failing test**

Append to `modelman/tests/screens/test_families.py` (after the existing imports; all imports needed already exist in the file):

```python
@pytest.mark.asyncio
async def test_family_screen_column_headers(tmp_path, monkeypatch):
    """The family table's fourth column is DOWNLOADED (count of local
    models on disk), not READY."""
    _seed(tmp_path, monkeypatch)

    from modelman.providers import registry as prov_registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.is_downloaded.return_value = False
    stub.size_of.return_value = None
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        table = app.screen.query_one("#family-table", DataTable)
        labels = [str(col.label) for col in table.columns.values()]
        assert labels == ["FAMILY", "DISPLAY", "VARIANTS", "DOWNLOADED", "SIZE"]
```

- [ ] **Step 2: Run test to verify it fails**

Run (from `modelman/`): `uv run pytest tests/screens/test_families.py::test_family_screen_column_headers -v`
Expected: FAIL — list differs, actual contains `"READY"` at index 3.

- [ ] **Step 3: Rename the header**

In `modelman/src/modelman/screens/families.py` line 95, change:

```python
        table.add_columns("FAMILY", "DISPLAY", "VARIANTS", "READY", "SIZE")
```

to:

```python
        table.add_columns("FAMILY", "DISPLAY", "VARIANTS", "DOWNLOADED", "SIZE")
```

- [ ] **Step 4: Run test to verify it passes**

Run (from `modelman/`): `uv run pytest tests/screens/test_families.py -v`
Expected: all 6 tests PASS (the 5 pre-existing + the new one).

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/screens/families.py modelman/tests/screens/test_families.py
git commit -m "feat(modelman): rename family screen READY column to DOWNLOADED"
```

---

### Task 2: Count only local models (skip cloud entries)

**Files:**
- Modify: `modelman/src/modelman/screens/families.py` (`reload()` loop, line ~262)
- Test: `modelman/tests/screens/test_families.py`

- [ ] **Step 1: Write the failing test**

Append to `modelman/tests/screens/test_families.py`. This test is self-contained (does not reuse `_seed`) because it needs a registry with a cloud-location model — `location` defaults to `None`, which must keep counting:

```python
@pytest.mark.asyncio
async def test_family_screen_downloaded_excludes_cloud(tmp_path, monkeypatch):
    """DOWNLOADED counts only local models. A cloud entry (location='cloud')
    holds no disk weights and must not inflate the count, even when the
    provider reports it as downloaded — `ollama show <model>:cloud` exits 0."""
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none"))],
        families=[FamilyEntry(name="alpha")],
        models=[
            ModelEntry(
                id="ollama/cloudy",
                family="alpha",
                provider_id="ollama",
                model_name="cloudy:cloud",
                location="cloud",
            ),
            ModelEntry(id="ollama/local-a", family="alpha", provider_id="ollama", model_name="a"),
        ],
    )
    save_registry(reg, reg_path)
    state = StateStore()
    state.set("ollama/local-a", ModelState(ready=True, disk_path="/tmp/a"))
    save_state(state, state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    from modelman.providers import registry as prov_registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.is_downloaded.return_value = True  # cloud tags resolve too
    stub.size_of.return_value = None
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        # Let the initial reconcile complete so reload() uses provider truth.
        for _ in range(50):
            await pilot.pause()
            if not app.screen._reconciling:
                break
        table = app.screen.query_one("#family-table", DataTable)
        # Row key is the family name (key=family in add_row); cells are
        # FAMILY, DISPLAY, VARIANTS, DOWNLOADED, SIZE.
        row_key = next(k for k in table.rows.keys() if k.value == "alpha")
        assert str(table.get_row(row_key)[3]) == "1"
```

- [ ] **Step 2: Run test to verify it fails**

Run (from `modelman/`): `uv run pytest tests/screens/test_families.py::test_family_screen_downloaded_excludes_cloud -v`
Expected: FAIL — `assert '2' == '1'` (the cloud model is counted via its reconcile record).

- [ ] **Step 3: Skip cloud models in the counting loop**

In `modelman/src/modelman/screens/families.py`, inside `reload()`'s `_repopulate`, add a guard at the top of the per-model loop. Change:

```python
                for m in models:
                    rec = self._reconciled.get(m.id)
```

to:

```python
                for m in models:
                    # Cloud entries hold no disk weights and cannot be
                    # duplicates — never count them toward DOWNLOADED/SIZE.
                    # Entries with location=None (legacy) still count.
                    if m.location == "cloud":
                        continue
                    rec = self._reconciled.get(m.id)
```

The guard sits before both the reconcile-record branch and the state-fallback `elif self.state.get(m.id).ready`, so neither path can count a cloud model.

- [ ] **Step 4: Run test to verify it passes**

Run (from `modelman/`): `uv run pytest tests/screens/test_families.py -v`
Expected: all 7 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/screens/families.py modelman/tests/screens/test_families.py
git commit -m "feat(modelman): exclude cloud models from family DOWNLOADED count"
```

---

### Task 3: Full verification + PR

**Files:** none (verification only)

- [ ] **Step 1: Run the full modelman test suite**

Run (from `modelman/`): `uv run pytest`
Expected: all tests PASS (no regressions outside the family screen).

- [ ] **Step 2: Run lint + typecheck (CI parity)**

Run (from `modelman/`): `make check`
Expected: ruff and typecheck both clean.

- [ ] **Step 3: Smoke test the TUI manually**

Run (from `modelman/`): `uv run modelman`
Verify visually: the fourth column header reads DOWNLOADED; the glm-5.3 row (cloud-only family) shows `0` and `—` for size; ornith-1.5 shows its local download count.

- [ ] **Step 4: Push and open the PR**

```bash
git push -u origin feat/family-downloaded-column
gh pr create --title "feat(modelman): family screen DOWNLOADED column (local models only)" --body "## Summary
- Renames the family screen's READY column to DOWNLOADED — it counts models downloaded to disk
- Excludes cloud entries (\`location == \"cloud\"\`) from the count: \`ollama show <model>:cloud\` exits 0, so cloud-only families like glm-5.3 previously showed READY=1 despite nothing on disk
- Models with no location set (legacy) still count; ModelScreen's per-model STATUS is untouched

## Spec
docs/superpowers/specs/2026-09-01-family-downloaded-column-design.md

Rationale: different sizes within a family (ornith-1.5 35b vs 9b) are legitimately kept side by side, so a per-family count is a triage signal, not copy-level duplicate detection."
```

Expected: PR URL printed. After merge, run the repo cleanup workflow (restore + tidy) as usual.