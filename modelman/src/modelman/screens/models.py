"""ModelScreen — drill into a family's models grouped by provider."""

from __future__ import annotations

import contextlib
from collections.abc import Callable
from pathlib import Path
from typing import TYPE_CHECKING

from rich.text import Text
from textual.app import ComposeResult
from textual.binding import Binding
from textual.css.query import NoMatches
from textual.screen import Screen
from textual.widgets import DataTable, Footer, Header, Static

from ..litellm import (
    default_litellm_config_path,
    is_effectively_exposed,
    passes_ready_gate,
    provider_policy,
)
from ..queue import PendingChanges
from ..registry import (
    DEFAULT_PROVIDER_IDS,
    LOCATION_CLOUD,
    LOCATION_LOCAL,
    Cost,
    DraftSpec,
    Fetch,
    ModelEntry,
    Registry,
    _cost_from_dict,
    is_native_provider,
    known_families,
    model_entry_to_variant,
    provider_config,
    save_registry,
)
from ..state import ModelState, StateStore, load_state, locked_state
from . import reconcile_model_state, reload_preserving_cursor
from .forms import default_form_kind

if TYPE_CHECKING:
    from ..providers.base import VariantSpec


def _variant_to_model_entry(variant: dict, *, family: str, registry: Registry) -> ModelEntry:
    """Convert a ModelForm VariantSpec-shaped dict to a ModelEntry.

    The dialog still emits the legacy TypedDict shape (provider, name,
    repo, files, model_info); the screen needs a ModelEntry to insert
    into registry.models. This adapter keeps the form simple and
    isolates the shape translation here.

    Edit mode preserves `variant["id"]` (the immutable key the user
    sees in the picker); add mode derives the same `provider/name`
    shape ModelForm produced. We don't need a separate "id derivation"
    step — the form already gave us one.
    """
    provider_id = variant["provider"]
    # Sanity: provider must exist in the registry. Defends against a
    # malformed dialog result that snuck past form validation.
    provider = registry.provider(provider_id)  # raises KeyError if unknown

    name = variant.get("name") or variant["id"]
    repo = variant.get("repo")
    files = variant.get("files")
    quantizations = variant.get("quantizations")
    local_path = variant.get("local_path")
    fetch = None
    if repo or files or quantizations or local_path:
        fetch = Fetch(repo=repo, files=files, quantizations=quantizations, local_path=local_path)

    # mlx_lm_server's target+draft pairing: the draft side is stored
    # separately from `fetch` (which stays the target's source). Same
    # truthiness-guard style as `fetch` above — omit `draft` entirely
    # when the form submitted neither draft key.
    draft_repo = variant.get("draft_repo")
    draft_local_path = variant.get("draft_local_path")
    draft = None
    if draft_repo or draft_local_path:
        draft = DraftSpec(repo=draft_repo, local_path=draft_local_path)

    model_info = dict(variant.get("model_info") or {})
    cost_raw = variant.get("cost")
    cost: Cost | None = None
    if cost_raw is not None:
        cost = cost_raw if isinstance(cost_raw, Cost) else _cost_from_dict(cost_raw)
    return ModelEntry(
        id=variant["id"],
        family=family,
        provider_id=provider_id,
        model_name=name,
        location=variant.get("location"),
        source="curated",
        cost=cost,
        model_info=model_info,
        fetch=fetch,
        draft=draft,
        quantization=variant.get("quantization"),
        # native is derived (never serialized) — re-derive it here so an
        # in-session add/edit doesn't reset the flag and flip the EXPOSED
        # column until the next disk reload. Mirrors _derive_native.
        native=is_native_provider(provider),
    )


def _human_size(n) -> str:
    if n is None:
        return "—"
    if n < 1024:
        return f"{n} B"
    for unit in ("KB", "MB", "GB", "TB"):
        n /= 1024
        if n < 1024:
            return f"{n:.1f} {unit}"
    return f"{n:.1f} PB"


def _format_price(value: float | None) -> str:
    """Format a single price with a dollar sign.

    Always shows at least two decimal places. Fractional cents are
    preserved, and trailing zeros beyond two decimals are stripped.
    """
    if value is None:
        return "-"
    s = f"{value:.10f}".rstrip("0").rstrip(".")
    if "." not in s:
        s += ".00"
    else:
        integer_part, decimal_part = s.split(".")
        if len(decimal_part) < 2:
            decimal_part = decimal_part.ljust(2, "0")
        s = f"{integer_part}.{decimal_part}"
    return f"${s}"


def _format_per_token(cost: Cost | None) -> str:
    """COST column: input/cache/output per-million-token prices."""
    if cost is None:
        return "-"
    prices = (
        cost.input_price_per_million,
        cost.cache_price_per_million,
        cost.output_price_per_million,
    )
    if all(p is None for p in prices):
        return "-"
    return f"${'/'.join(_format_price(p).lstrip('$') for p in prices)}"


def _format_subscription(cost: Cost | None) -> str:
    """SUB column: subscription price abbreviated as mo/yr."""
    if cost is None or cost.subscription_price is None:
        return "-"
    suffix = "mo" if cost.subscription_period == "month" else "yr"
    return f"{_format_price(cost.subscription_price)}/{suffix}"


