# modelman TUI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the questionary-based `download` flow with a Textual TUI for managing model families and the models within them, with changes queued until exit (deletes before downloads).

**Architecture:** A Textual `App` with screen-per-view navigation (`FamilyScreen` → `ModelScreen`) and a `PendingChanges` queue module that applies edits on exit. Capability fields live on each variant as a freeform `model_info` dict, auto-detected for Ollama. Providers expose a `size_of` method for the size columns.

**Tech Stack:** Python 3.13, Typer (entry), Rich, Textual, pytest, pytest-asyncio (TUI tests), mypy.

**Spec:** `docs/superpowers/specs/2026-08-26-modelman-tui-design.md`

**Working branch:** `feat/tui`

---

## File structure

**New files**
- `src/modelman/app.py` — Textual `ModelmanApp`, screen routing, keybindings (`BINDINGS`), top-level CSS
- `src/modelman/queue.py` — `PendingChanges` dataclass + `apply()` (deletes before downloads)
- `src/modelman/ollama_caps.py` — small helper to parse `ollama show` output into `model_info`
- `src/modelman/screens/__init__.py`
- `src/modelman/screens/families.py` — `FamilyScreen`
- `src/modelman/screens/models.py` — `ModelScreen`
- `src/modelman/screens/forms.py` — `AddFamilyModal`, `ModelForm` (add/edit), `ConfirmExitDialog`
- `tests/test_queue.py`
- `tests/test_ollama_caps.py`
- `tests/screens/__init__.py`
- `tests/screens/test_app_navigation.py`

**Modified files**
- `pyproject.toml` — add `textual` dep; add `pytest-asyncio` to dev; add asyncio mode
- `src/modelman/manifest.py` — `VariantSpec` gains optional `model_info`; pass through `_coerce_variant` / `save_manifest`
- `src/modelman/providers/base.py` — add `size_of(variant) -> int | None` (default `None`)
- `src/modelman/providers/ollama.py` — implement `size_of` (parse `ollama list` SIZE column); accept runner for show
- `src/modelman/providers/llamacpp.py` — implement `size_of`
- `src/modelman/providers/omlx.py` — implement `size_of`
- `src/modelman/main.py` — `modelman` (no args) launches the TUI; `download <family>` launches at that family's model screen. Drop `--all`/`-y`.
- `src/modelman/commands/download.py` — becomes a thin shim that calls into the TUI launcher

**Removed / unused**
- `questionary` import in `download.py` (the picker is gone). Keep `questionary` in `pyproject.toml` for now (YAGNI: don't remove a dep that's harmless).

---

## Task 1: Add dependencies

**Files:**
- Modify: `pyproject.toml`

- [ ] **Step 1: Add `textual` and `pytest-asyncio`**

Edit `pyproject.toml`:

```toml
dependencies = [
    "typer>=0.15.3",
    "rich>=14.0.0",
    "pyyaml>=6.0.2",
    "huggingface_hub>=1.0.0",
    "questionary>=2.0.0",
    "textual>=0.80.0",
]

[dependency-groups]
dev = [
    "pytest>=8.0.0",
    "pytest-mock>=3.14.0",
    "pytest-cov>=5.0.0",
    "mypy>=1.10.0",
    "types-pyyaml>=6.0.12.20260815",
    "pytest-asyncio>=0.24.0",
]

[tool.pytest.ini_options]
testpaths = ["tests"]
addopts = "-v --tb=short"
asyncio_mode = "auto"
```

- [ ] **Step 2: Sync and verify**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv sync
uv run python -c "import textual; print(textual.__version__)"
```

Expected: a version number prints (e.g. `0.80.0` or higher).

- [ ] **Step 3: Verify existing tests still pass**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest
```

Expected: all existing tests pass (no regressions from new deps).

- [ ] **Step 4: Commit**

```bash
cd /Users/keith/github/ohanaverse/modelman
git add pyproject.toml uv.lock
git commit -m "chore: add textual + pytest-asyncio deps"
```

---

## Task 2: Add `model_info` to VariantSpec

**Files:**
- Modify: `src/modelman/providers/base.py`
- Modify: `src/modelman/manifest.py`
- Modify: `tests/fixtures/sample_family.yaml` (add a `model_info` to one variant)
- Test: `tests/test_manifest.py` (append tests)

- [ ] **Step 1: Append a failing test for round-trip of `model_info`**

Append to `tests/test_manifest.py`:

```python
def test_model_info_round_trip(tmp_path):
    family_file = tmp_path / "ornith-1.5.yaml"
    family_file.write_text(Path("tests/fixtures/sample_family.yaml").read_text())
    manifest = load_manifest("ornith-1.5", family_dir=tmp_path)

    manifest.variants[0]["model_info"] = {  # type: ignore[typeddict-unknown-key]
        "supports_function_calling": True,
        "mode": "chat",
    }
    save_manifest(manifest, family_file)

    reloaded = load_manifest("ornith-1.5", family_dir=tmp_path)
    assert reloaded.variants[0]["model_info"] == {  # type: ignore[typeddict-unknown-key]
        "supports_function_calling": True,
        "mode": "chat",
    }
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/test_manifest.py::test_model_info_round_trip -v
```

Expected: FAIL with a `TypeError` or `KeyError` related to `model_info` (since the TypedDict doesn't have it and `_coerce_variant` strips unknown keys... actually it doesn't strip; the issue is the TypedDict type check and serialization).

- [ ] **Step 3: Add `model_info` to `VariantSpec`**

In `src/modelman/providers/base.py`, change `VariantSpec`:

```python
from typing import TypedDict


class VariantSpec(TypedDict, total=False):
    """A single model variant within a family manifest. All fields optional
    in the TypedDict sense, but providers require specific ones at runtime."""
    id: str                           # stable id within the family
    provider: str                     # "ollama" | "llamacpp" | "omlx"
    name: str                         # provider-specific (e.g. "ornith-1.5:35b" for ollama)
    repo: str | None                  # HF repo id (for llamacpp/omlx)
    files: list[str] | None           # files in repo (for llamacpp)
    quantizations: list[str] | None   # quant tags (for omlx)
    model_info: dict | None           # freeform LiteLLM model_info keys


class LocalModel(TypedDict):
    """A model that exists on the local machine."""
    variant_id: str
    path: str
    size_bytes: int | None
```

(`total=False` keeps the existing permissive loading behavior; `id` and `provider` are still required at runtime via `_coerce_variant`.)

- [ ] **Step 4: Ensure `_coerce_variant` and `save_manifest` pass `model_info` through**

In `src/modelman/manifest.py`, update `_coerce_variant`:

```python
    return VariantSpec(
        id=raw["id"],
        provider=raw["provider"],
        name=name,
        repo=raw.get("repo"),
        files=raw.get("files"),
        quantizations=raw.get("quantizations"),
        model_info=raw.get("model_info"),
    )
```

`save_manifest` already serializes the whole dict via `[dict(v) for v in manifest.variants]`, so `model_info` will be included automatically. No change needed there.

- [ ] **Step 5: Run the test to verify it passes**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/test_manifest.py -v
```

Expected: PASS for all manifest tests including the new one.

- [ ] **Step 6: Run mypy**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run mypy src/
```

Expected: no new errors. (The `# type: ignore[typeddict-unknown-key]` comments in the test will silence any residual complaints; if mypy is happy without them, drop the ignores.)

- [ ] **Step 7: Commit**

```bash
cd /Users/keith/github/ohanaverse/modelman
git add src/modelman/providers/base.py src/modelman/manifest.py tests/test_manifest.py
git commit -m "feat(manifest): freeform model_info per variant"
```

---

## Task 3: Add `size_of` to Provider base

**Files:**
- Modify: `src/modelman/providers/base.py`
- Test: `tests/test_providers/test_base.py`

- [ ] **Step 1: Append a failing test**

Append to `tests/test_providers/test_base.py`:

```python
def test_provider_size_of_default_is_none():
    from modelman.providers.base import Provider
    from modelman.providers.ollama import OllamaProvider

    # Default base impl returns None
    class _Stub(Provider):
        name = "stub"
        def is_downloaded(self, variant): return False
        def download(self, variant): return ""
        def list_local(self): return []

    assert _Stub({}).size_of({"id": "x", "provider": "stub", "name": "x"}) is None
    # OllamaProvider overrides; ensure method exists
    assert hasattr(OllamaProvider({}), "size_of")
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/test_providers/test_base.py::test_provider_size_of_default_is_none -v
```

Expected: FAIL — `Provider` has no `size_of`.

- [ ] **Step 3: Add `size_of` to `Provider`**

In `src/modelman/providers/base.py`, add to the `Provider` class:

