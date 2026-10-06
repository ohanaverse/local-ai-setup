"""`modelman migrate` is the one-time CLI entry point for importing legacy
config.yaml + families/*.yaml (and, optionally, wt's
config.toml) into the new registry.toml + modelman.toml. This covers the
command actually writing both output files and reporting what it
imported — the underlying merge logic is covered by tests/test_migrate.py."""

from typer.testing import CliRunner

from modelman.main import app
from modelman.registry import ModelEntry, load_registry, save_registry
from modelman.state import ModelState, load_state, locked_state


def test_migrate_command_writes_registry_and_reports_counts(tmp_path, monkeypatch):
    config_path = tmp_path / "config.yaml"
    config_path.write_text("providers:\n  ollama:\n    type: ollama\n")
    family_dir = tmp_path / "families"
    family_dir.mkdir()
    registry_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    monkeypatch.setenv("MODELMAN_CONFIG", str(config_path))
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(family_dir))
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    monkeypatch.setenv("MODELMAN_WT_CONFIG", str(tmp_path / "no-wt-config.toml"))

    runner = CliRunner()
    result = runner.invoke(app, ["migrate"])

    assert result.exit_code == 0
    assert "Imported 1 providers and 0 models" in result.stdout
    assert "wt config not found" in result.stdout
    assert registry_path.exists()
    assert load_registry(registry_path).provider("ollama").id == "ollama"


def test_migrate_command_preserves_existing_state_on_rerun(tmp_path, monkeypatch):
    """`modelman migrate` is a documented repair step (see wt/CLAUDE.md's
    "unknown provider" note) that users re-run on an already-migrated
    machine. A naive whole-file overwrite of modelman.toml from migrate's
    fresh, mostly-empty StateStore would silently wipe the [litellm] table
    and every model's ready state on that second run; this guards
    against that regression by asserting they survive a re-run."""
    config_path = tmp_path / "config.yaml"
    config_path.write_text("providers:\n  ollama:\n    type: ollama\n")
    family_dir = tmp_path / "families"
    family_dir.mkdir()
    registry_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    monkeypatch.setenv("MODELMAN_CONFIG", str(config_path))
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(family_dir))
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    monkeypatch.setenv("MODELMAN_WT_CONFIG", str(tmp_path / "no-wt-config.toml"))

    runner = CliRunner()
    assert runner.invoke(app, ["migrate"]).exit_code == 0

    # Simulate the user configuring litellm and readying a model after the
    # first migrate — this is the state a repair re-run must not clobber.
    with locked_state(state_path) as state:
        state.extra["litellm"] = {
            "enabled": True,
            "url": "http://localhost:4000",
            "api_key": "sk-real-key",
        }
        state.set("ollama/x", ModelState(ready=True))

    assert runner.invoke(app, ["migrate"]).exit_code == 0

    state = load_state(state_path)
    assert state.extra["litellm"]["url"] == "http://localhost:4000"
    assert state.extra["litellm"]["api_key"] == "sk-real-key"
    assert state.get("ollama/x").ready is True


def test_migrate_command_syncs_routes_once_after_writing(tmp_path, monkeypatch, wt_calls):
    # migrate writes registry.toml (it is the documented repair step), and wt
    # routes every registry cloud model, so it must run one
    # `wt litellm sync` after the save — otherwise repaired models stay
    # unrouted until some unrelated command syncs (#179).
    from modelman import wt_bridge

    config_path = tmp_path / "config.yaml"
    config_path.write_text("providers:\n  ollama:\n    type: ollama\n")
    family_dir = tmp_path / "families"
    family_dir.mkdir()
    registry_path = tmp_path / "registry.toml"
    monkeypatch.setenv("MODELMAN_CONFIG", str(config_path))
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(family_dir))
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))
    monkeypatch.setenv("MODELMAN_WT_CONFIG", str(tmp_path / "no-wt-config.toml"))

    fake = wt_bridge._run
    registry_on_disk_at_sync: list[bool] = []

    def recording(args, env=None, timeout=120):
        registry_on_disk_at_sync.append(registry_path.exists())
        return fake(args, env=env, timeout=timeout)

    monkeypatch.setattr(wt_bridge, "_run", recording)

    assert CliRunner().invoke(app, ["migrate"]).exit_code == 0
    assert wt_calls == [["sync", "--json"]]
    assert registry_on_disk_at_sync == [True]


def _fresh_machine(tmp_path, monkeypatch):
    """No legacy config, no families, no wt config, no registry: what
    `modelman migrate` meets on a machine that has never run modelman."""
    registry_path = tmp_path / "registry.toml"
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "no-config.yaml"))
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(tmp_path / "no-families"))
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))
    monkeypatch.setenv("MODELMAN_WT_CONFIG", str(tmp_path / "no-wt-config.toml"))
    return registry_path


