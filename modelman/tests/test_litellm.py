"""Tests for modelman's LiteLLM layer after wt took over the writes.

Row building, config.yaml editing, the launcher-required settings ensure
and the proxy restart all live in Go now (`wt/internal/litellm`), so what
is left to test here is modelman's own half of the contract: the gates it
applies against its in-memory state, the display predicates, the
read-only config helpers `modelman usage` depends on, and the batch
queues' delegation to `wt_bridge`.
"""

from dataclasses import replace

import pytest

from modelman import wt_bridge
from modelman.litellm import (
    ExposeError,
    LiteLLMConfigError,
    ProviderPolicy,
    _database_url_from_config,
    _reverse_model_index,
    _validate_locally,
    apply_expose_queue,
    apply_unexpose_queue,
    expose_model,
    is_cloud,
    is_cloud_effective,
    is_effectively_exposed,
    load_litellm_config,
    passes_ready_gate,
    provider_policy,
)
from modelman.registry import AuthConfig, ModelEntry, ProviderEntry, Registry
from modelman.state import ModelState, StateStore


def _provider(pid, *, base_url=None, secret_ref=None, auth_type="none"):
    return ProviderEntry(
        id=pid,
        name=pid,
        auth=AuthConfig(type=auth_type, base_url=base_url, secret_ref=secret_ref),
    )


def _model(mid, provider_id, model_name, model_info=None, cost=None):
    return ModelEntry(
        id=mid,
        family="f",
        provider_id=provider_id,
        model_name=model_name,
        model_info=model_info or {},
        cost=cost,
    )


def _registry(*provider_ids):
    """Minimal Registry with the given providers (location unset → not
    cloud), so existing predicate tests keep their pre-#46 semantics while
    satisfying the now-required `registry` parameter."""
    return Registry(providers=[_provider(pid) for pid in provider_ids])


class _Bridge:
    """Recorder standing in for `wt_bridge.expose`/`unexpose`.

    Records (verb, ids, litellm_path, kwargs) per call and returns an
    all-applied BridgeResult. `errors` maps a model id to the per-id
    rejection wt would report; `raise_on` makes the named verb raise
    WtBridgeError (wt missing / unreadable config.yaml).
    """

    def __init__(self):
        self.calls: list[tuple] = []
        self.errors: dict[str, str] = {}
        self.warnings: list[str] = []
        self.raise_on: str | None = None
        self.drop_outcomes = False  # simulate a short wt response

    def _run(self, verb, ids, litellm_path, kwargs):
        self.calls.append((verb, list(ids), litellm_path, kwargs))
        if self.raise_on == verb:
            raise wt_bridge.WtBridgeError(f"wt litellm {verb} could not run")
        action = "exposed" if verb == "expose" else "unexposed"
        return wt_bridge.BridgeResult(
            [
                wt_bridge.BridgeOutcome(i, None if i in self.errors else action, self.errors.get(i))
                for i in ids
                if not self.drop_outcomes
            ],
            True,
            list(self.warnings),
        )

    def install(self, monkeypatch):
        monkeypatch.setattr(
            wt_bridge,
            "expose",
            lambda ids, *, litellm_path=None, **kw: self._run("expose", ids, litellm_path, kw),
        )
        monkeypatch.setattr(
            wt_bridge,
            "unexpose",
            lambda ids, *, litellm_path=None, **kw: self._run("unexpose", ids, litellm_path, kw),
        )
        return self


@pytest.fixture
def bridge(monkeypatch):
    """The wt bridge, recorded and stubbed (never runs the real binary)."""
    return _Bridge().install(monkeypatch)


# ---------------------------------------------------------------------------
# Provider table (wt-owned) and the cloud predicates built on it
# ---------------------------------------------------------------------------


def test_is_cloud_reads_wts_provider_table():
    # The TUI's expose gate and wt's config writer must agree on which
    # providers are cloud-exempt, so modelman reads wt's table (`wt litellm
    # providers`) instead of keeping a second copy that could drift.
    assert is_cloud("openrouter") is True
    assert is_cloud("ollama") is False
    # Unknown providers are treated as local (conservative) — wt rejects
    # them anyway.
    assert is_cloud("some-new-provider") is False