def _format_location(location: str | None) -> str:
    """LOC column icon: cloud, local, or unknown."""
    if location is None or location == "":
        return "—"
    if location == LOCATION_CLOUD:
        return "↗"
    if location == LOCATION_LOCAL:
        return "▤"
    return location


def _entry_kwargs(m: ModelEntry) -> dict:
    """Deep-copy a ModelEntry to kwargs so snapshot copies don't share
    nested Fetch/Cost objects with the live registry."""
    from copy import deepcopy

    return deepcopy(m).__dict__


def _state_kwargs(s: ModelState) -> dict:
    from dataclasses import asdict

    return asdict(s)


class ModelScreen(Screen[None]):
    BINDINGS = [
        ("escape", "back", "Back"),
        ("a", "add_model", "Add"),
        ("d", "delete_model", "Delete"),
        ("e", "edit_model", "Edit"),
        Binding("enter", "select_row", "Edit", priority=True),
        ("g", "open_downloads", "Downloads"),
        ("r", "toggle_ready", "Toggle ready"),
        ("x", "toggle_expose", "Toggle exposed"),
    ]

    def __init__(
        self,
        registry: Registry,
        state: StateStore,
        family: str,
        registry_path: Path,
        state_path: Path,
        available_providers: list[str] | None = None,
    ) -> None:
        super().__init__()
        self.registry = registry
        self.state = state
        self.family = family
        self.registry_path = registry_path
        self.state_path = state_path
        # The list of providers configured in ~/.config/local-ai/config.yaml.
        # The provider-table on the left always shows every entry here, even
        # when the family has no models at all — otherwise an empty family
        # would show no providers and the user would have nowhere to click
        # 'a' to add the first one. Order is preserved as given (config
        # insertion order) and falls back to a stable default only if no
        # list was provided, so the cursor lands on the user's "first
        # choice" rather than alphabetical.
        if available_providers is not None:
            self.available_providers: list[str] = list(available_providers)
        else:
            # Default provider order for the Add dialog; DEFAULT_PROVIDER_IDS
            # keeps this in lockstep with the reconcilable provider set.
            self.available_providers = list(DEFAULT_PROVIDER_IDS)
        # Default selection: ollama if configured, otherwise the first
        # configured provider. This keeps the cursor on the most
        # common starting point for empty families.
        # Default selection: ollama if configured, otherwise the first
        # configured provider. This keeps the cursor on the most
        # common starting point for empty families.
        # Provider of the last model added or edited this session; used to
        # default the Add dialog's provider dropdown now that there's no
        # provider pane to inherit a selection from.
        self._last_provider_used: str | None = None
        # queued_ready / queued_deletes map model_id -> target state.
        # queued_ready values are bools: True to ready, False to clear.
        self.queued_ready: dict[str, bool] = {}
        self.queued_deletes: dict[str, VariantSpec] = {}
        # model_id -> target exposed state (True to expose, False to unexpose).
        self.queued_exposes: dict[str, bool] = {}
        # Ids whose queued_ready=True entry exists *only* because
        # action_toggle_expose cascaded it in (the model wasn't ready and
        # had no queued_ready entry of its own yet). Cancelling either half
        # of such a pair must cancel the other half too, or apply() ends up
        # running a download the user un-queued or an expose that fails
        # with "model is not ready". A ready entry the user set explicitly
        # (via 'r', or already present before the cascade) is never marked
        # here, so undoing it never touches an independently-queued expose.
        self._ready_cascade_for_expose: set[str] = set()
        # model_id -> target family. Applied to the registry at apply()
        # time; the in-memory family is untouched until then so the row
        # stays visible in this family's table with a → glyph.
        self.queued_moves: dict[str, str] = {}
        # Ids of models created this session via the add dialog. Used by
        # _restore_snapshot: a model added into a *different* family
        # isn't caught by the family-scoped restore filter.
        self._added_ids: set[str] = set()
        # Snapshot for discard: restore if the user exits without applying.
        self._snapshot_models: list[ModelEntry] = [
            ModelEntry(**_entry_kwargs(m)) for m in registry.models if m.family == family
        ]
        self._snapshot_state_entries: dict[str, ModelState] = {
            mid: ModelState(**_state_kwargs(s))
            for mid, s in state.models.items()
            if any(m.id == mid and m.family == family for m in registry.models)
        }

    def compose(self) -> ComposeResult:
        yield Header()
        yield DataTable(id="model-table", cursor_type="row")
        yield Static("path: —", id="details-panel")
        yield Static("Pending: ready 0 · delete 0", id="pending-bar")
        yield Footer()

    def on_mount(self) -> None:
        # Captured once while mounted: `self.app` (Screen.app) resolves via
        # a contextvar that isn't set on a background thread, falling back
        # to walking self._parent up to the App — a walk that raises
        # NoActiveAppError once this screen is popped. _run_apply and the
        # deferred-expose closure it registers both run later, on
        # StatusScreen's worker thread, after ModelScreen has already been
        # popped (_push_status_screen pops before pushing StatusScreen) —
        # they must use this reference instead of `self.app`.
        self._app_ref = self.app
        mt = self.query_one("#model-table", DataTable)
        mt.add_columns(
            "FAMILY",
            "PROVIDER",
            "MODEL",
            "LOC",
            "STATUS",
            "EXPOSED",
            "COST",
            "SUB",
            "SIZE",
        )
        self.reload()
        self._refresh_pending_bar()
        mt.focus()
        self.run_worker(self._run_reconcile, exclusive=True, thread=True)
        self._last_poll_had_active = False
        self.set_interval(1.0, self._poll_downloads)

    def _run_reconcile(self) -> None:
        """Ask each provider whether its models are on disk; write the
        result straight into `state` for local-artifact models. Files
        present -> ready=True + disk_path + size_bytes; absent ->
        ready=False + cleared path/size. Non-local-artifact models
        (cloud-located, or on a cloud provider) are left alone by this
        step — only disk_path/size_bytes are opportunistically updated
        when the provider reports them; their ready flag is driven by
        the ready-toggle's apply-time download/pull instead.

        Delegates to the shared reconcile_model_state (screens/__init__.py)
        so the write semantics can't drift from FamilyScreen's version.
        """
        reconcile_model_state(
            self.registry.models_by_family(self.family), self.registry, self.state
        )
        # Re-render on the main thread.
        self.app.call_from_thread(self.reload)

    def _poll_downloads(self) -> None:
        """Reload state from disk while any download is active (or just
        finished, to catch the final transition), so DownloadManager's
        writes — which land on disk, not on this screen's in-memory
        StateStore — become visible without the user navigating away and
        back. Cheap: modelman.toml is small and this only runs while
        downloads exist."""
        active = self.app.downloads.has_active()  # type: ignore[attr-defined]
        if not active and not self._last_poll_had_active:
            return
        self._last_poll_had_active = active
        try:
            self.state = load_state(self.state_path)
        except Exception:  # noqa: BLE001
            return
        self.reload()

    def reload(self) -> None:
        self._load_models()

    def _load_models(self) -> None:
        try:
            mt = self.query_one("#model-table", DataTable)
        except NoMatches:
            # Screen already popped — e.g. a background download's
            # on_complete callback (_on_download_finished) or the 1s
            # _poll_downloads timer fired after Escape closed this screen
            # while an untracked download was still in flight.
            return

        def _repopulate() -> None:
            mt.clear()
            models = sorted(
                self.registry.models_by_family(self.family),
                key=lambda m: (m.provider_id, m.model_name),
            )
            for m in models:
                ready = self._is_ready(m.id)
                size_str = _human_size(self.state.get(m.id).size_bytes) if ready else "—"
                if m.id in self.queued_deletes:
                    status = "[red]✗[/red]"
                elif self.app.downloads.is_downloading(m.id):  # type: ignore[attr-defined]
                    status = "[cyan]⏳[/cyan]"
                elif m.id in self.queued_ready:
                    status = (
                        "[yellow]↓[/yellow]" if self.queued_ready[m.id] else "[yellow]↑[/yellow]"
                    )
                elif m.id in self.queued_moves:
                    status = "[magenta]→[/magenta]"
                elif ready:
                    status = "[green]✓[/green]"
                else:
                    status = "[dim]○[/dim]"
                # Use the queued expose value as the override for projected state.
                exposed_override = self.queued_exposes.get(m.id)
                # Use the projected ready value for the EXPOSED column preview.
                ready_override = self._projected_ready(m.id)
                # Effective exposure = the (queued or persisted) flag AND the
                # *projected* ready value — the same gate `_validated_entry`
                # applies at apply time, so the column shows what the model
                # will be after apply, not what it was before the queue.
                # Native rows are the one exception: the column shows Y
                # unconditionally while the apply gate still rejects them
                # ("no LiteLLM mapping") — that split is deliberate.
                exposed_str = (
                    "Y"
                    if is_effectively_exposed(
                        m,
                        self.state,
                        self.registry,
                        exposed_override=exposed_override,
                        ready_override=ready_override,
                    )
                    else "–"
                )
                mt.add_row(
                    m.family,
                    m.provider_id,
                    m.model_name,
                    _format_location(m.location),
                    status,
                    exposed_str,
                    _format_per_token(m.cost),
                    _format_subscription(m.cost),
                    size_str,
                    key=m.id,
                )

        reload_preserving_cursor(mt, _repopulate)
        self._refresh_details_panel(mt.cursor_row)

    def _is_ready(self, model_id: str) -> bool:
        """Truth about whether a model is ready to use — a pure read of
        state.ready. Two writers: reconcile (on mount/resume) sets it from
        disk truth for local-artifact models, and the ready toggle's apply
        step writes it in both directions (ready-on downloads or flips the
        flag, ready-off clears the artifact and the flag) for models in
        any location."""
        return self.state.get(model_id).ready

    def _projected_ready(self, model_id: str) -> bool:
        """The ready value this model will have after apply() (or, for a
        model routed through DownloadManager instead of the queue, after
        its download finishes): the queued target if one exists,
        otherwise True while actively downloading, otherwise the
        persisted flag."""
        if model_id in self.queued_ready:
            return self.queued_ready[model_id]
        if self.app.downloads.is_downloading(model_id):  # type: ignore[attr-defined]
            return True
        return self.state.get(model_id).ready

    def _enforce_expose_ready_rule(self, mid: str, entry: ModelEntry) -> None:
        """Single invariant: expose depends on ready. A queued expose=True
        cannot survive a model whose projected ready is False — apply()
        enforces the same rule at the gate (_validated_entry rejects the
        expose with 'model is not ready'); this keeps the queue consistent
        with it instead of leaving a doomed entry for apply() to fail on.
        Cloud rows are exempt, matching _validated_entry (via
        passes_ready_gate). Drops the expose with a notification rather
        than silently overwriting the user's request."""
        if self.queued_exposes.get(mid) is True and not passes_ready_gate(
            entry,
            self.state,
            self.registry,
            ready_override=self._projected_ready(mid),
        ):
            self.queued_exposes.pop(mid, None)
            self.app.notify(f"Expose cancelled: {mid} will not be ready")

    def _cancel_ready_cascade(self, mid: str) -> None:
        """Undo an expose-triggered ready cascade: cancel a running
        download if that's what the cascade used (mapped provider), or
        drop the queued flag-only ready-on otherwise. Shared by the
        repeated-'x'-keypress cancel path and discard (Discard-cancels-
        cascade)."""
        if self.app.downloads.is_downloading(mid):  # type: ignore[attr-defined]
            self.app.downloads.cancel(mid)  # type: ignore[attr-defined]
        self.queued_ready.pop(mid, None)
        self._ready_cascade_for_expose.discard(mid)

    def _refresh_pending_bar(self) -> None:
        try:
            bar = self.query_one("#pending-bar", Static)
        except NoMatches:
            return  # screen already popped (see _load_models)
        bar.update(
            f"Pending: ready {len(self.queued_ready)} · delete {len(self.queued_deletes)}"
            f" · move {len(self.queued_moves)} · expose {len(self.queued_exposes)}"
        )

    def action_toggle_ready(self) -> None:
        mt = self.query_one("#model-table", DataTable)
        if mt.row_count == 0:
            return
        row_key = list(mt.rows.keys())[mt.cursor_row]
        mid = str(row_key.value)
        entry = next((m for m in self.registry.models if m.id == mid), None)
        if entry is None:
            return

        if self.app.downloads.is_downloading(mid):  # type: ignore[attr-defined]
            # Three-way 'r': downloading -> cancel. The model's readiness
            # was transient (it only becomes ready when the download
            # finishes), so a queued expose — whether cascade-marked or
            # queued while the model merely looked ready-by-projection —
            # would be doomed once the download stops. Drop both halves.
            self.app.downloads.cancel(mid)  # type: ignore[attr-defined]
            self.queued_exposes.pop(mid, None)
            self._ready_cascade_for_expose.discard(mid)
            self.app.notify(f"Cancelling download: {mid}")
            self.reload()
            return

        persisted_ready = self.state.get(mid).ready
        displayed_ready = self.queued_ready.get(mid, persisted_ready)
        target = not displayed_ready
        if target == persisted_ready:
            # Repeated keypress: this target is exactly what's already on
            # disk once any queued flip is dropped. Cancel it instead of
            # re-queuing a no-op.
            self.queued_ready.pop(mid, None)
            if mid in self._ready_cascade_for_expose:
                # This readiness was only queued because a prior 'x' press
                # cascaded it in for an expose; cancel that expose with it.
                self.queued_exposes.pop(mid, None)
                self._ready_cascade_for_expose.discard(mid)
            # The invariant covers every other case: a user-queued expose
            # stranded by this cancel is dropped (with a notification).
            self._enforce_expose_ready_rule(mid, entry)
            self.app.notify(f"Model already {'ready' if target else 'not ready'}")
            self._refresh_pending_bar()
            self.reload()
            return

        if target and self._provider_can_download(entry.provider_id):
            # A real download (ollama/omlx/llamacpp, local or cloud):
            # start it immediately in the background instead of queuing
            # it — it is never applied by PendingChanges.apply() (Task 4).
            self._start_download(entry)
            return

        # Flag-only provider (native/unmapped) ready-on, or any ready-off:
        # unchanged apply-on-exit queue behavior. apply() owns the
        # consequences — it removes the artifact (provider delete, or the
        # recorded disk_path for flag-only providers) on ready-off and
        # re-derives the unexpose cascade from the persisted exposure flag
        # — and the invariant below drops any queued expose the new ready
        # value makes impossible, so the screen queue can never strand a
        # doomed expose.
        self.queued_ready[mid] = target
        self._enforce_expose_ready_rule(mid, entry)
        self._last_provider_used = entry.provider_id
        self._refresh_pending_bar()
        self.reload()

    def action_toggle_expose(self) -> None:
        mt = self.query_one("#model-table", DataTable)
        if mt.row_count == 0:
            return
        row_key = list(mt.rows.keys())[mt.cursor_row]
        mid = str(row_key.value)
        entry = next((m for m in self.registry.models if m.id == mid), None)
        if entry is None:
            return
        if provider_policy(entry.provider_id) is None:
            self.app.notify("Provider has no LiteLLM mapping — cannot expose")
            return
        persisted_exposed = self.state.get(mid).litellm_exposed
        displayed_exposed = self.queued_exposes.get(mid, persisted_exposed)
        target = not displayed_exposed
        if target == persisted_exposed:
            # Repeated keypress: cancel the queued expose toggle.
            self.queued_exposes.pop(mid, None)
            if mid in self._ready_cascade_for_expose:
                # The download was only queued to serve this expose; the
                # user never asked for it independently.
                self._cancel_ready_cascade(mid)
            self.app.notify(f"Model already {'exposed' if target else 'not exposed'}")
            self._refresh_pending_bar()
            self.reload()
            return
        if target and not passes_ready_gate(
            entry,
            self.state,
            self.registry,
            ready_override=self._projected_ready(mid),
        ):
            # Exposing requires ready — the same gate _validated_entry
            # applies at apply time. If the user has a ready toggle queued
            # that leaves the model not-ready, refuse rather than overwrite
            # their request; otherwise cascade the download in: a mapped
            # provider's ready-on starts the real download immediately
            # (DownloadManager), a flag-only provider's is queued (apply
            # runs the ready loop before the expose loop, so the order
            # works).
            if mid in self.queued_ready:
                self.app.notify(
                    "Model is queued to be made not ready — cancel that before exposing"
                )
                return
            if self._provider_can_download(entry.provider_id):
                self._start_download(entry)
            else:
                self.queued_ready[mid] = True
            self._ready_cascade_for_expose.add(mid)
        self.queued_exposes[mid] = target
        self._refresh_pending_bar()
        self.reload()

    def _provider_entry_or_none(self, provider_id: str):
        try:
            return self.registry.provider(provider_id)
        except KeyError:
            return None

    def _provider_can_download(self, provider_id: str) -> bool:
        """True when a ready-on against this provider is a real
        download/pull and must go through DownloadManager: the provider
        has a registered Provider class (ollama/omlx/llamacpp). This is
        deliberately NOT model_has_local_artifact — an ollama *cloud*
        model has no local artifact but its ready-on still runs a real
        `ollama pull` (that's what registers the tag), so it must route
        through DownloadManager too; native/unmapped providers have no
        Provider class and keep the queued flag flip. Mirrors _run_apply's
        try/except-KeyError flag-only rule."""
        if self._provider_entry_or_none(provider_id) is None:
            return False
        from ..providers.registry import ProviderRegistry

        return provider_id in ProviderRegistry.available()

    def _start_download(self, entry: ModelEntry) -> None:
        """Route a real ready-on through DownloadManager instead of the
        apply-on-exit queue — the download starts immediately in the
        background and PendingChanges.apply() never touches it (Task 4's
        assertion enforces this)."""
        variant = model_entry_to_variant(entry)
        config = provider_config(self.registry.provider(entry.provider_id))

        def _on_complete(local_path: str, mid: str = entry.id) -> None:
            self.app.call_from_thread(self._on_download_finished, mid)

        self.app.downloads.start(  # type: ignore[attr-defined]
            entry.id, variant, config, on_complete=_on_complete, registry=self.registry
        )
        self._last_provider_used = entry.provider_id
        self._refresh_pending_bar()
        self.reload()

    def _on_download_finished(self, model_id: str) -> None:
        """Best-effort immediate refresh when this screen started the
        download that just finished. The 1s poll (below) is the
        authoritative fallback for every other case — a download that
        finishes after the initiating ModelScreen was replaced by
        another instance still gets picked up there."""
        try:
            self.state = load_state(self.state_path)
        except Exception:  # noqa: BLE001
            return
        self.reload()
        self._refresh_pending_bar()

    def _provider_list(self) -> list[str]:
        # Use the full configured-provider list, not just the providers
        # currently used by models in the family, so the user can add
        # the first model for a fresh family via the 'a' dialog.
        # The list is sorted alphabetically so the Add dialog's
        # provider Select and any code that iterates providers sees
        # a stable, predictable order.
        if self.available_providers:
            return sorted(self.available_providers)
        return sorted({m.provider_id for m in self.registry.models_by_family(self.family)})

    def _provider_kinds(self) -> dict[str, str]:
        """Map each registered provider id to the ModelForm 'kind' that
        drives its Location-select lock rule: native providers and
        llamacpp/omlx are locked (cloud and local respectively); ollama
        is the one provider where location is genuinely editable;
        everything else (openrouter, any other unmapped provider) locks
        to cloud. Non-native kinds come from forms.default_form_kind so
        the fallback policy stays in one place."""
        kinds: dict[str, str] = {}
        for p in self.registry.providers:
            if is_native_provider(p):
                kinds[p.id] = "native"
            else:
                kinds[p.id] = default_form_kind(p.id)
        return kinds

    def _families_list(self) -> list[str]:
        """Every family the add/edit dialogs may target: families with
        models in the registry, first-class [[families]] entries, and
        legacy state.families keys, sorted."""
        return known_families(self.registry, self.state)

    def action_add_model(self) -> None:
        from .forms import ModelForm

        providers = self._provider_list() or list(DEFAULT_PROVIDER_IDS)
        # Pre-select the provider the user is currently looking at, so
        # adding "another llamacpp model" doesn't make them switch the
        # dropdown back. Fall back to None if no provider is selected.
        default_provider = (
            self._last_provider_used if self._last_provider_used in providers else None
        )
        self.app.push_screen(
            ModelForm(
                providers=providers,
                default_provider=default_provider,
                families=self._families_list(),
                family=self.family,
                provider_kinds=self._provider_kinds(),
            ),
            self._on_add_model,
        )

    def _on_add_model(self, result) -> None:
        if result is None:
            return
        variant = result.spec
        if any(m.id == variant["id"] for m in self.registry.models):
            self.app.notify("Model ID already exists")
            return
        entry = _variant_to_model_entry(variant, family=result.family, registry=self.registry)
        self.registry.models.append(entry)
        self._added_ids.add(variant["id"])
        # Persist immediately (mirrors _on_edit_model): a real-download
        # ready-on bypasses the apply-on-exit queue entirely via
        # _start_download, so there's no later save point that would
        # otherwise persist this entry.
        save_registry(self.registry, self.registry_path)
        if self._provider_can_download(entry.provider_id):
            # Mapped provider: the ready-on is a real download/pull —
            # start it in the background now instead of queueing it.
            self._start_download(entry)
        else:
            # Flag-only provider or cloud model with no download
            # mechanism: unchanged apply-on-exit queue behavior.
            self.queued_ready[variant["id"]] = True
        self._last_provider_used = variant["provider"]
        self.reload()
        self._refresh_pending_bar()

    def action_delete_model(self) -> None:
        mt = self.query_one("#model-table", DataTable)
        if mt.row_count == 0:
            return
        row_key = list(mt.rows.keys())[mt.cursor_row]
        mid = str(row_key.value)
        entry = next((m for m in self.registry.models if m.id == mid), None)
        if entry is None:
            return
        if self.app.downloads.is_downloading(mid):  # type: ignore[attr-defined]
            # The weights are being written right now; deleting mid-flight
            # would race the download thread. Cancel first instead.
            self.app.notify(f"{mid} is downloading — cancel first")
            return
        # "d" queues only the registry removal. apply()'s deletes loop already
        # removes the on-disk file, drops the registry/state rows, and cascades
        # the unexpose — queueing ready=False/expose=False here would double-
        # delete the file and, on cancel, leave orphaned queues that still
        # destroy the model. A second "d" toggles the delete back off.
        spec = model_entry_to_variant(entry)
        if mid in self.queued_deletes:
            self.queued_deletes.pop(mid)
        else:
            self.queued_deletes[mid] = spec
        self._refresh_pending_bar()
        self.reload()

    def action_edit_model(self) -> None:
        mt = self.query_one("#model-table", DataTable)
        if mt.row_count == 0:
            return
        # cursor_row can briefly exceed row_count during a reload race;
        # bail instead of crashing.
        if mt.cursor_row >= mt.row_count:
            return
        row_key = list(mt.rows.keys())[mt.cursor_row]
        mid = str(row_key.value)
        entry = next((m for m in self.registry.models if m.id == mid), None)
        if entry is None:
            return
        if self.app.downloads.is_downloading(mid):  # type: ignore[attr-defined]
            # An edit that changes the variant mid-download would race the
            # in-flight write; move/edit must wait for the download.
            self.app.notify(f"{mid} is downloading — cancel first")
            return
        from .forms import ModelForm

        spec = model_entry_to_variant(entry)
        self.app.push_screen(
            ModelForm(
                providers=self._provider_list(),
                variant=spec,
                families=self._families_list(),
                family=self.queued_moves.get(mid, self.family),
                provider_kinds=self._provider_kinds(),
            ),
            self._on_edit_model,
        )

    def on_data_table_row_selected(self, event: DataTable.RowSelected) -> None:
        self.action_edit_model()

    def on_data_table_row_highlighted(self, event: DataTable.RowHighlighted) -> None:
        """Keep the details panel in sync with the row under the cursor."""
        self._refresh_details_panel(event.cursor_row)

    def _refresh_details_panel(self, cursor_row: int) -> None:
        """Show the on-disk path of the row under the cursor, from
        state.disk_path; renders an em dash when the model isn't ready
        or its path is unknown.
        """
        try:
            details = self.query_one("#details-panel", Static)
            mt = self.query_one("#model-table", DataTable)
        except NoMatches:
            return  # not mounted (e.g. screen teardown race)
        if cursor_row < 0 or cursor_row >= mt.row_count:
            details.update("path: —")
            return
        row_key = list(mt.rows.keys())[cursor_row]
        mid = str(row_key.value)
        path = self.state.get(mid).disk_path if self._is_ready(mid) else None
        details.update(Text(f"path: {path or '—'}"))

    def action_select_row(self) -> None:
        """Screen-level Enter handler: always edits the row under the
        cursor now that there's only one table."""
        self.action_edit_model()

    def _on_edit_model(self, result) -> None:
        if result is None:
            return
        updated = result.spec
        new_entry = _variant_to_model_entry(updated, family=self.family, registry=self.registry)
        for i, m in enumerate(self.registry.models):
            if m.id == updated["id"]:
                self.registry.models[i] = new_entry
                break
        # Persist the edit's registry metadata (location, model name,
        # fetch split, …) immediately: unlike every other mutating
        # action here, a same-family edit queues nothing, so the
        # escape-apply path is never reached and PendingChanges' final
        # save_registry() would never run. FamilyScreen's on_screen_resume
        # reloads the registry from disk, which would silently drop the
        # edit before the user ever reopened this screen. Family changes
        # from the dialog stay queued as moves and are applied (and saved)
        # at apply time, as before.
        save_registry(self.registry, self.registry_path)
        if result.family != self.family:
            self.queued_moves[updated["id"]] = result.family
        else:
            self.queued_moves.pop(updated["id"], None)
        self._last_provider_used = updated["provider"]
        self.reload()
        self._refresh_pending_bar()

    def has_pending_changes(self) -> bool:
        """True when this screen holds any unapplied queued mutation
        (ready/delete/move/expose). Used by action_back's exit-confirm
        gate and ModelmanApp.request_quit()'s ctrl+q guard, so a queue
        pending here blocks both Escape and quit the same way."""
        return bool(
            self.queued_ready or self.queued_deletes or self.queued_moves or self.queued_exposes
        )

    def action_back(self) -> None:
        if not self.has_pending_changes():
            self.app.pop_screen()
            return
        from .forms import ConfirmExitDialog

        self.app.push_screen(
            ConfirmExitDialog(
                ready=list(self.queued_ready.items()),
                deletes=list(self.queued_deletes.values()),
                exposes=list(self.queued_exposes.items()),
                moves=list(self.queued_moves.items()),
            ),
            self._on_exit_confirm,
        )

    def action_open_downloads(self) -> None:
        from .downloads import DownloadScreen

        self.app.push_screen(DownloadScreen())

    def _on_exit_confirm(self, choice: str | None) -> None:
        if choice == "apply":
            self._push_status_screen()
            return
        if choice == "discard":
            # Snapshot the clear-set BEFORE the cancel loops: they discard
            # ids from _ready_cascade_for_expose as they cancel them, so a
            # union taken afterwards would only see the leftovers — and
            # skip clear_state for every cascade-cancelled download,
            # leaving its stale row in the DownloadScreen.
            to_clear = set(self._ready_cascade_for_expose) | set(self._added_ids)
            # Cancel any download that was only running because an expose
            # cascaded it in (Discard-cancels-cascade) BEFORE restoring the
            # snapshot, so a discarded session leaves no background
            # download running for a change the user walked away from.
            for mid in list(self._ready_cascade_for_expose):
                self._cancel_ready_cascade(mid)
            # Also cancel any download still running for a model added
            # this session: _restore_snapshot() below removes its registry
            # entry, and a download that finishes afterward would persist
            # a dangling modelman.toml row (ready=True) for a model_id no
            # longer in the registry.
            for mid in list(self._added_ids):
                if self.app.downloads.is_downloading(mid):  # type: ignore[attr-defined]
                    self.app.downloads.cancel(mid)  # type: ignore[attr-defined]
            # Clear the download states for all cascade-cancelled/added
            # models so they don't persist in the DownloadScreen after
            # discard. clear_state() is safe to call even if the state
            # doesn't exist.
            for mid in to_clear:
                self.app.downloads.clear_state(mid)  # type: ignore[attr-defined]
            self._restore_snapshot()
            # Same-family edits save registry immediately on _on_edit_model.
            # Restoring the in-memory snapshot is not enough: FamilyScreen
            # reloads from disk on resume, so we must also write the restored
            # registry back to disk to undo any edits the user discarded.
            save_registry(self.registry, self.registry_path)
            # A download that finished mid-session persisted ready=True to
            # disk (DownloadManager._run's locked_state write) for a model
            # this discard just removed from the registry; the in-memory
            # restore alone leaves that dangling row in modelman.toml.
            # Locked read-modify-write merges only this session's added
            # ids onto fresh disk state, so a concurrent DownloadManager
            # completion for an unrelated model survives.
            with contextlib.suppress(Exception), locked_state(self.state_path) as disk_state:
                for mid in self._added_ids:
                    if mid not in self._snapshot_state_entries:
                        disk_state.models.pop(mid, None)
            self.queued_ready.clear()
            self.queued_deletes.clear()
            self.queued_moves.clear()
            self.queued_exposes.clear()
            self._ready_cascade_for_expose.clear()
            self._added_ids.clear()
            self.app.pop_screen()
            return
        # "cancel" or None: stay on the model screen, queue preserved.
        return

    def _push_status_screen(self) -> None:
        """Hand off the apply run to StatusScreen for live progress.

        ModelScreen pops itself; StatusScreen then runs apply() on a
        worker thread and pops back to FamilyScreen when done.
        """
        from .status import StatusScreen

        self.app.pop_screen()
        self.app.push_screen(StatusScreen(family=self.family, run_apply=self._run_apply))

    def _register_deferred_expose(self, model_id: str, litellm_path: Path) -> None:
        """Defer a queued expose against a still-downloading model: it
        can't be applied now (the ready gate would reject it — the model
        isn't ready yet), so register it to run once DownloadManager
        reports success instead. Loads a fresh Registry/StateStore at
        run time rather than closing over self.registry/self.state,
        since this may fire long after this ModelScreen instance is
        gone."""
        registry_path = self.registry_path
        state_path = self.state_path
        app = self._app_ref

        def _apply_deferred_expose() -> None:
            from ..litellm import apply_expose_queue
            from ..registry import load_registry
            from ..state import locked_state

            registry = load_registry(registry_path)
            with locked_state(state_path) as state:
                outcomes, warnings = apply_expose_queue(
                    registry, state, [(model_id, True)], litellm_path
                )
            # This runs on DownloadManager's background thread (via its
            # post-download action, itself inside a contextlib.suppress
            # that would otherwise swallow this silently) — a per-model
            # failure here is returned in outcomes, not raised, so it
            # would never surface to the user without this notify.
            for mid, _target, error in outcomes:
                if error is not None:
                    app.call_from_thread(app.notify, f"Expose failed for {mid}: {error}")  # type: ignore[attr-defined]
            for warning in warnings:
                app.call_from_thread(app.notify, warning)  # type: ignore[attr-defined]

        app.downloads.register_post_download(model_id, _apply_deferred_expose)  # type: ignore[attr-defined]

    def _run_apply(
        self,
        on_event: Callable[[str], None],
        on_progress: Callable[[str], None],
        register: Callable[[PendingChanges], None],
    ) -> None:
        """Construct a PendingChanges and drive apply().

        Passed as a closure to StatusScreen. Mutates self.registry and
        self.state in place (mark_ready, remove deleted models).
        The on_event callable receives lifecycle tags; on_progress
        receives per-line provider output forwarded verbatim into the
        log.
        """
        from ..providers.registry import ProviderRegistry

        providers: dict[str, object] = {}
        specs_by_id = {
            m.id: model_entry_to_variant(m)
            for m in self.registry.models
            if m.id in self.queued_ready
        }
        for spec in list(specs_by_id.values()) + list(self.queued_deletes.values()):
            try:
                entry = self.registry.provider(spec["provider"])
                providers[spec["provider"]] = ProviderRegistry.get(
                    spec["provider"], provider_config(entry)
                )
            except KeyError:
                # Provider not in registry or not mapped to a Provider
                # class: treat as flag-only (native/unmapped) and let
                # PendingChanges flip state flags without a provider call.
                continue
            # Other exceptions (bad config, import failure, etc.) are
            # real errors and must not be silently treated as flag-only.
        litellm_path = default_litellm_config_path()
        # DownloadManager status per model, snapshotted once. Uses
        # self._app_ref (captured on_mount), not self.app: this method runs
        # on StatusScreen's worker thread after ModelScreen has already
        # been popped (_push_status_screen pops before pushing
        # StatusScreen), and Screen.app raises NoActiveAppError once
        # popped and off the main thread.
        # is_downloading() alone would also race: it flips to False the
        # instant a download finishes, before this screen's on_complete
        # callback reloads self.state from disk.
        download_status = {
            s.model_id: s.status
            for s in self._app_ref.downloads.states()  # type: ignore[attr-defined]
        }
        immediate_exposes: list[tuple[str, bool]] = []
        for mid, target in self.queued_exposes.items():
            if target and download_status.get(mid) == "downloading":
                self._register_deferred_expose(mid, litellm_path)
                continue
            if target and download_status.get(mid) == "done":
                # The download that made this model ready finished: disk
                # is already authoritative (DownloadManager persists to
                # modelman.toml before flipping is_downloading() to False),
                # but self.state may not have caught up yet. Refresh just
                # this model's row rather than the whole StateStore, so an
                # in-memory-only reconcile result for an unrelated model
                # isn't discarded by reloading state wholesale.
                with contextlib.suppress(Exception):
                    self.state.set(mid, load_state(self.state_path).get(mid))
            immediate_exposes.append((mid, target))

        pending = PendingChanges(
            registry=self.registry,
            state=self.state,
            family=self.family,
            registry_path=self.registry_path,
            state_path=self.state_path,
            providers=providers,
            ready=[(mid, specs_by_id[mid], target) for mid, target in self.queued_ready.items()],
            deletes=[(mid, spec) for mid, spec in self.queued_deletes.items()],
            moves=list(self.queued_moves.items()),
            exposes=immediate_exposes,
            litellm_path=litellm_path,
        )
        register(pending)
        pending.apply(on_event=on_event, on_progress=on_progress)
        # The closure runs on the StatusScreen's worker thread; mutate
        # in-memory queue state from here too so subsequent opens of this
        # screen see an empty queue.
        self.queued_ready.clear()
        self.queued_deletes.clear()
        self.queued_moves.clear()
        self.queued_exposes.clear()
        self._ready_cascade_for_expose.clear()
        self._added_ids.clear()

    def _restore_snapshot(self) -> None:
        """Restore the in-memory registry/state to the snapshot taken on
        mount, dropping any queued mutations.

        Restore is keyed by model id, not family: models with queued
        (unapplied) moves still belong to this family in the registry
        (edits always write back with family=self.family; only
        queued_moves tracks the pending target), so every live model
        with family == self.family is already in restore_ids via
        _snapshot_models or _added_ids. _added_ids covers the remaining
        gap: a model added into a *different* family this session, which
        the id check alone would otherwise let survive discard.
        """
        restore_ids = {m.id for m in self._snapshot_models} | self._added_ids
        keep = [m for m in self.registry.models if m.id not in restore_ids]
        self.registry.models = keep + self._snapshot_models
        # Replace state entries that were in the snapshot.
        for mid in self._snapshot_state_entries:
            self.state.set(mid, self._snapshot_state_entries[mid])
        # Defensive: drop state entries that somehow leaked in during this
        # session but weren't in the snapshot, scoped to this family. (No
        # session path writes state.models outside apply(), so this is a
        # no-op under normal discard flows.)
        for mid in list(self.state.models):
            if mid not in self._snapshot_state_entries and any(
                m.id == mid and m.family == self.family for m in self.registry.models
            ):
                self.state.models.pop(mid, None)