def test_migrate_on_a_fresh_machine_adds_rows_for_installed_providers(tmp_path, monkeypatch):
    """#194: `wt` tells a user with no registry to "seed it with `modelman
    migrate`", and on a fresh machine that wrote `providers = []` — after
    which a pulled model was "unknown model" to `modelman start` and
    invisible to wt, with no command that would create the provider row. The
    command itself, not only the helper it calls, must leave a registry with a
    default row for each local provider whose tool is installed, and say so."""
    registry_path = _fresh_machine(tmp_path, monkeypatch)
    monkeypatch.setattr("modelman.sync._installed_local_providers", lambda: ["omlx", "ollama"])

    result = CliRunner().invoke(app, ["migrate"])

    assert result.exit_code == 0, result.stdout
    registry = load_registry(registry_path)
    assert [p.id for p in registry.providers] == ["ollama", "omlx"]
    assert registry.provider("ollama").auth.base_url == "http://localhost:11434"
    assert registry.provider("ollama").location == "local"
    assert "registry.toml now has 2 providers and 0 models." in result.stdout
    assert "Added provider entries: ollama, omlx" in result.stdout

    # A re-run leaves the same rows, not duplicates, and announces nothing:
    # migrate reads the registry on disk, where the rows already are (#234).
    again = CliRunner().invoke(app, ["migrate"])
    assert again.exit_code == 0
    assert [p.id for p in load_registry(registry_path).providers] == ["ollama", "omlx"]
    assert "Added provider entries" not in again.stdout


def test_migrate_on_a_fresh_machine_with_nothing_installed_adds_no_rows(tmp_path, monkeypatch):
    """A provider whose tool is not installed gets no row: wt would probe a
    server the machine does not have. The registry is still written, so the
    "registry missing" state ends either way."""
    registry_path = _fresh_machine(tmp_path, monkeypatch)
    monkeypatch.setattr("modelman.sync._installed_local_providers", lambda: [])

    result = CliRunner().invoke(app, ["migrate"])

    assert result.exit_code == 0, result.stdout
    assert load_registry(registry_path).providers == []
    assert "Added provider entries" not in result.stdout


def test_migrate_rerun_keeps_a_model_added_since_the_first_run(tmp_path, monkeypatch):
    """#234: migrate is the documented repair step, and it rebuilt
    registry.toml from the legacy inputs alone — so on a machine whose models
    were all added after the first migrate (TUI, `ollama-catalog sync`, by
    hand), the re-run wrote `models = []`."""
    registry_path = _fresh_machine(tmp_path, monkeypatch)
    monkeypatch.setattr("modelman.sync._installed_local_providers", lambda: ["ollama"])
    assert CliRunner().invoke(app, ["migrate"]).exit_code == 0
    registry = load_registry(registry_path)
    registry.models.append(
        ModelEntry(id="ollama/x", family="x", provider_id="ollama", model_name="x:latest")
    )
    save_registry(registry, registry_path)

    again = CliRunner().invoke(app, ["migrate"])

    assert again.exit_code == 0, again.stdout
    assert [m.id for m in load_registry(registry_path).models] == ["ollama/x"]
    assert "Imported 0 providers and 0 models" in again.stdout
    assert "registry.toml now has 1 providers and 1 models." in again.stdout


def test_migrate_rerun_keeps_an_edited_entry_over_the_legacy_one(tmp_path, monkeypatch):
    """The legacy inputs are the stale side: an entry the import produces
    again must not replace the one on disk, which may have been edited since."""
    registry_path = _fresh_machine(tmp_path, monkeypatch)
    config_path = tmp_path / "config.yaml"
    config_path.write_text("providers:\n  ollama:\n    type: ollama\n")
    monkeypatch.setenv("MODELMAN_CONFIG", str(config_path))
    assert CliRunner().invoke(app, ["migrate"]).exit_code == 0
    registry = load_registry(registry_path)
    registry.provider("ollama").name = "Edited since"
    save_registry(registry, registry_path)

    again = CliRunner().invoke(app, ["migrate"])

    assert again.exit_code == 0, again.stdout
    registry = load_registry(registry_path)
    assert [p.id for p in registry.providers] == ["ollama"]
    assert registry.provider("ollama").name == "Edited since"
    assert "Imported 0 providers and 0 models" in again.stdout