def test_provider_policy_is_none_for_unmapped_provider():
    # provider_policy() is the "does this provider have a LiteLLM mapping
    # at all" question the TUI's expose gate and _validate_locally both
    # ask; an unmapped provider must answer None rather than a default
    # policy, or an unroutable row would be queued.
    assert provider_policy("ollama") == ProviderPolicy(cloud=False)
    assert provider_policy("openrouter") == ProviderPolicy(cloud=True)
    assert provider_policy("some-new-provider") is None


def test_display_paths_degrade_instead_of_raising_when_wt_is_missing(monkeypatch):
    # provider_policy/is_cloud run from TUI render code for every row, so a
    # missing or failing wt must never raise there: the table reads empty
    # (warned once by wt_bridge) and everything looks unmapped/non-cloud.
    monkeypatch.setattr(wt_bridge, "provider_cloud_flags", dict)
    assert provider_policy("ollama") is None
    assert is_cloud("openrouter") is False


def test_write_paths_refuse_when_wt_provider_table_is_unavailable(monkeypatch, tmp_path):
    # The write path must NOT inherit the display path's degradation: an
    # empty table means wt could not be run, and reporting that as
    # "provider has no LiteLLM mapping" would blame the registry for a
    # missing binary. It surfaces as LiteLLMConfigError, before any flag flips.
    monkeypatch.setattr(wt_bridge, "provider_cloud_flags", dict)
    registry = Registry(
        providers=[_provider("ollama")],
        models=[_model("ollama/a", "ollama", "a")],
    )
    state = StateStore()
    state.set("ollama/a", ModelState(ready=True))
    with pytest.raises(LiteLLMConfigError, match="wt"):
        expose_model(registry, state, "ollama/a", tmp_path / "config.yaml")
    assert state.get("ollama/a").exposed is False


# ---------------------------------------------------------------------------
# The local gate (_validate_locally)
# ---------------------------------------------------------------------------


def test_validate_locally_accepts_not_ready_location_cloud_model():
    """An ollama model explicitly marked location='cloud' is exempt from
    the ready gate at apply time, matching the TUI EXPOSED column's
    location-aware cloud exemption."""
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))],
        models=[
            ModelEntry(
                id="ollama/kimi-k3:cloud",
                family="f",
                provider_id="ollama",
                model_name="kimi-k3:cloud",
                location="cloud",
            )
        ],
    )
    state = StateStore()
    state.set("ollama/kimi-k3:cloud", ModelState(ready=False))
    _validate_locally(registry, state, "ollama/kimi-k3:cloud")  # must not raise


def test_gate_rejects_native_model_even_when_wt_maps_its_provider(tmp_path, bridge, monkeypatch):
    """Native ⇒ no LiteLLM policy invariant (#47): even when wt's provider
    table maps a native provider (hand-edited registry), a native model is
    rejected before the bridge is called instead of producing an
    unroutable LiteLLM row."""
    monkeypatch.setattr(wt_bridge, "provider_cloud_flags", lambda: {"ollama": False, "agy": False})
    model = _model("agy/contract-fixture:native", "agy", "contract-fixture:native")
    model.native = True
    registry = Registry(providers=[_provider("agy", auth_type="native")], models=[model])
    state = StateStore()
    state.set(model.id, ModelState(ready=True, exposed=False))
    with pytest.raises(ExposeError, match="native"):
        expose_model(registry, state, model.id, tmp_path / "config.yaml")
    # The rejected expose must not flip the flag or reach wt.
    assert state.get(model.id).exposed is False
    assert bridge.calls == []


# ---------------------------------------------------------------------------
# apply_expose_queue — the TUI's batch path
# ---------------------------------------------------------------------------


def _queue_registry():
    registry = Registry(
        providers=[_provider("ollama", base_url="http://localhost:11434")],
        models=[
            _model("ollama/a", "ollama", "a"),
            _model("ollama/b", "ollama", "b"),
            _model("ollama/c", "ollama", "c"),
        ],
    )
    state = StateStore()
    for mid in ("ollama/a", "ollama/b", "ollama/c"):
        state.set(mid, ModelState(ready=True))
    return registry, state


