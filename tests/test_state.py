"""modelman.toml — modelman's own per-machine state overlay. Holds only
per-machine mutable state (downloaded/disk_path/size_bytes/litellm_exposed)
keyed by registry model id; registry.toml holds everything that describes
the model itself (see 2026-08-27-shared-model-registry-design.md). The
file is optional: a fresh install has no state yet, so a missing file
returns an empty store rather than raising (matching settings.py's
existing convention for optional per-machine files)."""

from modelman.state import ModelState, StateStore, load_state, save_state


def test_load_state_missing_file_returns_empty_store(tmp_path):
    store = load_state(tmp_path / "nonexistent.toml")
    assert store.models == {}


def test_state_store_get_returns_default_for_unknown_model():
    store = StateStore()
    assert store.get("ollama/x") == ModelState()


def test_save_then_load_round_trips_model_state(tmp_path):
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
