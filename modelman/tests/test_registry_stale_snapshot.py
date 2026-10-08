"""modelman must not save an old snapshot over a registry another program wrote.

wt writes registry.toml now. modelman's lock is in-process only and its TUI
saves the whole Registry it loaded at start, so without this guard an open
modelman would revert a `wt model` edit on its next save. Deleted with
modelman.
"""

import os
import tomllib
from pathlib import Path

import pytest

from modelman import registry as registry_module
from modelman.queue import PendingChanges
from modelman.registry import (
    AuthConfig,
    ModelEntry,
    ProviderEntry,
    Registry,
    RegistryError,
    RegistryNotFoundError,
    load_registry,
    locked_registry,
    save_registry,
)
from modelman.state import StateStore

STALE = "registry.toml changed on disk; reload"


@pytest.fixture(autouse=True)
def _no_record_from_an_earlier_test():
    """The guard's record is per process, so it outlives a test. Every test
    here uses its own tmp_path today; clearing the record keeps a test that
    reuses a path (a fixed name under a shared directory, a monkeypatched
    HOME) from being refused, or let through, because of an earlier one."""
    with registry_module._SEEN_LOCK:
        registry_module._SEEN_ON_DISK.clear()
        registry_module._FOREIGN_CHANGES.clear()


def _seed(path: Path) -> Registry:
    """Write a one-provider registry at `path` and load it, as a TUI would."""
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\n\n[providers.auth]\ntype = "none"\n',
        encoding="utf-8",
    )
    return load_registry(path)


def _as_wt_would(path: Path) -> str:
    """Another program replaces the file (atomically, as wt does) with a
    registry holding one more model. Returns the new text."""
    text = path.read_text(encoding="utf-8") + (
        '\n[[models]]\nid = "ollama/from-wt"\nfamily = "f"\n'
        'provider_id = "ollama"\nmodel_name = "from-wt"\n'
    )
    tmp = path.with_name(path.name + ".tmp")
    tmp.write_text(text, encoding="utf-8")
    os.replace(tmp, path)
    return text


def _add_model(registry: Registry, name: str) -> None:
    registry.models.append(
        ModelEntry(id=f"ollama/{name}", family="f", provider_id="ollama", model_name=name)
    )


def test_a_save_over_another_programs_write_is_refused(tmp_path):
    """The case the guard exists for: modelman loaded, wt wrote, modelman
    saves. The save must fail with the reload message and leave wt's file."""
    path = tmp_path / "registry.toml"
    registry = _seed(path)
    wt_text = _as_wt_would(path)

    _add_model(registry, "from-modelman")
    with pytest.raises(RegistryError, match=STALE):
        save_registry(registry, path)
    assert path.read_text(encoding="utf-8") == wt_text
    assert not list(tmp_path.glob(".registry.toml.*")), "a temp file was left behind"


def test_a_change_that_keeps_the_size_is_still_caught(tmp_path):
    """Size alone would miss an edit that swaps one character; the
    modification time catches it."""
    path = tmp_path / "registry.toml"
    registry = _seed(path)
    st = path.stat()
    path.write_text(path.read_text(encoding="utf-8").replace("Ollama", "OLLAMA"), encoding="utf-8")
    os.utime(path, ns=(st.st_atime_ns, st.st_mtime_ns + 1_000_000_000))
    assert path.stat().st_size == st.st_size

    with pytest.raises(RegistryError, match=STALE):
        save_registry(registry, path)


def test_modelmans_own_saves_never_trip_the_guard(tmp_path):
    """A successful write records the new file as seen. Without that the
    second save in one TUI session would be refused — and so would the
    screen's save after the background price refresh (locked_registry) wrote."""
    path = tmp_path / "registry.toml"
    screen_registry = _seed(path)

    _add_model(screen_registry, "first")
    save_registry(screen_registry, path)
    _add_model(screen_registry, "second")
    save_registry(screen_registry, path)

    with locked_registry(path) as fresh:  # the price-refresh worker
        _add_model(fresh, "from-worker")
    _add_model(screen_registry, "third")
    save_registry(screen_registry, path)  # an older object, but no other program wrote

    assert [m.id for m in load_registry(path).models] == [
        "ollama/first",
        "ollama/second",
        "ollama/third",
    ]


def test_a_reload_clears_the_refusal(tmp_path):
    """The message says to reload, so that has to work: loading the registry
    again makes the next save acceptable, and it carries the other program's
    edit."""
    path = tmp_path / "registry.toml"
    registry = _seed(path)
    _as_wt_would(path)
    with pytest.raises(RegistryError, match=STALE):
        save_registry(registry, path)

    reloaded = load_registry(path)
    _add_model(reloaded, "from-modelman")
    save_registry(reloaded, path)
    assert [m.id for m in load_registry(path).models] == ["ollama/from-wt", "ollama/from-modelman"]