def test_apply_expose_queue_splits_into_one_call_per_direction(tmp_path, bridge):
    # A mixed queue must cost at most two `wt litellm` invocations (one
    # expose batch, one unexpose batch) — per-model calls would spawn wt,
    # rewrite config.yaml and bounce the proxy once per model. Also pins
    # the argv-shaping contract: --skip-ready-gate (modelman applied the
    # gate itself) and the caller's litellm_path forwarded to both calls.
    registry, state = _queue_registry()
    state.set("ollama/c", ModelState(ready=True, exposed=True))
    path = tmp_path / "config.yaml"

    outcomes, warnings = apply_expose_queue(
        registry, state, [("ollama/a", True), ("ollama/c", False), ("ollama/b", True)], path
    )

    assert [(verb, ids, p) for verb, ids, p, _ in bridge.calls] == [
        ("expose", ["ollama/a", "ollama/b"], path),
        ("unexpose", ["ollama/c"], path),
    ]
    assert bridge.calls[0][3] == {"skip_ready_gate": True}
    # Outcomes keep the queue's own order, not the batches'.
    assert outcomes == [
        ("ollama/a", True, None),
        ("ollama/c", False, None),
        ("ollama/b", True, None),
    ]
    assert warnings == []
    assert state.get("ollama/a").exposed is True
    assert state.get("ollama/b").exposed is True
    assert state.get("ollama/c").exposed is False


def test_apply_expose_queue_concatenates_warnings_from_both_batches(tmp_path, bridge):
    # Proxy-restart notices are non-fatal and come back from wt per call;
    # the caller (the TUI event channel) must see the ones from both
    # batches, not just the last.
    registry, state = _queue_registry()
    bridge.warnings = ["restart failed"]
    outcomes, warnings = apply_expose_queue(
        registry, state, [("ollama/a", True), ("ollama/b", False)], tmp_path / "config.yaml"
    )
    assert [o[2] for o in outcomes] == [None, None]
    assert warnings == ["restart failed", "restart failed"]


def test_apply_expose_queue_local_gate_failure_never_reaches_wt(tmp_path, bridge):
    # A model that fails modelman's own gate (not ready) is reported per
    # item with its reason, keeps its flag, and is not handed to wt — the
    # rest of the queue still applies.
    registry, state = _queue_registry()
    state.set("ollama/b", ModelState(ready=False))

    outcomes, _ = apply_expose_queue(
        registry, state, [("ollama/a", True), ("ollama/b", True)], tmp_path / "config.yaml"
    )

    assert bridge.calls[0][1] == ["ollama/a"]
    assert outcomes[0] == ("ollama/a", True, None)
    assert outcomes[1][0:2] == ("ollama/b", True)
    assert "not ready" in outcomes[1][2]
    assert state.get("ollama/a").exposed is True
    assert state.get("ollama/b").exposed is False


def test_apply_expose_queue_per_id_wt_error_leaves_that_flag_alone(tmp_path, bridge):
    # wt can reject one id of a batch (exit 1 with JSON) while applying the
    # others. That rejection becomes this item's outcome error and its flag
    # must stay false, or modelman.toml would claim a route config.yaml
    # never got.
    registry, state = _queue_registry()
    bridge.errors = {"ollama/b": 'model "ollama/b" not found in registry'}

    outcomes, _ = apply_expose_queue(
        registry, state, [("ollama/a", True), ("ollama/b", True)], tmp_path / "config.yaml"
    )

    assert outcomes[0] == ("ollama/a", True, None)
    assert "not found in registry" in outcomes[1][2]
    assert state.get("ollama/a").exposed is True
    assert state.get("ollama/b").exposed is False


def test_apply_expose_queue_bridge_failure_raises_with_no_flag_touched(tmp_path, bridge):
    # A bridge-level failure (wt missing, unreadable config.yaml, garbled
    # output) applied nothing on wt's side either, so it must propagate as
    # LiteLLMConfigError — the type queue.py turns into a whole-batch
    # failure — with every flag untouched.
    registry, state = _queue_registry()
    bridge.raise_on = "expose"

    with pytest.raises(LiteLLMConfigError):
        apply_expose_queue(
            registry, state, [("ollama/a", True), ("ollama/b", True)], tmp_path / "config.yaml"
        )
    assert state.get("ollama/a").exposed is False
    assert state.get("ollama/b").exposed is False