```python
    def size_of(self, variant: "VariantSpec") -> int | None:
        """Return the on-disk size in bytes for this variant, or None if unknown.

        Providers override this when they can determine a size. Default is None
        so unknown providers don't crash the size columns.
        """
        return None
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/test_providers/test_base.py -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/keith/github/ohanaverse/modelman
git add src/modelman/providers/base.py tests/test_providers/test_base.py
git commit -m "feat(providers): add size_of with default None"
```

---

## Task 4: Implement `size_of` for llamacpp

**Files:**
- Modify: `src/modelman/providers/llamacpp.py`
- Test: `tests/test_providers/test_llamacpp.py`

- [ ] **Step 1: Append a failing test**

Append to `tests/test_providers/test_llamacpp.py`:

```python
def test_size_of_stats_primary_file(tmp_path, monkeypatch):
    from modelman.providers.llamacpp import LlamaCppProvider

    # Lay down a fake HF cache: models--org--name/snapshots/<rev>/file.gguf
    hf = tmp_path / "hf"
    snap = hf / "models--ornith--test" / "snapshots" / "rev1"
    snap.mkdir(parents=True)
    f = snap / "model.gguf"
    f.write_bytes(b"x" * 100)

    monkeypatch.setenv("HF_HOME", str(hf))
    p = LlamaCppProvider({})
    size = p.size_of({
        "id": "x", "provider": "llamacpp", "name": "x",
        "repo": "ornith/test", "files": ["model.gguf"],
    })
    assert size == 100


def test_size_of_returns_none_when_not_in_cache(tmp_path, monkeypatch):
    from modelman.providers.llamacpp import LlamaCppProvider

    monkeypatch.setenv("HF_HOME", str(tmp_path / "empty"))
    p = LlamaCppProvider({})
    assert p.size_of({
        "id": "x", "provider": "llamacpp", "name": "x",
        "repo": "ornith/missing", "files": ["model.gguf"],
    }) is None
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/test_providers/test_llamacpp.py -v -k "size_of"
```

Expected: FAIL.

- [ ] **Step 3: Implement `size_of` in `LlamaCppProvider`**

Add to `src/modelman/providers/llamacpp.py`, inside `LlamaCppProvider`:

```python
    def size_of(self, variant: VariantSpec) -> int | None:
        repo = variant.get("repo")
        files = variant.get("files")
        if not repo or not files:
            return None
        hf_org, hf_name = repo.split("/", 1)
        repo_dir = _hf_cache_dir() / f"models--{hf_org}--{hf_name}" / "snapshots"
        if not repo_dir.exists():
            return None
        primary = files[0]
        for snap in repo_dir.iterdir():
            candidate = snap / primary
            if candidate.exists():
                return candidate.stat().st_size
        return None
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/test_providers/test_llamacpp.py -v -k "size_of"
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/keith/github/ohanaverse/modelman
git add src/modelman/providers/llamacpp.py tests/test_providers/test_llamacpp.py
git commit -m "feat(llamacpp): size_of stats primary file in HF cache"
```

---

## Task 5: Implement `size_of` for omlx

**Files:**
- Modify: `src/modelman/providers/omlx.py`
- Test: `tests/test_providers/test_omlx.py`

- [ ] **Step 1: Append a failing test**

Append to `tests/test_providers/test_omlx.py`:

```python
def test_size_of_sums_model_dir(tmp_path):
    from modelman.providers.omlx import OMLXProvider

    md = tmp_path / "models"
    target = md / "Ornith-1.5"
    target.mkdir(parents=True)
    (target / "a.safetensors").write_bytes(b"a" * 50)
    (target / "b.safetensors").write_bytes(b"b" * 30)

    p = OMLXProvider({"model_dir": str(md)})
    size = p.size_of({
        "id": "x", "provider": "omlx", "name": "x",
        "repo": "ornith/Ornith-1.5", "files": None,
    })
    assert size == 80


def test_size_of_returns_none_when_missing(tmp_path):
    from modelman.providers.omlx import OMLXProvider

    p = OMLXProvider({"model_dir": str(tmp_path / "models")})
    assert p.size_of({
        "id": "x", "provider": "omlx", "name": "x",
        "repo": "ornith/missing", "files": None,
    }) is None
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/test_providers/test_omlx.py -v -k "size_of"
```

Expected: FAIL.

- [ ] **Step 3: Implement `size_of` in `OMLXProvider`**

Add to `src/modelman/providers/omlx.py`, inside `OMLXProvider`:

```python
    def size_of(self, variant: VariantSpec) -> int | None:
        repo = variant.get("repo")
        if not repo:
            return None
        target = _model_dir(self.config) / _basename(repo)
        if not target.is_dir():
            return None
        total = 0
        for f in target.rglob("*"):
            if f.is_file():
                total += f.stat().st_size
        return total or None
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/test_providers/test_omlx.py -v -k "size_of"
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/keith/github/ohanaverse/modelman
git add src/modelman/providers/omlx.py tests/test_providers/test_omlx.py
git commit -m "feat(omlx): size_of sums model dir"
```

---

## Task 6: Implement `size_of` for ollama (parses `ollama list` SIZE)

**Files:**
- Modify: `src/modelman/providers/ollama.py`
- Test: `tests/test_providers/test_ollama.py`

- [ ] **Step 1: Append a failing test**

Append to `tests/test_providers/test_ollama.py`:

```python
def test_size_of_parses_ollama_list(mock_runner):
    from modelman.providers.ollama import OllamaProvider

    stdout = (
        "NAME                       ID           SIZE      MODIFIED\n"
        "ornith-1.5:35b             abc123       21 GB     2 days ago\n"
        "ornith-1.5:8b              def456       5.2 GB    3 days ago\n"
    )
    runner = mock_runner(returncode=0, stdout=stdout)
    p = OllamaProvider({})

    assert p.size_of(
        {"id": "x", "provider": "ollama", "name": "ornith-1.5:35b"},
        runner=runner,
    ) == 21 * 1024 ** 3
    assert p.size_of(
        {"id": "x", "provider": "ollama", "name": "ornith-1.5:8b"},
        runner=runner,
    ) == int(5.2 * 1024 ** 3)


def test_size_of_returns_none_when_not_in_list(mock_runner):
    from modelman.providers.ollama import OllamaProvider

    runner = mock_runner(returncode=0, stdout="NAME ID SIZE MODIFIED\n")
    p = OllamaProvider({})
    assert p.size_of(
        {"id": "x", "provider": "ollama", "name": "missing:tag"},
        runner=runner,
    ) is None
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/test_providers/test_ollama.py -v -k "size_of"
```

Expected: FAIL — `OllamaProvider.size_of` doesn't exist yet.

- [ ] **Step 3: Implement `size_of` in `OllamaProvider`**

In `src/modelman/providers/ollama.py`, add a parser and the method. First, add a module-level helper near the top (after `_default_runner`):

```python
def _parse_ollama_list_sizes(stdout: str) -> dict[str, int]:
    """Parse `ollama list` output into {model_name: size_bytes}.

    SIZE column is human-readable (e.g. '21 GB', '5.2 GB', '742.35 MB').
    """
    sizes: dict[str, int] = {}
    units = {"B": 1, "KB": 1024, "MB": 1024**2, "GB": 1024**3, "TB": 1024**4}
    for line in stdout.splitlines():
        line = line.strip()
        if not line or line.startswith("NAME"):
            continue
        parts = line.split()
        if len(parts) < 3:
            continue
        name = parts[0]
        size_str = parts[2]
        try:
            num, unit = size_str.split()
        except ValueError:
            continue
        try:
            value = float(num)
        except ValueError:
            continue
        multiplier = units.get(unit.upper())
        if multiplier is None:
            continue
        sizes[name] = int(value * multiplier)
    return sizes
```

Then add to `OllamaProvider`:

```python
    def size_of(self, variant: VariantSpec, runner: _Runner | None = None) -> int | None:
        r = (runner or _default_runner)(["ollama", "list"], capture_output=True, text=True)
        if r.returncode != 0:
            return None
        sizes = _parse_ollama_list_sizes(r.stdout)
        return sizes.get(variant["name"])
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/test_providers/test_ollama.py -v -k "size_of"
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/keith/github/ohanaverse/modelman
git add src/modelman/providers/ollama.py tests/test_providers/test_ollama.py
git commit -m "feat(ollama): size_of parses ollama list SIZE column"
```

---

## Task 7: `PendingChanges` apply() — deletes before downloads

**Files:**
- Create: `src/modelman/queue.py`
- Test: `tests/test_queue.py`

- [ ] **Step 1: Write the failing test**

Create `tests/test_queue.py`:

```python
import pytest
from pathlib import Path
from unittest.mock import MagicMock

from modelman.manifest import FamilyManifest
from modelman.queue import PendingChanges


def _manifest_with_downloads(tmp_path):
    fam_path = tmp_path / "fam.yaml"
    m = FamilyManifest(
        family="f",
        display_name="F",
        variants=[
            {"id": "a", "provider": "ollama", "name": "f:a"},
            {"id": "b", "provider": "llamacpp", "name": "f:b",
             "repo": "org/repo", "files": ["x.gguf"]},
        ],
    )
    m.mark_downloaded("a", str(tmp_path / "downloaded-a"))
    Path(m.downloaded["a"]["local_path"]).mkdir()
    m.mark_downloaded("b", str(tmp_path / "downloaded-b"))
    Path(m.downloaded["b"]["local_path"]).write_bytes(b"old")
    return m, fam_path


def test_apply_deletes_before_downloads(tmp_path):
    """On apply, delete steps must run before download steps (free disk first)."""
    m, fam_path = _manifest_with_downloads(tmp_path)
    order: list[str] = []

    provider_ollama = MagicMock()
    provider_ollama.download.return_value = str(tmp_path / "new-a")
    provider_ollama.delete.return_value = None
    provider_ollama.name = "ollama"

    provider_llama = MagicMock()
    provider_llama.download.return_value = str(tmp_path / "new-b")
    provider_llama.delete.return_value = None
    provider_llama.name = "llamacpp"

    # Provider.delete should be called on 'a' before Provider.download is called on 'a'.
    def track_delete(variant):
        order.append(f"delete:{variant['id']}")
    def track_download(variant):
        order.append(f"download:{variant['id']}")
        return f"/tmp/new-{variant['id']}"
    provider_ollama.delete.side_effect = track_delete
    provider_ollama.download.side_effect = track_download
    provider_llama.delete.side_effect = track_delete
    provider_llama.download.side_effect = track_download

    pending = PendingChanges(
        manifest=m,
        manifest_path=fam_path,
        providers={"ollama": provider_ollama, "llamacpp": provider_llama},
        deletes=[m.variants[0]],       # delete variant 'a'
        downloads=[m.variants[1]],     # re-download variant 'b'
    )
    pending.apply()

    # 'a' delete happened before 'b' download
    assert order.index("delete:a") < order.index("download:b")
    # Manifest no longer references 'a'
    assert "a" not in m.downloaded
    assert "b" in m.downloaded
    # Manifest was saved
    assert fam_path.exists()


def test_apply_collects_failures(tmp_path):
    m, fam_path = _manifest_with_downloads(tmp_path)
    provider = MagicMock()
    provider.name = "ollama"
    provider.delete.return_value = None
    provider.download.side_effect = RuntimeError("network down")

    pending = PendingChanges(
        manifest=m,
        manifest_path=fam_path,
        providers={"ollama": provider},
        downloads=[m.variants[0]],
    )
    pending.apply()

    # Manifest saved even though download failed
    assert fam_path.exists()
    assert pending.failures  # populated
    assert "network down" in str(pending.failures[0])


def test_apply_empty_is_noop(tmp_path):
    m, fam_path = _manifest_with_downloads(tmp_path)
    pending = PendingChanges(manifest=m, manifest_path=fam_path, providers={})
    pending.apply()
    assert not fam_path.exists()  # nothing to do, don't rewrite the file
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/test_queue.py -v
```

Expected: FAIL — `modelman.queue` doesn't exist.

- [ ] **Step 3: Implement `PendingChanges`**

Create `src/modelman/queue.py`:

```python
"""In-memory change queue applied on exit of the TUI model screen."""
from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path
from typing import TYPE_CHECKING

from .manifest import save_manifest

if TYPE_CHECKING:
    from .manifest import FamilyManifest
    from .providers.base import VariantSpec


@dataclass
class PendingChanges:
    manifest: "FamilyManifest"
    manifest_path: Path
    providers: dict[str, object]  # provider_name -> Provider-like (has download + delete)
    downloads: list["VariantSpec"] = field(default_factory=list)
    deletes: list["VariantSpec"] = field(default_factory=list)
    failures: list[str] = field(default_factory=list)

    def apply(self) -> None:
        """Apply deletes first, then downloads, then save the manifest once.

        On failure of any single step, capture it in self.failures and continue
        with the remaining steps.
        """
        if not self.downloads and not self.deletes:
            return

        for variant in self.deletes:
            try:
                self._delete(variant)
            except Exception as exc:  # noqa: BLE001
                self.failures.append(f"delete {variant['id']}: {exc}")
                continue
            self.manifest.variants = [
                v for v in self.manifest.variants if v["id"] != variant["id"]
            ]
            self.manifest.downloaded.pop(variant["id"], None)

        for variant in self.downloads:
            try:
                local_path = self._download(variant)
            except Exception as exc:  # noqa: BLE001
                self.failures.append(f"download {variant['id']}: {exc}")
                continue
            self.manifest.mark_downloaded(variant["id"], local_path)

        save_manifest(self.manifest, self.manifest_path)

    def _download(self, variant: "VariantSpec") -> str:
        provider = self.providers[variant["provider"]]
        return provider.download(variant)  # type: ignore[attr-defined]

    def _delete(self, variant: "VariantSpec") -> None:
        provider = self.providers[variant["provider"]]
        if hasattr(provider, "delete"):
            provider.delete(variant)  # type: ignore[attr-defined]
            return
        # Fallback: best-effort filesystem cleanup for HF-based providers.
        local_path = self.manifest.downloaded.get(variant["id"], {}).get("local_path")
        if local_path:
            from pathlib import Path as _P
            p = _P(local_path)
            if p.is_file():
                p.unlink()
            elif p.is_dir():
                import shutil
                shutil.rmtree(p)
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/test_queue.py -v
```

Expected: PASS for all three tests.

- [ ] **Step 5: Commit**

```bash
cd /Users/keith/github/ohanaverse/modelman
git add src/modelman/queue.py tests/test_queue.py
git commit -m "feat(queue): PendingChanges applies deletes before downloads"
```

---

## Task 8: Ollama capability auto-detect (parse `ollama show`)

**Files:**
- Create: `src/modelman/ollama_caps.py`
- Test: `tests/test_ollama_caps.py`

- [ ] **Step 1: Write the failing test**

Create `tests/test_ollama_caps.py`:

```python
from modelman.ollama_caps import parse_ollama_show, auto_detect_model_info


def test_parse_capabilities_tools_to_function_calling():
    stdout = (
        "Model\n"
        "  architecture    qwen2\n"
        "  parameters      8.2B\n"
        "  context length  32768\n"
        "  embedding length 4096\n"
        "Capabilities\n"
        "    completion\n"
        "    tools\n"
    )
    info = parse_ollama_show(stdout)
    assert info == {"supports_function_calling": True}


def test_parse_no_capabilities_section():
    stdout = "Model\n  architecture llama\n"
    assert parse_ollama_show(stdout) == {}


def test_auto_detect_runs_runner_and_returns_info(mock_runner):
    runner = mock_runner(
        returncode=0,
        stdout="Capabilities\n    tools\n    vision\n",
    )
    info = auto_detect_model_info("foo:1b", runner=runner)
    runner.assert_called_with(["ollama", "show", "foo:1b"], capture_output=True, text=True)
    assert info.get("supports_function_calling") is True


def test_auto_detect_returns_empty_on_failure(mock_runner):
    runner = mock_runner(returncode=1, stdout="", stderr="not found")
    assert auto_detect_model_info("missing:tag", runner=runner) == {}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/test_ollama_caps.py -v
```

Expected: FAIL — module not found.

- [ ] **Step 3: Implement the parser**

Create `src/modelman/ollama_caps.py`:

```python
"""Parse `ollama show` output for LiteLLM-compatible model_info fields."""
from __future__ import annotations

import subprocess
from typing import Any, Callable, Protocol


class _Runner(Protocol):
    def __call__(self, *args: Any, **kwargs: Any) -> Any: ...


# Map of ollama capability strings to LiteLLM model_info keys we currently populate.
_CAPABILITY_MAP = {
    "tools": "supports_function_calling",
    "vision": "supports_vision",
}


def parse_ollama_show(stdout: str) -> dict[str, Any]:
    """Extract a model_info dict from `ollama show <model>` text output.

    Only populates keys we know how to map today; everything else is ignored.
    """
    info: dict[str, Any] = {}
    in_caps = False
    for line in stdout.splitlines():
        stripped = line.strip()
        if not stripped:
            in_caps = False
            continue
        if stripped.lower() == "capabilities":
            in_caps = True
            continue
        if not in_caps:
            continue
        key = _CAPABILITY_MAP.get(stripped)
        if key and key not in info:
            info[key] = True
    return info


def _default_runner(args: list[str], **kwargs: Any):
    return subprocess.run(args, **kwargs)


def auto_detect_model_info(name: str, runner: _Runner | None = None) -> dict[str, Any]:
    """Run `ollama show <name>` and return its parsed model_info. {} on failure."""
    r = (runner or _default_runner)(
        ["ollama", "show", name], capture_output=True, text=True
    )
    if r.returncode != 0:
        return {}
    return parse_ollama_show(r.stdout)
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/test_ollama_caps.py -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/keith/github/ohanaverse/modelman
git add src/modelman/ollama_caps.py tests/test_ollama_caps.py
git commit -m "feat(ollama): parse ollama show capabilities into model_info"
```