def test_locked_registry_applies_on_top_of_another_programs_write(tmp_path):
    """locked_registry re-reads the file before it writes, so it is never
    stale: the price refresh lands on top of wt's edit instead of failing."""
    path = tmp_path / "registry.toml"
    _seed(path)
    _as_wt_would(path)
    with locked_registry(path) as fresh:
        _add_model(fresh, "priced")
    assert [m.id for m in load_registry(path).models] == ["ollama/from-wt", "ollama/priced"]


def test_a_first_save_is_never_refused(tmp_path):
    """A path this process never looked at has no snapshot to be stale against:
    `modelman migrate` writing a new registry, or a save over a file that was
    never read, must work as before."""
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))]
    )
    new = tmp_path / "new" / "registry.toml"
    save_registry(registry, new)
    assert load_registry(new).provider("ollama").id == "ollama"

    never_loaded = tmp_path / "other.toml"
    never_loaded.write_text("providers = []\n", encoding="utf-8")
    save_registry(registry, never_loaded)
    assert load_registry(never_loaded).provider("ollama").id == "ollama"


def test_a_registry_deleted_since_it_was_loaded_is_refused(tmp_path):
    """A file that is gone has changed on disk too. Recreating it from an old
    snapshot would undo whoever removed or moved it."""
    path = tmp_path / "registry.toml"
    registry = _seed(path)
    path.unlink()
    with pytest.raises(RegistryError, match=STALE):
        save_registry(registry, path)
    assert not path.exists()


def test_a_symlinked_registry_is_judged_by_its_target(tmp_path):
    """wt writes through a symlinked registry, replacing the target file.
    The record is kept under the resolved path, so a load through the link
    and a write to the target are the same file to the guard."""
    target = tmp_path / "dotfiles" / "registry.toml"
    link = tmp_path / "config" / "registry.toml"
    link.parent.mkdir()
    registry = _seed(target)
    link.symlink_to(target)
    via_link = load_registry(link)

    wt_text = _as_wt_would(target)
    for stale in (registry, via_link):
        with pytest.raises(RegistryError, match=STALE):
            save_registry(stale, link)
    assert target.read_text(encoding="utf-8") == wt_text
    assert link.is_symlink()


def test_a_background_load_does_not_bless_an_older_snapshot(tmp_path):
    """The TUI's price refresh loads the registry on a worker thread while the
    screen still holds the snapshot from startup. If wt wrote in between, the
    worker's load must not make the screen's snapshot look current: the
    screen's save is still refused, and wt's row survives."""
    path = tmp_path / "registry.toml"
    screen_registry = _seed(path)
    _as_wt_would(path)
    with locked_registry(path):  # the worker: loads wt's file, saves it back
        pass

    _add_model(screen_registry, "from-modelman")
    with pytest.raises(RegistryError, match=STALE):
        save_registry(screen_registry, path)
    assert [m.id for m in load_registry(path).models] == ["ollama/from-wt"]


def test_a_registry_created_after_modelman_found_none_is_not_overwritten(tmp_path):
    """A fresh machine: modelman opens with no registry and holds an empty
    one, then `wt model init` creates the file. "No file" is a snapshot too,
    so modelman's save must not replace wt's registry with its empty one."""
    path = tmp_path / "registry.toml"
    with pytest.raises(RegistryNotFoundError):
        load_registry(path)
    path.write_text('[[providers]]\nid = "ollama"\nname = "Ollama"\n', encoding="utf-8")
    wt_text = _as_wt_would(path)

    with pytest.raises(RegistryError, match=STALE):
        save_registry(Registry(), path)
    assert path.read_text(encoding="utf-8") == wt_text


def test_a_background_load_does_not_bless_the_empty_registry(tmp_path):
    """The same fresh machine, one step later: after `wt model init` created
    the file, the TUI's price-refresh worker loads it, so the file on disk is
    the one this process last read. The screen's empty Registry was built in
    memory, not loaded, and must still be refused — saving it would wipe
    every row wt seeded."""
    path = tmp_path / "registry.toml"
    with pytest.raises(RegistryNotFoundError):
        load_registry(path)
    screen_registry = Registry()
    _seed(path)  # wt creates the file; the load inside is the worker's
    wt_text = path.read_text(encoding="utf-8")

    with pytest.raises(RegistryError, match=STALE):
        save_registry(screen_registry, path)
    assert path.read_text(encoding="utf-8") == wt_text


def test_a_registry_modelman_found_absent_can_still_be_created(tmp_path):
    """The other half of recording "no file": when nobody else creates it,
    modelman's first save does — the Add dialog on a fresh install."""
    path = tmp_path / "registry.toml"
    with pytest.raises(RegistryNotFoundError):
        load_registry(path)
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))]
    )
    save_registry(registry, path)
    assert load_registry(path).provider("ollama").id == "ollama"