def test_apply_expose_queue_unexpose_batch_bridge_failure_keeps_expose_flags(tmp_path, bridge):
    # The batches are sequential: when the expose batch applied and the
    # unexpose batch then fails at the bridge level, only the unexposes
    # failed. Raising would discard the applied exposes and leave their
    # flags false while config.yaml already carries the routes, so the
    # failure must become a per-id error for the unexposed ids and the
    # applied exposes' flags must still flip.
    registry, state = _queue_registry()
    bridge.raise_on = "unexpose"

    outcomes, _ = apply_expose_queue(
        registry,
        state,
        [("ollama/a", True), ("ollama/b", False), ("ollama/c", True)],
        tmp_path / "config.yaml",
    )

    assert outcomes == [
        ("ollama/a", True, None),
        ("ollama/b", False, "wt litellm unexpose could not run"),
        ("ollama/c", True, None),
    ]
    assert state.get("ollama/a").exposed is True
    assert state.get("ollama/b").exposed is False
    assert state.get("ollama/c").exposed is True


def test_apply_expose_queue_empty_queue_does_nothing(tmp_path, bridge):
    # An empty queue must not spawn wt (and therefore never bounce the
    # proxy) — apply() calls this whenever anything else was queued.
    assert apply_expose_queue(_queue_registry()[0], StateStore(), [], tmp_path / "c.yaml") == (
        [],
        [],
    )
    assert bridge.calls == []


# ---------------------------------------------------------------------------
# apply_unexpose_queue — `modelman stop --all`
# ---------------------------------------------------------------------------


def test_apply_unexpose_queue_is_one_call_for_the_whole_batch(tmp_path, bridge):
    # `stop --all` must bounce the LiteLLM proxy once for the whole batch,
    # not once per exposed model being stopped — one wt call, one restart.
    state = StateStore()
    state.set("ollama/a", ModelState(exposed=True))
    state.set("ollama/b", ModelState(exposed=True))
    path = tmp_path / "config.yaml"

    warnings = apply_unexpose_queue(state, ["ollama/a", "ollama/b"], path)

    assert [(verb, ids, p) for verb, ids, p, _ in bridge.calls] == [
        ("unexpose", ["ollama/a", "ollama/b"], path)
    ]
    assert warnings == []
    assert state.get("ollama/a").exposed is False
    assert state.get("ollama/b").exposed is False


def test_apply_unexpose_queue_no_call_when_empty(tmp_path, bridge):
    # An empty batch (e.g. stop --all with nothing exposed) must not run wt
    # or bounce the proxy at all.
    assert apply_unexpose_queue(StateStore(), [], tmp_path / "config.yaml") == []
    assert bridge.calls == []


def test_apply_unexpose_queue_propagates_bridge_failure(tmp_path, bridge):
    # A bridge-level failure is not a per-model one — it must propagate so
    # the caller (stop_all_local_models) can turn it into a single warning
    # covering the whole batch, with no flag flipped.
    state = StateStore()
    state.set("ollama/a", ModelState(exposed=True))
    bridge.raise_on = "unexpose"
    with pytest.raises(LiteLLMConfigError):
        apply_unexpose_queue(state, ["ollama/a"], tmp_path / "config.yaml")
    assert state.get("ollama/a").exposed is True


# ---------------------------------------------------------------------------
# Read-only config.yaml helpers (`modelman usage`)
# ---------------------------------------------------------------------------


def test_load_litellm_config_invalid_yaml_raises_config_error(tmp_path):
    # A hand-edited config with a YAML syntax error must surface as
    # LiteLLMConfigError (the CLI prints "error: ..."), not a raw
    # yaml.scanner.ScannerError traceback.
    path = tmp_path / "config.yaml"
    path.write_text("model_list:\n  - model_name: [unclosed\n broken: yaml:\n")
    with pytest.raises(LiteLLMConfigError, match="not valid YAML"):
        load_litellm_config(path)


def test_load_litellm_config_missing_raises(tmp_path):
    assert not (tmp_path / "nope.yaml").exists()
    with pytest.raises(LiteLLMConfigError):
        load_litellm_config(tmp_path / "nope.yaml")


def test_load_litellm_config_non_mapping_raises(tmp_path):
    # `modelman usage` indexes the loaded document by key; a config that
    # parses to a list or scalar must be refused with the same error type
    # rather than exploding later in _database_url_from_config.
    path = tmp_path / "config.yaml"
    path.write_text("- just\n- a list\n")
    with pytest.raises(LiteLLMConfigError, match="not a mapping"):
        load_litellm_config(path)