---

## Task 9: Minimal Textual app that launches

**Files:**
- Create: `src/modelman/app.py`
- Create: `src/modelman/screens/__init__.py`
- Create: `src/modelman/screens/families.py` (placeholder content)
- Test: `tests/screens/test_app_navigation.py`

- [ ] **Step 1: Create empty screens package and a placeholder FamilyScreen**

Create `src/modelman/screens/__init__.py` (empty file).

Create `src/modelman/screens/families.py`:

```python
"""FamilyScreen — default view listing all configured families."""
from __future__ import annotations

from textual.screen import Screen


class FamilyScreen(Screen[None]):
    pass
```

- [ ] **Step 2: Write a smoke test that the app launches and shows the family screen**

Create `tests/screens/__init__.py` (empty). Create `tests/screens/test_app_navigation.py`:

```python
import pytest
from modelman.app import ModelmanApp


@pytest.mark.asyncio
async def test_app_launches_into_family_screen():
    app = ModelmanApp()
    async with app.run_test() as pilot:
        # App started; we are on FamilyScreen.
        from modelman.screens.families import FamilyScreen
        assert isinstance(app.screen, FamilyScreen)
```

- [ ] **Step 3: Run test to verify it fails**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/screens/test_app_navigation.py -v
```

Expected: FAIL — `modelman.app` doesn't exist.

- [ ] **Step 4: Implement minimal `ModelmanApp`**

Create `src/modelman/app.py`:

```python
"""Textual application root for modelman."""
from __future__ import annotations

from textual.app import App

from .screens.families import FamilyScreen


class ModelmanApp(App[None]):
    TITLE = "modelman"

    def on_mount(self) -> None:
        self.push_screen(FamilyScreen())
```

- [ ] **Step 5: Run test to verify it passes**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/screens/test_app_navigation.py -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /Users/keith/github/ohanaverse/modelman
git add src/modelman/app.py src/modelman/screens/ tests/screens/
git commit -m "feat(tui): minimal ModelmanApp launching FamilyScreen"
```

---

## Task 10: FamilyScreen renders families with size column

**Files:**
- Modify: `src/modelman/screens/families.py`
- Modify: `src/modelman/app.py`
- Modify: `tests/screens/test_app_navigation.py`

- [ ] **Step 1: Write the failing test for family listing**

Append to `tests/screens/test_app_navigation.py`:

```python
@pytest.mark.asyncio
async def test_family_screen_lists_configured_families(tmp_path, monkeypatch):
    from modelman.manifest import FamilyManifest, save_manifest
    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    m = FamilyManifest(
        family="ornith", display_name="Ornith",
        variants=[{"id": "a", "provider": "ollama", "name": "o:35b"}],
    )
    m.mark_downloaded("a", str(tmp_path / "downloaded-a"))
    save_manifest(m, fam_dir / "ornith.yaml")

    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text(
        "providers:\n  ollama:\n    type: ollama\n"
    )

    from modelman.app import ModelmanApp
    app = ModelmanApp()
    async with app.run_test() as pilot:
        # Wait for the screen to mount and load.
        await pilot.pause()
        table = app.screen.query_one("DataTable")
        assert table.row_count == 1
        # Family name and display appear in the rendered rows.
        rendered = " ".join(str(c) for c in table.coordinate_to_cell_key)
        assert "ornith" in str(table.render())
```

(Use `await pilot.pause()` and inspect `table.render()` / rows directly; if exact assertions are noisy, assert `row_count == 1` only and visually confirm in a manual run.)

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/screens/test_app_navigation.py::test_family_screen_lists_configured_families -v
```

Expected: FAIL — the screen has no table or doesn't load manifests yet.

- [ ] **Step 3: Implement FamilyScreen with DataTable**

Replace `src/modelman/screens/families.py` with:

```python
"""FamilyScreen — default view listing all configured families."""
from __future__ import annotations

from pathlib import Path

from textual.app import ComposeResult
from textual.containers import Horizontal
from textual.screen import Screen
from textual.widgets import DataTable, Footer, Header

from ..config import load_config
from ..manifest import get_family_dir, load_manifest


def _human_size(n: int | None) -> str:
    if n is None:
        return "—"
    for unit in ("B", "KB", "MB", "GB", "TB"):
        if n < 1024:
            return f"{n:.1f} {unit}" if unit != "B" else f"{n} {unit}"
        n /= 1024  # type: ignore[assignment]
    return f"{n:.1f} PB"


class FamilyScreen(Screen[None]):
    BINDINGS = [
        ("a", "add_family", "Add"),
        ("d", "delete_family", "Delete"),
        ("enter", "open_family", "Open"),
        ("q", "quit", "Quit"),
    ]

    def compose(self) -> ComposeResult:
        yield Header()
        yield DataTable(id="family-table", cursor_type="row")
        yield Footer()

    def on_mount(self) -> None:
        table = self.query_one(DataTable)
        table.add_columns("FAMILY", "DISPLAY", "VARIANTS", "DOWNLOADED", "SIZE")
        self.reload()

    def reload(self) -> None:
        table = self.query_one(DataTable)
        table.clear()
        family_dir: Path = get_family_dir()
        if not family_dir.exists():
            return
        try:
            config = load_config()
        except Exception:
            config = None  # type: ignore[assignment]

        for path in sorted(family_dir.glob("*.yaml")):
            try:
                m = load_manifest(path.stem, family_dir=family_dir)
            except Exception:
                continue
            downloaded = len(m.downloaded)
            variants = len(m.variants)
            total_size: int | None = 0
            unknown = False
            if config is not None:
                for v in m.variants:
                    if v["id"] in m.downloaded:
                        try:
                            from ..providers.registry import ProviderRegistry
                            provider = ProviderRegistry.get(
                                v["provider"], config.provider(v["provider"])
                            )
                            sz = provider.size_of(v)
                        except Exception:
                            sz = None
                        if sz is None:
                            unknown = True
                            continue
                        total_size = (total_size or 0) + sz
            size_str = "—" if unknown and not total_size else _human_size(total_size if not unknown else None)
            table.add_row(m.family, m.display_name or "", str(variants), str(downloaded), size_str)
```

- [ ] **Step 4: Update the smoke test to be tolerant**

Replace `tests/screens/test_app_navigation.py` test app-launching with the rows-count check only:

```python
@pytest.mark.asyncio
async def test_family_screen_lists_configured_families(tmp_path, monkeypatch):
    from modelman.manifest import FamilyManifest, save_manifest
    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    m = FamilyManifest(
        family="ornith", display_name="Ornith",
        variants=[{"id": "a", "provider": "ollama", "name": "o:35b"}],
    )
    m.mark_downloaded("a", str(tmp_path / "downloaded-a"))
    save_manifest(m, fam_dir / "ornith.yaml")

    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text(
        "providers:\n  ollama:\n    type: ollama\n"
    )

    from modelman.app import ModelmanApp
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        table = app.screen.query_one("DataTable")
        assert table.row_count == 1
```

(Replace the earlier `test_family_screen_lists_configured_families` body with this simpler version.)

- [ ] **Step 5: Run tests to verify they pass**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/screens/test_app_navigation.py -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /Users/keith/github/ohanaverse/modelman
git add src/modelman/screens/families.py tests/screens/test_app_navigation.py
git commit -m "feat(tui): FamilyScreen lists families with size column"
```

---

## Task 11: AddFamilyModal

**Files:**
- Create: `src/modelman/screens/forms.py`
- Modify: `src/modelman/screens/families.py`
- Modify: `tests/screens/test_app_navigation.py`

- [ ] **Step 1: Write the failing test**

Append to `tests/screens/test_app_navigation.py`:

```python
@pytest.mark.asyncio
async def test_add_family_creates_manifest(tmp_path, monkeypatch):
    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    from modelman.app import ModelmanApp
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        # Press 'a' to open the modal.
        await pilot.press("a")
        # Type a family name and submit.
        for ch in "mamba":
            await pilot.press(ch)
        await pilot.press("enter")
        await pilot.pause()
        # Manifest file exists.
        assert (fam_dir / "mamba.yaml").exists()
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/screens/test_app_navigation.py::test_add_family_creates_manifest -v
```

