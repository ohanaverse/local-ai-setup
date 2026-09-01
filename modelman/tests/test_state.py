"""modelman.toml — modelman's own per-machine state overlay. Holds only
per-machine mutable state (downloaded/disk_path/size_bytes/litellm_exposed)
keyed by registry model id; registry.toml holds everything that describes
the model itself (see 2026-08-27-shared-model-registry-design.md). The
file is optional: a fresh install has no state yet, so a missing file
returns an empty store rather than raising (matching settings.py's
existing convention for optional per-machine files)."""

from pathlib import Path

from modelman.state import (
    FamilyState,
    ModelState,
    StateStore,
    _default_state_path,
    load_state,
    save_state,
)


def test_load_state_missing_file_returns_empty_store(tmp_path):
    # A fresh install has no modelman.toml yet; loading must not raise, or
    # the TUI would crash on first run before any state exists.
    store = load_state(tmp_path / "nonexistent.toml")
    assert store.models == {}


def test_state_store_get_returns_default_for_unknown_model():
    # get() must return a blank ModelState for ids not yet in the store so
    # callers can read fields without a None check on every access.
    store = StateStore()
    assert store.get("ollama/x") == ModelState()


def test_load_state_accepts_legacy_downloaded_key(tmp_path):
    # Existing installs have modelman.toml files with the old `downloaded`
    # key. Renaming the field must not silently drop their ready state.
    path = tmp_path / "modelman.toml"
    path.write_text('[model_state."ollama/x"]\ndownloaded = true\ndisk_path = "/models/x"\n')
    store = load_state(path)
    assert store.get("ollama/x").ready is True
    assert store.get("ollama/x").disk_path == "/models/x"


def test_save_writes_ready_key_not_legacy_downloaded(tmp_path):
    # Going forward, only the new key should ever be written.
    path = tmp_path / "modelman.toml"
    store = StateStore()
    store.set("ollama/x", ModelState(ready=True))
    save_state(store, path)
    raw_text = path.read_text()
    assert "ready = true" in raw_text
    assert "downloaded" not in raw_text


def test_save_then_load_round_trips_model_state(tmp_path):
    # The whole point of the overlay is persistence across runs; if any
    # field is dropped on save/load, download state silently resets.
    store = StateStore()
    store.set(
        "llamacpp/qwen3.8-27b-q4",
        ModelState(
            ready=True,
            disk_path="/models/qwen3.8-27b-q4.gguf",
            size_bytes=17179869184,
            litellm_exposed=True,
        ),
    )
    path = tmp_path / "modelman.toml"

    save_state(store, path)
    loaded = load_state(path)

    assert loaded.get("llamacpp/qwen3.8-27b-q4") == ModelState(
        ready=True,
        disk_path="/models/qwen3.8-27b-q4.gguf",
        size_bytes=17179869184,
        litellm_exposed=True,
    )


def test_family_state_dataclass_round_trips_display_name():
    # display_name is optional; a family with no display name must round-trip
    # as None so family_display_name() can fall back to the family id.
    assert FamilyState(display_name="Qwen 3.8").display_name == "Qwen 3.8"
    assert FamilyState().display_name is None


def test_forget_family_removes_known_entry():
    # Deleting a family must remove its overlay entry so it no longer shows
    # as known and its display name is gone.
    store = StateStore()
    store.families["qwen3.8"] = FamilyState(display_name="Qwen 3.8")
    store.forget_family("qwen3.8")
    assert "qwen3.8" not in store.families


def test_forget_family_unknown_family_is_a_noop():
    # Deleting a family that was never recorded must not raise, so callers
    # don't need an existence check before removing.
    store = StateStore()
    store.forget_family("nope")  # must not raise
    assert store.families == {}


def test_save_then_load_round_trips_family_overlay(tmp_path):
    # The families overlay (display name + known marker) must survive a
    # save/load cycle, including a known family with no display name yet.
    store = StateStore()
    store.families["qwen3.8"] = FamilyState(display_name="Qwen 3.8")
    store.families["empty-family"] = FamilyState()  # known, no display name yet
    path = tmp_path / "modelman.toml"

    save_state(store, path)
    loaded = load_state(path)

    assert loaded.families["qwen3.8"].display_name == "Qwen 3.8"
    assert "empty-family" in loaded.families
    assert loaded.families["empty-family"].display_name is None


def test_default_state_path_honors_xdg(monkeypatch):
    # modelman.toml must land in the same directory as registry.toml, so it
    # honors XDG_CONFIG_HOME the same way registry.py does.
    monkeypatch.delenv("MODELMAN_STATE", raising=False)
    monkeypatch.setenv("XDG_CONFIG_HOME", "/custom/xdg")
    assert _default_state_path() == Path("/custom/xdg/local-ai/modelman.toml")


def test_default_state_path_modelman_state_override_wins(monkeypatch):
    # The explicit MODELMAN_STATE override must beat XDG_CONFIG_HOME, matching
    # registry.py's MODELMAN_REGISTRY precedence.
    monkeypatch.setenv("MODELMAN_STATE", "/custom/modelman.toml")
    monkeypatch.setenv("XDG_CONFIG_HOME", "/custom/xdg")
    assert _default_state_path() == Path("/custom/modelman.toml")


def test_save_then_load_preserves_unknown_keys(tmp_path):
    # Hand-edited fields in modelman.toml (per-model, per-family, and
    # top-level) must survive a save/load round-trip rather than being
    # silently dropped.
    path = tmp_path / "modelman.toml"
    path.write_text(
        '[model_state."ollama/x"]\n'
        "downloaded = true\n"
        'custom_field = "keep-me"\n'
        "\n"
        '[families."f"]\n'
        'display_name = "F"\n'
        "family_extra = 1\n"
        "\n"
        "[settings]\n"
        'top_level = "keep"\n'
    )
    store = load_state(path)
    save_state(store, path)
    loaded = load_state(path)
    assert loaded.get("ollama/x").ready is True
    assert loaded.get("ollama/x").extra == {"custom_field": "keep-me"}
    assert loaded.families["f"].extra == {"family_extra": 1}
    assert loaded.extra == {"settings": {"top_level": "keep"}}