def test_load_litellm_config_reads_a_document_wt_wrote(tmp_path):
    # The read path must still understand the real file shape (comments
    # and all) now that wt, not modelman, produces it.
    path = tmp_path / "config.yaml"
    path.write_text(
        "# hand-written note\n"
        "model_list:\n"
        "- model_name: ollama/a\n"
        "  litellm_params:\n"
        "    model: ollama_chat/a\n"
        "general_settings:\n"
        "  database_url: postgresql://x\n"
    )
    config = load_litellm_config(path)
    assert [r["model_name"] for r in config["model_list"]] == ["ollama/a"]
    assert _database_url_from_config(config) == "postgresql://x"


def test_database_url_from_config_reads_general_settings():
    config = {
        "model_list": [],
        "general_settings": {"database_url": "postgresql://user@localhost/db"},
    }
    assert _database_url_from_config(config) == "postgresql://user@localhost/db"


def test_database_url_from_config_missing_returns_none():
    assert _database_url_from_config({"model_list": []}) is None


def test_reverse_model_index():
    # The reverse index must map each model_list entry's litellm_params.model
    # back to its registry model_name, since that's how NULL-model_name spend
    # rows get resolved.
    model_list = [
        {
            "model_name": "ollama/qwen3.8:27b-mlx",
            "litellm_params": {"model": "ollama_chat/qwen3.8:27b-mlx"},
        },
        {
            "model_name": "openrouter/qwen/qwen3.8-27b",
            "litellm_params": {"model": "openrouter/qwen/qwen3.8-27b"},
        },
        {
            "model_name": "omlx/Qwen3.8-27B-4bit",
            "litellm_params": {"model": "openai/Qwen3.8-27B-4bit"},
        },
    ]
    index = _reverse_model_index(model_list)
    assert index["ollama_chat/qwen3.8:27b-mlx"] == "ollama/qwen3.8:27b-mlx"
    assert index["openrouter/qwen/qwen3.8-27b"] == "openrouter/qwen/qwen3.8-27b"
    assert index["openai/Qwen3.8-27B-4bit"] == "omlx/Qwen3.8-27B-4bit"


def test_reverse_model_index_first_entry_wins_on_duplicate():
    # Two model_list entries can point at the same litellm_params.model; the
    # first entry must win deterministically.
    model_list = [
        {"model_name": "ollama/a", "litellm_params": {"model": "shared/target"}},
        {"model_name": "ollama/b", "litellm_params": {"model": "shared/target"}},
    ]
    index = _reverse_model_index(model_list)
    assert index["shared/target"] == "ollama/a"


def test_reverse_model_index_skips_non_dict_rows():
    # Hand-edited scalar rows in model_list must be ignored, not crash.
    model_list = [
        "just-a-string",
        {"model_name": "ollama/a", "litellm_params": {"model": "m"}},
        {"model_name": "ollama/b"},
    ]
    index = _reverse_model_index(model_list)
    assert index == {"m": "ollama/a"}


# ---------------------------------------------------------------------------
# Display predicates (TUI EXPOSED column / expose gate projection)
# ---------------------------------------------------------------------------


def test_is_effectively_exposed_exposed_and_ready():
    # Flagged and ready is the baseline effective-exposure case; if this
    # ever fails, the TUI's EXPOSED column and LiteLLM routing disagree
    # with the persisted state on the simplest case.
    model = _model("ollama/a", "ollama", "a")
    state = StateStore()
    state.set("ollama/a", ModelState(ready=True, exposed=True))

    assert is_effectively_exposed(model, state, _registry("ollama")) is True


def test_is_effectively_exposed_exposed_not_ready_local():
    # A flagged local model without ready must NOT count as exposed —
    # this is the gate that keeps LiteLLM from routing to a missing
    # artifact; dropping it would break the ready-gate invariant end to end.
    model = _model("ollama/a", "ollama", "a")
    state = StateStore()
    state.set("ollama/a", ModelState(ready=False, exposed=True))

    assert is_effectively_exposed(model, state, _registry("ollama")) is False


def test_is_effectively_exposed_exposed_not_ready_cloud():
    # Cloud models are exempt from the ready gate even when not ready:
    # the same exemption _validate_locally applies at expose time. Pinning
    # it here keeps a flagged cloud row rendering Y in the TUI column.
    model = replace(_model("openrouter/qwen", "openrouter", "qwen"), location="cloud")
    state = StateStore()
    state.set("openrouter/qwen", ModelState(ready=False, exposed=True))

    assert is_effectively_exposed(model, state, _registry("openrouter")) is True