Expected: FAIL — `a` binding not handled.

- [ ] **Step 3: Implement `AddFamilyModal` and wire `a` action**

Add to `src/modelman/screens/forms.py`:

```python
"""Modal forms for the TUI."""
from __future__ import annotations

from pathlib import Path
from typing import Optional

from textual.app import ComposeResult
from textual.containers import Vertical
from textual.screen import ModalScreen
from textual.widgets import Button, Input, Label

from ..manifest import FamilyManifest, save_manifest, get_family_dir


class AddFamilyModal(ModalScreen[Optional[FamilyManifest]]):
    """Prompt for a family name and optional display name."""

    DEFAULT_CSS = """
    AddFamilyModal { align: center middle; }
    AddFamilyModal > Vertical { width: 60; height: auto; padding: 1 2; border: round $primary; }
    AddFamilyModal Label { margin-bottom: 1; }
    AddFamilyModal Input { margin-bottom: 1; }
    AddFamilyModal Horizontal { height: auto; align-horizontal: right; }
    AddFamilyModal Button { margin-left: 1; }
    """

    def compose(self) -> ComposeResult:
        with Vertical():
            yield Label("Family name (required):")
            yield Input(id="family-name", placeholder="e.g. ornith-1.5")
            yield Label("Display name (optional):")
            yield Input(id="display-name", placeholder="e.g. Ornith 1.5")
            from textual.containers import Horizontal
            with Horizontal():
                yield Button("Cancel", id="cancel", variant="default")
                yield Button("Create", id="create", variant="primary")

    def on_button_pressed(self, event: Button.Pressed) -> None:
        if event.button.id == "cancel":
            self.dismiss(None)
            return
        name = self.query_one("#family-name", Input).value.strip()
        display = self.query_one("#display-name", Input).value.strip()
        if not name:
            return
        manifest = FamilyManifest(family=name, display_name=display or name)
        path: Path = get_family_dir() / f"{name}.yaml"
        save_manifest(manifest, path)
        self.dismiss(manifest)
```

Then in `src/modelman/screens/families.py`, add an action and import:

```python
from .forms import AddFamilyModal
```

(Add this import at the top alongside the existing ones; `FamilyManifest` is already imported via `from ..manifest import ...`.)

And add an action method on `FamilyScreen`:

```python
    def action_add_family(self) -> None:
        def _on_close(result: FamilyManifest | None) -> None:
            if result is not None:
                self.reload()
        self.app.push_screen(AddFamilyModal(), _on_close)
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/screens/test_app_navigation.py::test_add_family_creates_manifest -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/keith/github/ohanaverse/modelman
git add src/modelman/screens/forms.py src/modelman/screens/families.py tests/screens/test_app_navigation.py
git commit -m "feat(tui): AddFamilyModal creates a new family manifest"
```

---

## Task 12: Delete family with guard

**Files:**
- Modify: `src/modelman/screens/families.py`
- Modify: `tests/screens/test_app_navigation.py`

- [ ] **Step 1: Write the failing tests**

Append to `tests/screens/test_app_navigation.py`:

```python
@pytest.mark.asyncio
async def test_delete_family_when_empty(tmp_path, monkeypatch):
    from modelman.manifest import FamilyManifest, save_manifest
    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    save_manifest(FamilyManifest(family="mamba"), fam_dir / "mamba.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    from modelman.app import ModelmanApp
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("d")
        await pilot.pause()
        assert not (fam_dir / "mamba.yaml").exists()


@pytest.mark.asyncio
async def test_delete_family_blocked_when_downloaded(tmp_path, monkeypatch):
    from modelman.manifest import FamilyManifest, save_manifest
    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    m = FamilyManifest(
        family="ornith",
        variants=[{"id": "a", "provider": "ollama", "name": "o:35b"}],
    )
    m.mark_downloaded("a", str(tmp_path / "downloaded"))
    save_manifest(m, fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    from modelman.app import ModelmanApp
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("d")
        await pilot.pause()
        # Still there — delete was blocked.
        assert (fam_dir / "ornith.yaml").exists()
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/screens/test_app_navigation.py -v -k "delete_family"
```

Expected: FAIL — no `action_delete_family`.

- [ ] **Step 3: Implement `action_delete_family` and a confirm modal**

Add to `src/modelman/screens/forms.py`:

```python
class ConfirmModal(ModalScreen[bool]):
    """Generic yes/no confirmation. Default is No."""
    DEFAULT_CSS = """
    ConfirmModal { align: center middle; }
    ConfirmModal > Vertical { width: 60; height: auto; padding: 1 2; border: round $primary; }
    ConfirmModal Label { margin-bottom: 1; }
    ConfirmModal Horizontal { height: auto; align-horizontal: right; }
    ConfirmModal Button { margin-left: 1; }
    """

    def __init__(self, message: str) -> None:
        super().__init__()
        self._message = message

    def compose(self) -> ComposeResult:
        with Vertical():
            yield Label(self._message)
            from textual.containers import Horizontal
            with Horizontal():
                yield Button("No", id="no", variant="default")
                yield Button("Yes", id="yes", variant="warning")

    def on_button_pressed(self, event: Button.Pressed) -> None:
        self.dismiss(event.button.id == "yes")
```

In `src/modelman/screens/families.py`, add imports:

```python
from ..manifest import get_family_dir
from .forms import AddFamilyModal, ConfirmModal
```

And add the action:

```python
    def action_delete_family(self) -> None:
        table = self.query_one(DataTable)
        if table.row_count == 0:
            return
        # Selected row key 0 corresponds to first family row.
        # We rely on row ordering matching sorted glob in reload().
        row_key = list(table.rows.keys())[table.cursor_row]
        family_name = str(row_key.value)
        try:
            m = load_manifest(family_name)
        except Exception:
            return
        if m.downloaded:
            self.app.push_screen(
                ConfirmModal(f"'{family_name}' has {len(m.downloaded)} downloaded model(s). Delete anyway? (will be blocked)")
            )
            # Block delete: re-push a no-op screen that dismisses false.
            self._notify_blocked(family_name)
            return
        self.app.push_screen(
            ConfirmModal(f"Delete empty family '{family_name}'?"),
            self._on_delete_confirm,
        )

    def _on_delete_confirm(self, confirmed: bool) -> None:
        if not confirmed:
            return
        table = self.query_one(DataTable)
        row_key = list(table.rows.keys())[table.cursor_row]
        family_name = str(row_key.value)
        path = get_family_dir() / f"{family_name}.yaml"
        if path.exists():
            path.unlink()
        self.reload()

    def _notify_blocked(self, family_name: str) -> None:
        self.app.push_screen(
            ConfirmModal(f"Cannot delete '{family_name}': it has downloaded models. Remove them first."),
        )
```