def test_the_pre_xdg_fallback_guards_the_path_a_save_writes(tmp_path, monkeypatch):
    """With XDG_CONFIG_HOME set and the registry only under ~/.config,
    modelman reads the old file and saves to the XDG path. wt has no such
    fallback and creates the XDG file; modelman's save must not replace it."""
    home = tmp_path / "home"
    monkeypatch.setenv("HOME", str(home))
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg"))
    monkeypatch.delenv("WT_REGISTRY", raising=False)
    monkeypatch.delenv("MODELMAN_REGISTRY", raising=False)
    _seed(home / ".config" / "local-ai" / "registry.toml")
    registry = load_registry()

    canonical = tmp_path / "xdg" / "local-ai" / "registry.toml"
    canonical.parent.mkdir(parents=True)
    canonical.write_text('[[providers]]\nid = "ollama"\nname = "Ollama"\n', encoding="utf-8")
    wt_text = _as_wt_would(canonical)

    with pytest.raises(RegistryError, match=STALE):
        save_registry(registry)
    assert canonical.read_text(encoding="utf-8") == wt_text


def test_a_replacement_with_the_same_size_and_time_is_caught(tmp_path):
    """wt replaces the file by rename, so its inode changes even when the size
    and the modification time do not (a same-size edit inside one tick of a
    coarse file clock, as on CI's filesystem)."""
    path = tmp_path / "registry.toml"
    registry = _seed(path)
    st = path.stat()
    tmp = path.with_name("replacement.tmp")
    tmp.write_text(path.read_text(encoding="utf-8").replace("Ollama", "OLLAMA"), encoding="utf-8")
    os.utime(tmp, ns=(st.st_atime_ns, st.st_mtime_ns))
    os.replace(tmp, path)
    after = path.stat()
    assert (after.st_size, after.st_mtime_ns) == (st.st_size, st.st_mtime_ns)

    with pytest.raises(RegistryError, match=STALE):
        save_registry(registry, path)


def test_a_save_during_a_load_does_not_poison_the_record(tmp_path, monkeypatch):
    """The price-refresh worker loads without the lock. If the screen saves
    between the worker's open() and its stat, the worker must not record the
    file it opened (now replaced) as the current one: every later save in
    that TUI would be refused though no other program wrote anything."""
    path = tmp_path / "registry.toml"
    screen_registry = _seed(path)
    real_load = tomllib.load

    def load_while_the_screen_saves(f):
        raw = real_load(f)
        monkeypatch.setattr(registry_module.tomllib, "load", real_load)
        _add_model(screen_registry, "first")
        save_registry(screen_registry, path)
        return raw

    monkeypatch.setattr(registry_module.tomllib, "load", load_while_the_screen_saves)
    load_registry(path)  # the worker's load, with the screen's save in the middle

    _add_model(screen_registry, "second")
    save_registry(screen_registry, path)
    assert [m.id for m in load_registry(path).models] == ["ollama/first", "ollama/second"]


def test_a_save_landing_while_a_load_records_does_not_poison_the_record(tmp_path, monkeypatch):
    """The load's disk check and the record it leaves are not one step: a save
    landing between them (modelman's own, on another thread) used to be read
    as another program's change, and left the record describing the file the
    load had opened — replaced by then. Every later save in the process was
    refused though nothing foreign had been written."""
    path = tmp_path / "registry.toml"
    screen_registry = _seed(path)
    _add_model(screen_registry, "first")
    save_registry(screen_registry, path)

    real_note = registry_module._note_loaded
    saved: list[bool] = []

    def note_after_a_save(p, stamp):
        if not saved:
            saved.append(True)
            _add_model(screen_registry, "second")
            save_registry(screen_registry, path)
        return real_note(p, stamp)

    monkeypatch.setattr(registry_module, "_note_loaded", note_after_a_save)
    load_registry(path)  # the price-refresh worker's load

    _add_model(screen_registry, "third")
    save_registry(screen_registry, path)  # no other program wrote: not refused
    assert [m.id for m in load_registry(path).models] == [
        "ollama/first",
        "ollama/second",
        "ollama/third",
    ]


def test_a_refused_save_at_the_end_of_an_apply_is_reported(tmp_path):
    """The path a TUI user sees: a queued Apply ends with one save, and when
    that save is refused the run reports `save:fail` with the reload message
    instead of raising — and wt's file is left alone."""
    path = tmp_path / "registry.toml"
    registry = _seed(path)
    _add_model(registry, "queued")
    save_registry(registry, path)
    wt_text = _as_wt_would(path)

    events: list[str] = []
    pending = PendingChanges(
        registry=registry,
        state=StateStore(),
        registry_path=path,
        state_path=tmp_path / "modelman.toml",
        providers={},
        moves=[("ollama/queued", "other-family")],
    )
    pending.apply(on_event=events.append)

    assert [e for e in events if e.startswith("save:")] == ["save:start", f"save:fail|{STALE}"]
    assert path.read_text(encoding="utf-8") == wt_text