def test_is_effectively_exposed_not_exposed_ready():
    # Ready alone is insufficient — the litellm_exposed flag is the AND's
    # other half; without this pin, a ready-model regression could route
    # models the user never exposed into LiteLLM's config.
    model = _model("ollama/a", "ollama", "a")
    state = StateStore()
    state.set("ollama/a", ModelState(ready=True, exposed=False))

    assert is_effectively_exposed(model, state, _registry("ollama")) is False


def test_is_effectively_exposed_exposed_override():
    # exposed_override projects a queued (not yet persisted) expose toggle:
    # the TUI column must preview the post-queue state. Without the
    # override the persisted False flag must still win.
    model = _model("ollama/a", "ollama", "a")
    state = StateStore()
    state.set("ollama/a", ModelState(ready=True, exposed=False))

    assert is_effectively_exposed(model, state, _registry("ollama"), exposed_override=True) is True
    assert is_effectively_exposed(model, state, _registry("ollama")) is False


def test_is_effectively_exposed_ready_override():
    # ready_override projects a queued ready toggle (the expose→ready
    # cascade): flagged-not-ready flips to exposed only under the override.
    model = _model("ollama/a", "ollama", "a")
    state = StateStore()
    state.set("ollama/a", ModelState(ready=False, exposed=True))

    assert is_effectively_exposed(model, state, _registry("ollama"), ready_override=True) is True
    assert is_effectively_exposed(model, state, _registry("ollama")) is False


def test_is_effectively_exposed_native_not_exposed_not_ready():
    # Native models bypass LiteLLM entirely, so they are always catalog-exposed
    # even when unflagged and not ready. This must match wt's IsExposed.
    model = _model("agy/contract-fixture:native", "agy", "contract-fixture:native")
    model.native = True
    state = StateStore()
    state.set("agy/contract-fixture:native", ModelState(ready=False, exposed=False))

    assert is_effectively_exposed(model, state, _registry("agy")) is True


def test_is_effectively_exposed_native_ignores_exposed_override():
    # Native models are unconditionally exposed: an explicit exposed_override=False
    # (like the persisted flag) must NOT hide a native model — the native
    # short-circuit runs before any override is consulted (issue #48 pin).
    model = _model("agy/contract-fixture:native", "agy", "contract-fixture:native")
    model.native = True
    state = StateStore()
    state.set("agy/contract-fixture:native", ModelState(ready=False, exposed=False))

    assert is_effectively_exposed(model, state, _registry("agy"), exposed_override=False) is True
    assert is_effectively_exposed(model, state, _registry("agy"), ready_override=False) is True


def test_is_effectively_exposed_override_false_hides_non_native():
    # For non-native models the override path works as documented:
    # exposed_override=False overrides a persisted true flag. Cloud location
    # exempts the ready gate, so the override is the only reason this row hides.
    model = replace(_model("ollama/glm-5", "ollama", "glm-5"), location="cloud")
    state = StateStore()
    state.set("ollama/glm-5", ModelState(ready=False, exposed=True))

    assert (
        is_effectively_exposed(model, state, _registry("ollama"), exposed_override=False) is False
    )


def test_passes_ready_gate_local_ready():
    # A ready local model passes the gate apply-time validation enforces
    # (_validate_locally's "model is not ready" rejection must match this).
    model = _model("ollama/a", "ollama", "a")
    state = StateStore()
    state.set("ollama/a", ModelState(ready=True, exposed=False))

    assert passes_ready_gate(model, state, _registry("ollama")) is True


def test_passes_ready_gate_local_not_ready():
    # Not-ready local fails the gate — the core rule that keeps exposes
    # pointing at missing artifacts out of LiteLLM's config.
    model = _model("ollama/a", "ollama", "a")
    state = StateStore()
    state.set("ollama/a", ModelState(ready=False, exposed=True))

    assert passes_ready_gate(model, state, _registry("ollama")) is False


def test_passes_ready_gate_cloud_not_ready():
    # Cloud exemption: a cloud model passes without ready, regardless of
    # the exposure flag (the gate is exposure-agnostic by design — the
    # flag check lives in is_effectively_exposed).
    model = replace(_model("openrouter/qwen", "openrouter", "qwen"), location="cloud")
    state = StateStore()
    state.set("openrouter/qwen", ModelState(ready=False, exposed=True))

    assert passes_ready_gate(model, state, _registry("openrouter")) is True