def test_migrate_leaves_an_unreadable_registry_alone(tmp_path, monkeypatch, wt_calls):
    """A registry that cannot be read is not a missing one: overwriting it
    would discard whatever a hand repair could still recover."""
    registry_path = _fresh_machine(tmp_path, monkeypatch)
    registry_path.write_text("[[models]\nthis is not toml")

    result = CliRunner().invoke(app, ["migrate"])

    assert result.exit_code == 1
    assert "registry.toml" in result.output
    assert registry_path.read_text() == "[[models]\nthis is not toml"
    assert wt_calls == []


def test_migrate_reports_a_wrongly_shaped_registry_instead_of_a_traceback(
    tmp_path, monkeypatch, wt_calls
):
    """Valid TOML whose values have the wrong shape (a hand edit) fails in the
    parser with a TypeError/ValueError, and bytes that are not UTF-8 with a
    UnicodeDecodeError — none of them a RegistryError or a TOMLDecodeError.
    Each is still "cannot read": the same message, and the file left alone."""
    registry_path = _fresh_machine(tmp_path, monkeypatch)
    for content in (
        b"providers = 3\n",
        b'[[providers]]\nid = "ollama"\nauth = "none"\n',
        b'[[models]]\nid = "a"\nfamily = "a"\nprovider_id = "ollama"\n'
        b'model_name = "a"\nmodel_info = "x"\n',
        b"\xff\xfe not utf-8",
    ):
        registry_path.write_bytes(content)

        result = CliRunner().invoke(app, ["migrate"])

        assert result.exit_code == 1, content
        assert f"error: cannot read {registry_path}" in result.output, content
        assert registry_path.read_bytes() == content
    assert wt_calls == []


def test_migrate_names_the_pre_xdg_registry_it_could_not_read(tmp_path, monkeypatch):
    """With XDG_CONFIG_HOME set and no registry there, the one that is read is
    the pre-XDG file in ~/.config. When that one is unreadable the message
    must name it, not the XDG path where there is nothing to fix."""
    _fresh_machine(tmp_path, monkeypatch)
    home = tmp_path / "home"
    monkeypatch.setenv("HOME", str(home))
    monkeypatch.delenv("MODELMAN_REGISTRY")
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg"))
    legacy = home / ".config" / "local-ai" / "registry.toml"
    legacy.parent.mkdir(parents=True)
    legacy.write_text("[[models]\nthis is not toml")

    result = CliRunner().invoke(app, ["migrate"])

    assert result.exit_code == 1
    assert f"error: cannot read {legacy}" in result.output
    assert not (tmp_path / "xdg" / "local-ai" / "registry.toml").exists()


def test_migrate_rerun_keeps_the_state_of_a_model_it_imports_again(tmp_path, monkeypatch):
    """The legacy download markers are as stale as the legacy registry
    entries: a re-run must not put `ready`/`disk_path` back to what they were
    at the first migrate, or drop the `running` flag of a model that is up."""
    from modelman.manifest import FamilyManifest, VariantSpec, save_manifest

    _fresh_machine(tmp_path, monkeypatch)
    family_dir = tmp_path / "families"
    family_dir.mkdir()
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(family_dir))
    manifest = FamilyManifest(
        family="qwen",
        variants=[VariantSpec(id="q1", provider="omlx", name="qwen-4bit", repo="org/qwen-4bit")],
    )
    manifest.downloaded["q1"] = {"local_path": "/old/qwen-4bit"}
    save_manifest(manifest, family_dir / "qwen.yaml")
    state_path = tmp_path / "modelman.toml"

    assert CliRunner().invoke(app, ["migrate"]).exit_code == 0
    first = load_state(state_path).get("omlx/qwen-4bit")
    assert (first.ready, first.disk_path) == (True, "/old/qwen-4bit")

    with locked_state(state_path) as state:
        state.set(
            "omlx/qwen-4bit",
            ModelState(ready=True, disk_path="/new/qwen-4bit", size_bytes=7, running=True),
        )

    assert CliRunner().invoke(app, ["migrate"]).exit_code == 0

    assert load_state(state_path).get("omlx/qwen-4bit") == ModelState(
        ready=True, disk_path="/new/qwen-4bit", size_bytes=7, running=True
    )


def test_merge_imported_adds_an_id_the_import_repeats_only_once():
    """wt's config.toml is imported entry by entry with no check on model
    ids, so the import itself can carry one twice; "does not already have"
    covers what this same merge just added."""
    from modelman.main import _merge_imported
    from modelman.registry import Registry

    def model():
        return ModelEntry(id="ollama/x", family="x", provider_id="ollama", model_name="x:latest")

    registry = Registry()

    assert _merge_imported(registry, Registry(models=[model(), model()])) == (0, 1)
    assert [m.id for m in registry.models] == ["ollama/x"]