(`_notify_blocked` is shown but dismissed with default No; for a minimal implementation this is acceptable. If you want a non-blocking toast, swap it for `self.app.notify(...)`.)

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/screens/test_app_navigation.py -v -k "delete_family"
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/keith/github/ohanaverse/modelman
git add src/modelman/screens/forms.py src/modelman/screens/families.py tests/screens/test_app_navigation.py
git commit -m "feat(tui): delete family with downloaded-models guard"
```

---

## Task 13: Open family → ModelScreen

**Files:**
- Create: `src/modelman/screens/models.py`
- Modify: `src/modelman/screens/families.py`
- Modify: `tests/screens/test_app_navigation.py`

- [ ] **Step 1: Write the failing test**

Append to `tests/screens/test_app_navigation.py`:

```python
@pytest.mark.asyncio
async def test_enter_opens_model_screen(tmp_path, monkeypatch):
    from modelman.manifest import FamilyManifest, save_manifest
    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    save_manifest(FamilyManifest(family="ornith"), fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    from modelman.app import ModelmanApp
    from modelman.screens.models import ModelScreen
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        assert isinstance(app.screen, ModelScreen)
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/screens/test_app_navigation.py::test_enter_opens_model_screen -v
```

Expected: FAIL.

- [ ] **Step 3: Implement `ModelScreen` (skeleton) and `action_open_family`**

Create `src/modelman/screens/models.py`:

```python
"""ModelScreen — drill into a family's models grouped by provider."""
from __future__ import annotations

from typing import TYPE_CHECKING

from textual.app import ComposeResult
from textual.screen import Screen
from textual.widgets import Footer, Header

if TYPE_CHECKING:
    from ..manifest import FamilyManifest


class ModelScreen(Screen[None]):
    BINDINGS = [
        ("escape", "back", "Back"),
    ]

    def __init__(self, manifest: "FamilyManifest", manifest_path) -> None:
        super().__init__()
        self.manifest = manifest
        self.manifest_path = manifest_path

    def compose(self) -> ComposeResult:
        yield Header()
        yield Footer()

    def action_back(self) -> None:
        self.app.pop_screen()
```

In `src/modelman/screens/families.py`, add the action and import:

```python
from .models import ModelScreen
from ..manifest import get_family_dir
```

And:

```python
    def action_open_family(self) -> None:
        table = self.query_one(DataTable)
        if table.row_count == 0:
            return
        row_key = list(table.rows.keys())[table.cursor_row]
        family_name = str(row_key.value)
        m = load_manifest(family_name)
        path = get_family_dir() / f"{family_name}.yaml"
        self.app.push_screen(ModelScreen(m, path))
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/screens/test_app_navigation.py::test_enter_opens_model_screen -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/keith/github/ohanaverse/modelman
git add src/modelman/screens/models.py src/modelman/screens/families.py tests/screens/test_app_navigation.py
git commit -m "feat(tui): ModelScreen opens from FamilyScreen via Enter"
```

---

## Task 14: ModelScreen two-pane layout + status column

**Files:**
- Modify: `src/modelman/screens/models.py`
- Modify: `tests/screens/test_app_navigation.py`

- [ ] **Step 1: Write the failing test**

Append to `tests/screens/test_app_navigation.py`:

```python
@pytest.mark.asyncio
async def test_model_screen_two_pane_lists_providers_and_models(tmp_path, monkeypatch):
    from modelman.manifest import FamilyManifest, save_manifest
    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    m = FamilyManifest(
        family="ornith",
        variants=[
            {"id": "o35", "provider": "ollama", "name": "ornith:35b"},
            {"id": "q4", "provider": "llamacpp", "name": "q4",
             "repo": "o/r", "files": ["x.gguf"]},
        ],
    )
    save_manifest(m, fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text(
        "providers:\n  ollama:\n    type: ollama\n  llamacpp:\n    type: llamacpp\n"
    )

    from modelman.app import ModelmanApp
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        # Two DataTables: provider list and models list.
        tables = app.screen.query("DataTable")
        assert len(tables) == 2
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/screens/test_app_navigation.py::test_model_screen_two_pane_lists_providers_and_models -v
```

Expected: FAIL — ModelScreen has no tables.

- [ ] **Step 3: Implement two-pane layout in ModelScreen**

Replace `src/modelman/screens/models.py` with:

```python
"""ModelScreen — drill into a family's models grouped by provider."""
from __future__ import annotations

from collections import defaultdict
from typing import TYPE_CHECKING

from textual.app import ComposeResult
from textual.containers import Horizontal, Vertical
from textual.screen import Screen
from textual.widgets import DataTable, Footer, Header

from ..providers.registry import ProviderRegistry
from ..config import load_config

if TYPE_CHECKING:
    from ..manifest import FamilyManifest


def _human_size(n) -> str:
    if n is None:
        return "—"
    for unit in ("B", "KB", "MB", "GB", "TB"):
        if n < 1024:
            return f"{n:.1f} {unit}" if unit != "B" else f"{n} {unit}"
        n /= 1024
    return f"{n:.1f} PB"


class ModelScreen(Screen[None]):
    BINDINGS = [
        ("escape", "back", "Back"),
    ]

    def __init__(self, manifest: "FamilyManifest", manifest_path) -> None:
        super().__init__()
        self.manifest = manifest
        self.manifest_path = manifest_path
        self.selected_provider: str | None = None

    def compose(self) -> ComposeResult:
        yield Header()
        with Horizontal(id="panes"):
            with Vertical(id="provider-pane"):
                yield DataTable(id="provider-table", cursor_type="row")
            with Vertical(id="model-pane"):
                yield DataTable(id="model-table", cursor_type="row")
        yield Footer()

    def on_mount(self) -> None:
        providers = DataTable(id="provider-table")  # type: ignore[has-type]
        pt = self.query_one("#provider-table", DataTable)
        pt.add_columns("PROVIDER", "MODELS")
        mt = self.query_one("#model-table", DataTable)
        mt.add_columns("NAME", "STATUS", "SIZE", "PATH")
        self.reload()

    def reload(self) -> None:
        pt = self.query_one("#provider-table", DataTable)
        pt.clear()
        counts: dict[str, int] = defaultdict(int)
        for v in self.manifest.variants:
            counts[v["provider"]] += 1
        for provider, count in counts.items():
            pt.add_row(provider, str(count), key=provider)
        if counts and self.selected_provider is None:
            self.selected_provider = next(iter(counts))
        if self.selected_provider:
            self._load_models_for_provider(self.selected_provider)

    def on_data_table_row_highlighted(self, event: DataTable.RowHighlighted) -> None:
        if event.control.id == "provider-table":
            row_key = event.row_key
            if row_key is not None:
                self.selected_provider = str(row_key.value)
                self._load_models_for_provider(self.selected_provider)

    def _load_models_for_provider(self, provider: str) -> None:
        mt = self.query_one("#model-table", DataTable)
        mt.clear()
        try:
            config = load_config()
        except Exception:
            config = None
        for v in self.manifest.variants:
            if v["provider"] != provider:
                continue
            downloaded = v["id"] in self.manifest.downloaded
            status = "✓" if downloaded else "○"
            path = self.manifest.downloaded.get(v["id"], {}).get("local_path", "—")
            size = "—"
            if downloaded and config is not None:
                try:
                    p = ProviderRegistry.get(provider, config.provider(provider))
                    sz = p.size_of(v)
                    size = _human_size(sz)
                except Exception:
                    pass
            mt.add_row(v["name"], status, size, path, key=v["id"])

    def action_back(self) -> None:
        self.app.pop_screen()
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/screens/test_app_navigation.py::test_model_screen_two_pane_lists_providers_and_models -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/keith/github/ohanaverse/modelman
git add src/modelman/screens/models.py tests/screens/test_app_navigation.py
git commit -m "feat(tui): ModelScreen two-pane with providers and models"
```

---

## Task 15: Toggle download (`x`) + pending bar

**Files:**
- Modify: `src/modelman/screens/models.py`
- Modify: `tests/screens/test_app_navigation.py`

- [ ] **Step 1: Write the failing test**

Append to `tests/screens/test_app_navigation.py`:

```python
@pytest.mark.asyncio
async def test_toggle_download_queues_variant(tmp_path, monkeypatch):
    from modelman.manifest import FamilyManifest, save_manifest
    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    m = FamilyManifest(
        family="ornith",
        variants=[{"id": "o35", "provider": "ollama", "name": "ornith:35b"}],
    )
    save_manifest(m, fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    from modelman.app import ModelmanApp
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        # Cursor is on the only model row; press x.
        await pilot.press("x")
        await pilot.pause()
        pending = app.screen.queued_downloads
        assert "o35" in pending
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/screens/test_app_navigation.py::test_toggle_download_queues_variant -v
```

Expected: FAIL.

- [ ] **Step 3: Implement `x` action and pending state + bottom bar**

Modify `src/modelman/screens/models.py`:

Add to imports:

```python
from textual.widgets import DataTable, Footer, Header, Static
```

Add to `ModelScreen.__init__`:

```python
        self.queued_downloads: dict[str, dict] = {}
        self.queued_deletes: dict[str, dict] = {}
```

In `compose`, append after the panes:

```python
        yield Static("Pending: download 0 · delete 0", id="pending-bar")
```

Add a binding:

```python
    BINDINGS = [
        ("escape", "back", "Back"),
        ("x", "toggle_download", "Toggle download"),
    ]
```

Add property `_pending_summary` and method `_refresh_pending_bar`:

```python
    def _refresh_pending_bar(self) -> None:
        bar = self.query_one("#pending-bar", Static)
        bar.update(
            f"Pending: download {len(self.queued_downloads)} · delete {len(self.queued_deletes)}"
        )
```

Add action:

```python
    def action_toggle_download(self) -> None:
        mt = self.query_one("#model-table", DataTable)
        if mt.row_count == 0:
            return
        row_key = list(mt.rows.keys())[mt.cursor_row]
        vid = str(row_key.value)
        variant = self.manifest.variant_by_id(vid)
        if variant is None:
            return
        if vid in self.manifest.downloaded:
            return  # already downloaded — no-op
        if vid in self.queued_downloads:
            self.queued_downloads.pop(vid)
        else:
            self.queued_downloads[vid] = variant
        self._refresh_pending_bar()
```

In `on_mount`, after building tables, call `self._refresh_pending_bar()`.

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/screens/test_app_navigation.py::test_toggle_download_queues_variant -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/keith/github/ohanaverse/modelman
git add src/modelman/screens/models.py tests/screens/test_app_navigation.py
git commit -m "feat(tui): toggle download on exit with pending bar"
```

---

## Task 16: Add/Edit/Delete model modals + delete queue

**Files:**
- Modify: `src/modelman/screens/forms.py`
- Modify: `src/modelman/screens/models.py`
- Modify: `tests/screens/test_app_navigation.py`

- [ ] **Step 1: Write the failing test**

Append to `tests/screens/test_app_navigation.py`:

```python
@pytest.mark.asyncio
async def test_add_then_delete_model_queues_changes(tmp_path, monkeypatch):
    from modelman.manifest import FamilyManifest, save_manifest
    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    m = FamilyManifest(
        family="ornith",
        variants=[{"id": "o35", "provider": "ollama", "name": "ornith:35b"}],
    )
    m.mark_downloaded("o35", str(tmp_path / "downloaded"))
    save_manifest(m, fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    from modelman.app import ModelmanApp
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        # Press 'd' on the selected (already-downloaded) model to queue delete.
        await pilot.press("d")
        await pilot.pause()
        assert "o35" in app.screen.queued_deletes
        # Press 'a' to add a new model — we'll just type id and submit.
        await pilot.press("a")
        for ch in "newone":
            await pilot.press(ch)
        await pilot.press("enter")
        # Switch to name field, type and submit.
        await pilot.press("tab")
        for ch in "ornith:8b":
            await pilot.press(ch)
        await pilot.press("enter")
        await pilot.pause()
        added_ids = [v["id"] for v in app.screen.manifest.variants]
        assert "newone" in added_ids
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/screens/test_app_navigation.py::test_add_then_delete_model_queues_changes -v
```

Expected: FAIL — `a`/`d` not handled on ModelScreen.

- [ ] **Step 3: Implement `ModelForm`, `action_add_model`, `action_delete_model`**

Add to `src/modelman/screens/forms.py`:

```python
from typing import Optional, Callable
from textual.widgets import Input, Select, TextArea

from ..providers.base import VariantSpec
from ..ollama_caps import auto_detect_model_info


class ModelForm(ModalScreen[Optional[VariantSpec]]):
    """Add or edit a model. `variant=None` means add; else edit (id/provider fixed)."""

    DEFAULT_CSS = """
    ModelForm { align: center middle; }
    ModelForm > Vertical { width: 70; height: auto; padding: 1 2; border: round $primary; }
    ModelForm Label { margin-top: 1; }
    ModelForm Input, ModelForm Select { margin-bottom: 1; }
    ModelForm Horizontal { height: auto; align-horizontal: right; }
    ModelForm Button { margin-left: 1; }
    """

    def __init__(self, providers: list[str], variant: VariantSpec | None = None) -> None:
        super().__init__()
        self._providers = providers
        self._variant = variant  # None for add

    def compose(self) -> ComposeResult:
        editing = self._variant is not None
        with Vertical():
            yield Label("Provider:")
            yield Select(
                options=[(p, p) for p in self._providers],
                value=self._variant["provider"] if editing else self._providers[0],
                id="provider",
                disabled=editing,
            )
            yield Label("ID:")
            yield Input(
                value=self._variant["id"] if editing else "",
                placeholder="stable id (e.g. ollama-35b)",
                id="id",
                disabled=editing,
            )
            yield Label("Name (ollama):")
            yield Input(
                value=(self._variant.get("name") if editing else "") or "",
                placeholder="e.g. ornith-1.5:35b",
                id="name",
            )
            yield Label("Repo (llamacpp/omlx):")
            yield Input(
                value=(self._variant.get("repo") if editing else "") or "",
                placeholder="org/repo",
                id="repo",
            )
            yield Label("Files (llamacpp, comma-separated):")
            yield Input(
                value=",".join(self._variant.get("files") or []) if editing and self._variant.get("files") else "",
                placeholder="model.gguf",
                id="files",
            )
            yield Label("Quantizations (omlx, comma-separated):")
            yield Input(
                value=",".join(self._variant.get("quantizations") or []) if editing and self._variant.get("quantizations") else "",
                placeholder="4bit,8bit",
                id="quantizations",
            )
            from textual.containers import Horizontal
            with Horizontal():
                yield Button("Cancel", id="cancel", variant="default")
                yield Button("Save", id="save", variant="primary")

    def on_button_pressed(self, event: Button.Pressed) -> None:
        if event.button.id == "cancel":
            self.dismiss(None)
            return
        provider = str(self.query_one("#provider", Select).value)
        vid = self.query_one("#id", Input).value.strip()
        if not vid:
            return
        name = self.query_one("#name", Input).value.strip() or None
        repo = self.query_one("#repo", Input).value.strip() or None
        files_raw = self.query_one("#files", Input).value.strip()
        files = [f.strip() for f in files_raw.split(",") if f.strip()] or None
        quants_raw = self.query_one("#quantizations", Input).value.strip()
        quants = [q.strip() for q in quants_raw.split(",") if q.strip()] or None
        spec: VariantSpec = {
            "id": vid,
            "provider": provider,
            "name": name or vid,
            "repo": repo,
            "files": files,
            "quantizations": quants,
        }
        # Ollama auto-detect on add.
        if provider == "ollama" and self._variant is None and name:
            spec["model_info"] = auto_detect_model_info(name)
        else:
            spec["model_info"] = (self._variant or {}).get("model_info")
        self.dismiss(spec)
```

In `src/modelman/screens/models.py`, add bindings and actions:

```python
    BINDINGS = [
        ("escape", "back", "Back"),
        ("x", "toggle_download", "Toggle download"),
        ("a", "add_model", "Add"),
        ("d", "delete_model", "Delete"),
        ("e", "edit_model", "Edit"),
    ]
```

```python
    def _provider_list(self) -> list[str]:
        return sorted({v["provider"] for v in self.manifest.variants})

    def action_add_model(self) -> None:
        from .forms import ModelForm
        providers = self._provider_list() or ["ollama", "llamacpp", "omlx"]
        self.app.push_screen(
            ModelForm(providers=providers),
            self._on_add_model,
        )

    def _on_add_model(self, variant) -> None:
        if variant is None:
            return
        self.manifest.variants.append(variant)
        self.queued_downloads[variant["id"]] = variant
        self.reload()
        self._refresh_pending_bar()

    def action_delete_model(self) -> None:
        mt = self.query_one("#model-table", DataTable)
        if mt.row_count == 0:
            return
        row_key = list(mt.rows.keys())[mt.cursor_row]
        vid = str(row_key.value)
        variant = self.manifest.variant_by_id(vid)
        if variant is None:
            return
        if vid in self.queued_deletes:
            self.queued_deletes.pop(vid)
        else:
            self.queued_deletes[vid] = variant
        # If it was queued for download, drop that.
        self.queued_downloads.pop(vid, None)
        self._refresh_pending_bar()

    def action_edit_model(self) -> None:
        mt = self.query_one("#model-table", DataTable)
        if mt.row_count == 0:
            return
        row_key = list(mt.rows.keys())[mt.cursor_row]
        vid = str(row_key.value)
        variant = self.manifest.variant_by_id(vid)
        if variant is None:
            return
        from .forms import ModelForm
        self.app.push_screen(
            ModelForm(providers=self._provider_list(), variant=variant),
            self._on_edit_model,
        )

    def _on_edit_model(self, updated) -> None:
        if updated is None:
            return
        for i, v in enumerate(self.manifest.variants):
            if v["id"] == updated["id"]:
                self.manifest.variants[i] = updated
                break
        self.reload()
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/screens/test_app_navigation.py::test_add_then_delete_model_queues_changes -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/keith/github/ohanaverse/modelman
git add src/modelman/screens/forms.py src/modelman/screens/models.py tests/screens/test_app_navigation.py
git commit -m "feat(tui): add/edit/delete model modals with queueing"
```

---

## Task 17: ConfirmExitDialog + apply queue on exit

**Files:**
- Modify: `src/modelman/screens/forms.py`
- Modify: `src/modelman/screens/models.py`
- Modify: `tests/screens/test_app_navigation.py`

- [ ] **Step 1: Write the failing test**

Append to `tests/screens/test_app_navigation.py`:

```python
@pytest.mark.asyncio
async def test_escape_with_pending_shows_dialog_and_apply(tmp_path, monkeypatch):
    from modelman.manifest import FamilyManifest, save_manifest
    from unittest.mock import MagicMock
    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    m = FamilyManifest(
        family="ornith",
        variants=[{"id": "o35", "provider": "ollama", "name": "ornith:35b"}],
    )
    save_manifest(m, fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    # Patch ProviderRegistry.get to return a stub that records calls.
    from modelman.providers import registry
    original_get = registry.ProviderRegistry.get
    stub = MagicMock()
    stub.download.return_value = "/tmp/fake"
    stub.name = "ollama"
    def fake_get(name, cfg):
        if name == "ollama":
            return stub
        return original_get(name, cfg)
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(fake_get))

    from modelman.app import ModelmanApp
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        await pilot.press("x")         # queue download
        await pilot.press("escape")    # opens confirm dialog
        await pilot.pause()
        # Confirm "Yes" button — last button in the dialog is Yes.
        from textual.widgets import Button
        for btn in app.screen.query(Button):
            if btn.id == "yes":
                btn.press()
                break
        await pilot.pause()
        stub.download.assert_called()
        assert "o35" in app.screen.manifest.downloaded
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/screens/test_app_navigation.py::test_escape_with_pending_shows_dialog_and_apply -v
```

Expected: FAIL — `escape` currently pops without confirm.

- [ ] **Step 3: Implement `ConfirmExitDialog` and rewire `action_back`**

Add to `src/modelman/screens/forms.py`:

```python
from ..queue import PendingChanges


class ConfirmExitDialog(ModalScreen[bool]):
    """Show pending downloads/deletes and confirm apply."""
    DEFAULT_CSS = """
    ConfirmExitDialog { align: center middle; }
    ConfirmExitDialog > Vertical { width: 70; height: auto; padding: 1 2; border: round $primary; }
    ConfirmExitDialog Label { margin-bottom: 1; }
    ConfirmExitDialog Horizontal { height: auto; align-horizontal: right; }
    ConfirmExitDialog Button { margin-left: 1; }
    """

    def __init__(self, downloads: list, deletes: list) -> None:
        super().__init__()
        self._downloads = downloads
        self._deletes = deletes

    def compose(self) -> ComposeResult:
        with Vertical():
            yield Label(
                f"Pending: download {len(self._downloads)} · delete {len(self._deletes)}"
            )
            for v in self._downloads:
                yield Label(f"  ↓ {v['id']} ({v['provider']})")
            for v in self._deletes:
                yield Label(f"  × {v['id']} ({v['provider']})")
            yield Label("Apply these changes?")
            from textual.containers import Horizontal
            with Horizontal():
                yield Button("Cancel", id="no", variant="default")
                yield Button("Apply", id="yes", variant="primary")

    def on_button_pressed(self, event: Button.Pressed) -> None:
        self.dismiss(event.button.id == "yes")
```

In `src/modelman/screens/models.py`, replace `action_back`:

```python
    def action_back(self) -> None:
        if not self.queued_downloads and not self.queued_deletes:
            self.app.pop_screen()
            return
        from .forms import ConfirmExitDialog
        self.app.push_screen(
            ConfirmExitDialog(
                downloads=list(self.queued_downloads.values()),
                deletes=list(self.queued_deletes.values()),
            ),
            self._on_exit_confirm,
        )

    def _on_exit_confirm(self, confirmed: bool) -> None:
        if not confirmed:
            return
        from ..config import load_config
        try:
            config = load_config()
        except Exception:
            return
        providers: dict[str, object] = {}
        for v in list(self.queued_downloads.values()) + list(self.queued_deletes.values()):
            try:
                from ..providers.registry import ProviderRegistry
                providers[v["provider"]] = ProviderRegistry.get(
                    v["provider"], config.provider(v["provider"])
                )
            except Exception:
                continue
        pending = PendingChanges(
            manifest=self.manifest,
            manifest_path=self.manifest_path,
            providers=providers,
            downloads=list(self.queued_downloads.values()),
            deletes=list(self.queued_deletes.values()),
        )
        pending.apply()
        self.app.pop_screen()
```

Add the import at the top of `models.py`:

```python
from ..queue import PendingChanges
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/screens/test_app_navigation.py::test_escape_with_pending_shows_dialog_and_apply -v
```

Expected: PASS (download stub called, manifest updated).

- [ ] **Step 5: Commit**

```bash
cd /Users/keith/github/ohanaverse/modelman
git add src/modelman/screens/forms.py src/modelman/screens/models.py tests/screens/test_app_navigation.py
git commit -m "feat(tui): confirm-on-exit applies pending changes"
```

---

## Task 18: Rewire `modelman` and `modelman download` entry points

**Files:**
- Modify: `src/modelman/main.py`
- Modify: `src/modelman/commands/download.py` (or remove it)
- Modify: `tests/commands/test_download.py`

- [ ] **Step 1: Write the failing test**

Rewrite `tests/commands/test_download.py`:

```python
"""The `download` command is now a TUI shortcut; verify it launches ModelScreen."""
from unittest.mock import patch


def test_download_launches_tui_at_family(tmp_path, monkeypatch):
    from modelman.manifest import FamilyManifest, save_manifest
    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    save_manifest(FamilyManifest(family="ornith"), fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    from typer.testing import CliRunner
    from modelman.main import app

    with patch("modelman.main.run_tui") as run_tui:
        runner = CliRunner()
        result = runner.invoke(app, ["download", "ornith"])
        assert result.exit_code == 0
        run_tui.assert_called_once_with("ornith")


def test_no_args_launches_tui_at_family_list(tmp_path, monkeypatch):
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(tmp_path / "f"))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "c"))
    (tmp_path / "f").mkdir()
    (tmp_path / "c").write_text("providers:\n  ollama:\n    type: ollama\n")

    with patch("modelman.main.run_tui") as run_tui:
        from typer.testing import CliRunner
        from modelman.main import app
        runner = CliRunner()
        result = runner.invoke(app, [])
        assert result.exit_code == 0
        run_tui.assert_called_once_with(None)
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest tests/commands/test_download.py -v
```

Expected: FAIL — `run_tui` doesn't exist.

- [ ] **Step 3: Rewire `main.py`**

Replace `src/modelman/main.py`:

```python
"""modelman CLI entry point."""
from __future__ import annotations

import typer

# Import providers package to trigger registration of all providers.
from . import providers  # noqa: F401
from .app import ModelmanApp
from .screens.families import FamilyScreen
from .screens.models import ModelScreen
from .manifest import load_manifest, get_family_dir

app = typer.Typer(help="Manage local LLM model families across providers.")


def run_tui(family: str | None) -> None:
    """Launch the Textual TUI, optionally starting at a family's model screen."""
    modelman_app = ModelmanApp()
    if family is not None:
        manifest = load_manifest(family)
        path = get_family_dir() / f"{family}.yaml"
        modelman_app.push_screen(FamilyScreen())
        modelman_app.push_screen(ModelScreen(manifest, path))
    modelman_app.run()


@app.callback(invoke_without_command=True)
def _main(
    ctx: typer.Context,
):
    """Run `modelman` with no args to open the TUI."""
    if ctx.invoked_subcommand is None:
        run_tui(None)


@app.command()
def download(
    family: str = typer.Argument(..., help="Family name"),
):
    """Open the TUI at a family's model screen (queued downloads on exit)."""
    run_tui(family)


if __name__ == "__main__":
    app()
```

(Typer allows one `@app.callback()` per app; this single callback runs only when no subcommand was invoked, and the `download` subcommand still works on its own.)

- [ ] **Step 4: Delete the old questionary download logic**

Delete `src/modelman/commands/download.py` (its logic is replaced by the TUI). Keep `src/modelman/commands/__init__.py` if other commands will live there; otherwise delete the directory too.

- [ ] **Step 5: Run tests to verify they pass**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest -v
```

Expected: all tests pass.

- [ ] **Step 6: Run mypy**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run mypy src/
```

Expected: no errors.

- [ ] **Step 7: Commit**

```bash
cd /Users/keith/github/ohanaverse/modelman
git add src/modelman/main.py tests/commands/test_download.py
git rm src/modelman/commands/download.py
git commit -m "feat(cli): launch TUI; `download` opens at family model screen"
```

---

## Task 19: Final verification

- [ ] **Step 1: Run full test suite**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run pytest
```

Expected: all tests pass, coverage above 80% (`pytest --cov` if configured).

- [ ] **Step 2: Run mypy**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run mypy src/
```

Expected: clean.

- [ ] **Step 3: Manual smoke test**

```bash
cd /Users/keith/github/ohanaverse/modelman
uv run modelman
```

Expected: Textual TUI launches with the family list. Add a family, open it, add a model, toggle download, press Escape, confirm Apply — observe queued download runs.

- [ ] **Step 4: Final commit (if anything tweaked)**

```bash
cd /Users/keith/github/ohanaverse/modelman
git status
git add -u
git diff --cached --quiet || git commit -m "chore: post-implementation tweaks"
```
