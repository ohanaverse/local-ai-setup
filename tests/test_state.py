"""modelman.toml — modelman's own per-machine state overlay. Holds only
per-machine mutable state (downloaded/disk_path/size_bytes/litellm_exposed)
keyed by registry model id; registry.toml holds everything that describes
the model itself (see 2026-08-27-shared-model-registry-design.md). The
file is optional: a fresh install has no state yet, so a missing file
returns an empty store rather than raising (matching settings.py's
existing convention for optional per-machine files)."""

from modelman.state import FamilyState, ModelState, StateStore, load_state, save_state


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


def test_save_then_load_round_trips_model_state(tmp_path):
    # The whole point of the overlay is persistence across runs; if any
    # field is dropped on save/load, download state silently resets.
    store = StateStore()
    store.set(
        "llamacpp/qwen3.8-27b-q4",
        ModelState(
            downloaded=True,
            disk_path="/models/qwen3.8-27b-q4.gguf",
            size_bytes=17179869184,
            litellm_exposed=True,
        ),
    )
    path = tmp_path / "modelman.toml"

    save_state(store, path)
    loaded = load_state(path)

    assert loaded.get("llamacpp/qwen3.8-27b-q4") == ModelState(
        downloaded=True,
        disk_path="/models/qwen3.8-27b-q4.gguf",
        size_bytes=17179869184,
        litellm_exposed=True,
    )


def test_family_state_dataclass_round_trips_display_name():
    # display_name is optional; a family with no display name must round-trip
    # as None so family_display_name() can fall back to the family id.
    assert FamilyState(display_name="Qwen 3.8").display_name == "Qwen 3.8"
    assert FamilyState().display_name is None


def test_family_display_name_falls_back_to_family_id():
    # The TUI shows a human name when set but must degrade to the raw family
    # id when none is recorded, otherwise families would render blank.
    store = StateStore()
    assert store.family_display_name("qwen3.8") == "qwen3.8"


def test_touch_family_sets_display_name():
    # "Add Family" records a display name before any model exists; the name
    # must be retrievable immediately so the family list shows it.
    store = StateStore()
    store.touch_family("qwen3.8", display_name="Qwen 3.8")
    assert store.family_display_name("qwen3.8") == "Qwen 3.8"


def test_touch_family_without_display_name_still_marks_family_known():
    # A family can be "known" (present in the overlay) with no display name
    # yet; the marker must survive so it isn't treated as never-added.
    store = StateStore()
    store.touch_family("qwen3.8")
    assert "qwen3.8" in store.families
    assert store.family_display_name("qwen3.8") == "qwen3.8"


def test_touch_family_preserves_existing_display_name_when_not_given():
    # Re-touching a family without a name (e.g. recording a new model) must
    # not wipe the display name the user already set.
    store = StateStore()
    store.touch_family("qwen3.8", display_name="Qwen 3.8")
    store.touch_family("qwen3.8")
    assert store.family_display_name("qwen3.8") == "Qwen 3.8"


def test_touch_family_empty_display_name_clears_previous_name():
    # An explicit empty string means "clear the name", distinct from None
    # ("not provided"). Without this, an edit modal could never unset a
    # display name short of forget_family, which also drops the known marker.
    store = StateStore()
    store.touch_family("qwen3.8", display_name="Qwen 3.8")
    store.touch_family("qwen3.8", display_name="")
    assert store.family_display_name("qwen3.8") == "qwen3.8"


def test_forget_family_removes_known_entry():
    # Deleting a family must remove its overlay entry so it no longer shows
    # as known and its display name is gone.
    store = StateStore()
    store.touch_family("qwen3.8", display_name="Qwen 3.8")
    store.forget_family("qwen3.8")
    assert "qwen3.8" not in store.families
    assert store.family_display_name("qwen3.8") == "qwen3.8"


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
    store.touch_family("qwen3.8", display_name="Qwen 3.8")
    store.touch_family("empty-family")  # known, no display name yet
    path = tmp_path / "modelman.toml"

    save_state(store, path)
    loaded = load_state(path)

    assert loaded.family_display_name("qwen3.8") == "Qwen 3.8"
    assert "empty-family" in loaded.families
    assert loaded.family_display_name("empty-family") == "empty-family"