def test_passes_ready_gate_ready_override():
    # The override projects a queued ready toggle so queue-time checks
    # (_enforce_expose_ready_rule, action_toggle_expose) see the state
    # that will exist after apply, not the stale persisted one.
    model = _model("ollama/a", "ollama", "a")
    state = StateStore()
    state.set("ollama/a", ModelState(ready=False, exposed=True))

    assert passes_ready_gate(model, state, _registry("ollama"), ready_override=True) is True


def _registry_with_providers(*providers):
    """Minimal Registry holding just providers (no models needed by the
    cloud predicates)."""
    return Registry(providers=list(providers))


def test_is_cloud_effective_provider_location_inherited():
    # Issue #46: a model with no location of its own on a
    # location="cloud" provider is effectively cloud, matching wt's
    # ResolveLocation (model location, then provider location).
    model = _model("handmade/x", "handmade", "x")
    registry = _registry_with_providers(
        ProviderEntry(id="handmade", name="Handmade", location="cloud")
    )
    assert is_cloud_effective(model, registry) is True


def test_is_cloud_effective_provider_local_not_cloud():
    model = _model("handmade/x", "handmade", "x")
    registry = _registry_with_providers(
        ProviderEntry(id="handmade", name="Handmade", location="local")
    )
    assert is_cloud_effective(model, registry) is False


def test_is_cloud_effective_unknown_provider_conservative():
    # Unknown provider (hand-edited registry referencing an undefined
    # provider) → not cloud, mirroring is_cloud's conservative fallback.
    model = _model("handmade/x", "handmade", "x")
    registry = _registry_with_providers()
    assert is_cloud_effective(model, registry) is False


def test_is_effectively_exposed_provider_cloud_not_ready():
    # The #46 failure scenario: flag on, ready false, model location
    # unset, provider location cloud → exposed on both sides.
    model = _model("handmade/x", "handmade", "x")
    registry = _registry_with_providers(
        ProviderEntry(id="handmade", name="Handmade", location="cloud")
    )
    state = StateStore()
    state.set("handmade/x", ModelState(ready=False, exposed=True))
    assert is_effectively_exposed(model, state, registry=registry) is True


def test_passes_ready_gate_provider_cloud_not_ready():
    model = _model("handmade/x", "handmade", "x")
    registry = _registry_with_providers(
        ProviderEntry(id="handmade", name="Handmade", location="cloud")
    )
    state = StateStore()
    state.set("handmade/x", ModelState(ready=False, exposed=True))
    assert passes_ready_gate(model, state, registry) is True


# ---------------------------------------------------------------------------
# A wt response with no outcome for a requested id must fail closed
# ---------------------------------------------------------------------------


def test_expose_model_fails_closed_when_wt_returns_no_outcome(tmp_path, bridge):
    # A short/garbled wt response (no outcome for the id) must not be read as
    # success: flipping the flag would claim a route wt never wrote.
    registry, state = _queue_registry()
    bridge.drop_outcomes = True
    with pytest.raises(ExposeError, match="no result for ollama/a"):
        expose_model(registry, state, "ollama/a", tmp_path / "config.yaml")
    assert state.get("ollama/a").exposed is False


def test_apply_expose_queue_fails_closed_when_wt_returns_no_outcome(tmp_path, bridge):
    # Same fail-closed rule for the batch path, both directions: each id gets
    # an error tuple and no flag flips.
    registry, state = _queue_registry()
    state.get("ollama/c").exposed = True
    bridge.drop_outcomes = True
    outcomes, _ = apply_expose_queue(
        registry, state, [("ollama/a", True), ("ollama/c", False)], tmp_path / "config.yaml"
    )
    assert "no result for ollama/a" in outcomes[0][2]
    assert "no result for ollama/c" in outcomes[1][2]
    assert state.get("ollama/a").exposed is False
    assert state.get("ollama/c").exposed is True


def test_apply_unexpose_queue_fails_closed_when_wt_returns_no_outcome(tmp_path, bridge):
    # The un-expose batch must warn and keep the flag when wt says nothing
    # about an id, rather than claiming an un-exposure that never happened.
    _, state = _queue_registry()
    state.get("ollama/a").exposed = True
    bridge.drop_outcomes = True
    warnings = apply_unexpose_queue(state, ["ollama/a"], tmp_path / "config.yaml")
    assert any("no result for ollama/a" in w for w in warnings)
    assert state.get("ollama/a").exposed is True
