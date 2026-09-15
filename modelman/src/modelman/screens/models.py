"""ModelScreen — drill into a family's models grouped by provider."""

from __future__ import annotations

from dataclasses import replace
from datetime import UTC, datetime
from pathlib import Path
from typing import TYPE_CHECKING

from rich.text import Text
from textual.app import ComposeResult
from textual.binding import Binding
from textual.css.query import NoMatches
from textual.screen import Screen
from textual.widgets import DataTable, Footer, Header, Static

from ..formatting import format_size
from ..litellm import (
    is_effectively_exposed,
    passes_ready_gate,
    provider_policy,
)
from ..local_control import (
    LocalControlError,
    discover_unregistered_models,
    running_model_ids,
    same_provider_occupant,
    start_local_model,
    stop_local_model,
)
from ..queue import QueuedOps
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
    is_local_location,
    is_native_provider,
    known_families,
    model_entry_to_variant,
    save_registry,
)
from ..state import ModelState, StateStore, load_state, locked_state
from . import reconcile_model_state, reload_preserving_cursor, row_key_at
from .forms import ConfirmModal, default_form_kind

if TYPE_CHECKING:
    from ..local_control import DiscoveredModel
    from ..providers.base import VariantSpec


def _now_iso() -> str:
    """UTC timestamp for pricing_updated_at — second precision, no
    microseconds, so it round-trips cleanly through TOML."""
    return datetime.now(UTC).replace(microsecond=0).isoformat()


def _cost_changed(old: Cost | None, new: Cost | None) -> bool:
    """True when any *per-token* price differs between two Cost values.

    Deliberately ignores subscription fields: ``pricing_updated_at`` records
    when per-token prices were last refreshed (or set), and an edit that only
    changes a subscription price/period must not relabel that timestamp —
    subscription pricing is a different dimension from the per-token prices
    the refresh measures. None-ness changes count as a change; matching None
    is not a change.
    """
    if old is None and new is None:
        return False
    if old is None or new is None:
        return True
    return (
        old.input_price_per_million != new.input_price_per_million
        or old.cache_price_per_million != new.cache_price_per_million
        or old.output_price_per_million != new.output_price_per_million
    )


def _variant_to_model_entry(
    variant: dict, *, family: str, registry: Registry, source: str = "curated"
) -> ModelEntry:
    """Convert a ModelForm VariantSpec-shaped dict to a ModelEntry.

    The dialog still emits the legacy TypedDict shape (provider, name,
    repo, files, model_info); the screen needs a ModelEntry to insert
    into registry.models. This adapter keeps the form simple and
    isolates the shape translation here.

    Edit mode preserves `variant["id"]` (the immutable key the user
    sees in the picker); add mode derives the same `provider/name`
    shape ModelForm produced. We don't need a separate "id derivation"
    step — the form already gave us one. `source` defaults to
    "curated" (every normal add/edit); the discovered-registration flow
    passes "discovered" explicitly so provenance survives into
    registry.toml, matching the CLI's own auto-register convention.
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
        source=source,
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


def _format_per_token(cost: Cost | None) -> str:
    """COST column: input/cache/output per-million-token prices as three
    space-delimited values, each a 2-digit (leading-space-padded) integer
    part and 4 decimal places (e.g. ' 2.0000', '12.5000') so prices over
    $10/million don't break column alignment. A missing individual price
    renders as a 7-dash placeholder ('-------'), matching that width. No
    cost data at all still collapses to a single '-'."""
    if cost is None:
        return "-"
    prices = (
        cost.input_price_per_million,
        cost.cache_price_per_million,
        cost.output_price_per_million,
    )
    if all(p is None for p in prices):
        return "-"
    return " ".join("-------" if p is None else f"{p:7.4f}" for p in prices)


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
    """The app's single root screen: every model, across every family,
    sorted (family, location, provider, model name). Family-level
    filtering was removed along with FamilyScreen — see
    docs/superpowers/specs (family-screen removal design)."""

    BINDINGS = [
        ("escape", "back", "Back"),
        ("a", "add_model", "Add"),
        ("d", "delete_model", "Delete"),
        ("e", "edit_model", "Edit"),
        Binding("enter", "select_row", "Edit", priority=True),
        ("l", "toggle_litellm", "LiteLLM"),
        ("r", "toggle_ready", "Toggle ready"),
        ("x", "toggle_expose", "Toggle exposed"),
        ("s", "toggle_running", "Start/stop"),
    ]

    def __init__(
        self,
        registry: Registry,
        state: StateStore,
        registry_path: Path,
        state_path: Path,
    ) -> None:
        super().__init__()
        self.registry = registry
        self.state = state
        self.registry_path = registry_path
        self.state_path = state_path
        # Provider of the last model added or edited this session; used to
        # default the Add dialog's provider dropdown.
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
        # On-disk artifacts an in-scope local provider reports with no
        # matching registry.toml entry (populated by the on-mount
        # discovery worker — see _run_reconcile). Rendered as extra
        # synthetic rows in the table; _discovered_by_key is rebuilt
        # from scratch on every _repopulate() call, keyed by a synthetic
        # row key ("discovered:<provider>:<variant>"), never a real
        # ModelEntry id.
        self.discovered: list[DiscoveredModel] = []
        self._discovered_by_key: dict[str, DiscoveredModel] = {}
        # model_id -> target family. Applied to the registry at apply()
        # time; the in-memory family is untouched until then so the row
        # stays visible in the table with a → glyph.
        self.queued_moves: dict[str, str] = {}
        # Snapshot for discard: restore if the user exits without applying.
        # Spans the whole registry now — this screen isn't scoped to one
        # family, so there's no other family's data to preserve alongside it.
        # Taken once, here, at construction. This screen is the app's
        # single long-lived root and exits (with an Apply/Discard/None
        # QueuedOps) rather than resuming — apply() itself only ever runs
        # after the TUI process has already exited (see main.py's
        # run_queued_ops) — so there is no later point in this screen's
        # life where the baseline needs retaking.
        self._snapshot_models: list[ModelEntry] = []
        self._snapshot_state_entries: dict[str, ModelState] = {}
        self._retake_snapshot()

    def compose(self) -> ComposeResult:
        yield Header()
        yield DataTable(id="model-table", cursor_type="row")
        yield Static("path: —", id="details-panel")
        yield Static("Pending: ready 0 · delete 0", id="pending-bar")
        yield Static("", id="litellm-status")
        yield Footer()

    def on_mount(self) -> None:
        mt = self.query_one("#model-table", DataTable)
        mt.add_columns(
            "FAMILY",
            "PROVIDER",
            "MODEL",
            "LOC",
            "STATUS",
            "EXPOSED",
            "RUNNING",
            "COST",
            "SIZE",
        )
        self.reload()
        self._refresh_pending_bar()
        self._update_litellm_status()
        mt.focus()
        self.run_worker(self._run_reconcile, exclusive=True, thread=True)

    def _sorted_models(self) -> list[ModelEntry]:
        """Every model, sorted (family, location, provider, model name) —
        local models before cloud within each family/provider group.
        Shared by _load_models and _scroll_cursor_to_family so the two
        can't drift apart."""
        return sorted(
            self.registry.models,
            key=lambda m: (
                m.family,
                0 if is_local_location(m.location) else 1,
                m.provider_id,
                m.model_name,
            ),
        )

    def _run_reconcile(self) -> None:
        """Ask each provider whether its models are on disk; write the
        result straight into `state` for local-artifact models. Files
        present -> ready=True + disk_path + size_bytes; absent ->
        ready=False + cleared path/size. Non-local-artifact models
        (cloud-located, or on a cloud provider) are left alone by this
        step — only disk_path/size_bytes are opportunistically updated
        when the provider reports them; their ready flag is driven by
        the ready-toggle's apply-time download/pull instead.

        Delegates to the shared reconcile_model_state (screens/__init__.py).

        Also asks each in-scope local provider what's on disk that has
        no registry.toml entry at all (discover_unregistered_models,
        local_control.py) and stores the result on self.discovered —
        the same background worker, not a second one, since both calls
        are read-only provider queries with no reason to run
        concurrently.
        """
        reconcile_model_state(self.registry.models, self.registry, self.state)
        # Self-heal the running flag the same way ready/disk_path already
        # are: a model flagged running whose process actually died (crash,
        # manual kill outside modelman) must not keep showing RUNNING=●
        # forever. running_model_ids() re-probes every flagged model and
        # clears any stale flag as a side effect; anything it doesn't
        # return is not verified running, so it gets cleared here too.
        verified = set(running_model_ids(self.registry, self.state, self.state_path))
        for model_id, model_state in list(self.state.models.items()):
            if model_state.running and model_id not in verified:
                self.state.models[model_id] = replace(model_state, running=False)
        self.discovered = discover_unregistered_models(self.registry)
        # Re-render on the main thread.
        self.app.call_from_thread(self.reload)

    def _update_litellm_status(self) -> None:
        """Render the [litellm] routing mode in the screen's status area.

        Purely display: never starts, stops, or restarts the proxy — the
        toggle only flips modelman.toml's [litellm].enabled. Moved here
        from the removed FamilyScreen, which was the app's home screen
        before this one took over that role.
        """
        mode = "on" if self.state.litellm.enabled else "off"
        url_display = (
            f" ({self.state.litellm.url})" if mode == "on" and self.state.litellm.url else ""
        )
        self.query_one("#litellm-status", Static).update(
            f"LiteLLM: {mode}{url_display}  [l] toggle"
        )

    def action_toggle_litellm(self) -> None:
        """Flip [litellm].enabled on `l`. Routing policy only — the proxy
        process is never touched. locked_state() re-reads the on-disk
        state so the flip applies on top of the latest file. Only the
        `litellm` table is resynced back onto self.state (not a full
        reload) so this doesn't clobber in-session model/queue state
        this screen has already reconciled or the user has queued."""
        with locked_state(self.state_path) as disk_state:
            disk_state.litellm.enabled = not disk_state.litellm.enabled
        self.state.litellm = load_state(self.state_path).litellm
        self._update_litellm_status()

    def reload(self) -> None:
        self._load_models()

    def _load_models(self) -> None:
        try:
            mt = self.query_one("#model-table", DataTable)
        except NoMatches:
            # _run_reconcile's background worker defers back to this via
            # self.app.call_from_thread(self.reload) — if the app has
            # already begun exiting (Apply/Discard) by the time that
            # deferred call runs, the table may be torn down already.
            return

        def _repopulate() -> None:
            mt.clear()
            for m in self._sorted_models():
                ready = self._is_ready(m.id)
                size_str = format_size(self.state.get(m.id).size_bytes) if ready else "—"
                if m.id in self.queued_deletes:
                    status = "[red]✗[/red]"
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
                is_local = is_local_location(m.location) or is_local_location(
                    self.registry.provider(m.provider_id).location
                    if any(p.id == m.provider_id for p in self.registry.providers)
                    else None
                )
                running_str = "●" if (is_local and self.state.get(m.id).running) else "-"
                mt.add_row(
                    m.family,
                    m.provider_id,
                    m.model_name,
                    _format_location(m.location),
                    status,
                    exposed_str,
                    running_str,
                    _format_per_token(m.cost),
                    size_str,
                    key=m.id,
                )

            self._discovered_by_key = {}
            for d in sorted(self.discovered, key=lambda d: (d.provider_id, d.variant_id)):
                row_key = f"discovered:{d.provider_id}:{d.variant_id}"
                self._discovered_by_key[row_key] = d
                mt.add_row(
                    "—",
                    d.provider_id,
                    d.variant_id,
                    _format_location(LOCATION_LOCAL),
                    "[cyan]+[/cyan]",
                    "–",
                    "-",
                    "-",
                    format_size(d.size_bytes) if d.size_bytes else "—",
                    key=row_key,
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
        """The ready value this model will have after apply(): the queued
        target if one exists, otherwise the persisted flag."""
        if model_id in self.queued_ready:
            return self.queued_ready[model_id]
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
        """Undo an expose-triggered ready cascade: drop the queued
        ready-on. Shared by the repeated-'x'-keypress cancel path and
        discard."""
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
        entry = self._current_entry()
        if entry is None:
            return
        mid = entry.id

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

        # Every ready-on/off is queued now — apply() (post-exit) owns the
        # consequences, including a mapped provider's real download. The
        # invariant below drops any queued expose the new ready value
        # makes impossible, so the screen queue can never strand one.
        self.queued_ready[mid] = target
        self._enforce_expose_ready_rule(mid, entry)
        self._last_provider_used = entry.provider_id
        self._refresh_pending_bar()
        self.reload()

    def action_toggle_expose(self) -> None:
        entry = self._current_entry()
        if entry is None:
            return
        mid = entry.id
        if provider_policy(entry.provider_id) is None:
            self.app.notify("Provider has no LiteLLM mapping — cannot expose")
            return
        persisted_exposed = self.state.get(mid).exposed
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
            # their request; otherwise cascade a ready=True queue in for
            # either a mapped or flag-only provider — nothing runs until
            # Apply, at which point apply() runs the ready loop before the
            # expose loop, so the order works.
            if mid in self.queued_ready:
                self.app.notify(
                    "Model is queued to be made not ready — cancel that before exposing"
                )
                return
            self.queued_ready[mid] = True
            self._ready_cascade_for_expose.add(mid)
        self.queued_exposes[mid] = target
        self._refresh_pending_bar()
        self.reload()

    def action_toggle_running(self) -> None:
        entry = self._current_entry()
        if entry is None:
            return
        provider_location = next(
            (p.location for p in self.registry.providers if p.id == entry.provider_id), None
        )
        if not (is_local_location(entry.location) or is_local_location(provider_location)):
            self.app.notify("Only local models can be started/stopped")
            return
        if not self._is_ready(entry.id):
            self.app.notify("Model is not ready — mark it ready first")
            return
        mid = entry.id
        currently_running = self.state.get(mid).running

        if currently_running:
            self.app.notify(f"Stopping {mid}…")
            self.run_worker(lambda: self._do_stop(mid), thread=True, exclusive=False)
            return

        others = [m for m in self.state.models if m != mid and self.state.models[m].running]
        if others:
            # Design §4: a same-provider replacement is never a silent
            # surprise. omlx/mtplx/mlx_lm_server can only serve one model
            # per process, so starting this one stops that provider's
            # current occupant — name it explicitly, on top of the generic
            # "N others running" advisory (which also covers unrelated
            # providers, where nothing is replaced).
            occupant = same_provider_occupant(self.registry, self.state, mid, entry.provider_id)
            replacement_note = (
                f"\nStarting this will also stop {occupant}, since {entry.provider_id} "
                "can only serve one model at a time."
                if occupant is not None
                else ""
            )
            message = (
                f"{len(others)} other local model(s) already running: {', '.join(sorted(others))}."
                f"{replacement_note}\n"
                f"Start {mid} anyway?"
            )
            self.app.push_screen(ConfirmModal(message), lambda ok: self._on_start_confirmed(mid, ok))
        else:
            self._on_start_confirmed(mid, True)

    def _on_start_confirmed(self, model_id: str, confirmed: bool | None) -> None:
        if not confirmed:
            return
        self.app.notify(f"Starting {model_id}…")
        self.run_worker(lambda: self._do_start(model_id), thread=True, exclusive=False)

    def _resync_running_flags(self) -> None:
        """Merge just the `running` flags from disk back onto self.state.

        local_control owns those flags and writes them to modelman.toml
        from these workers, so they have to be re-read — but a full
        `self.state = load_state(...)` would clobber everything the
        on-mount reconcile worker put in memory (`ready`/`disk_path`/
        `size_bytes`, none of which is persisted until the pending-changes
        queue is applied on exit), silently reverting the READY/SIZE/path
        columns after the first `s` press. Same reasoning — and same
        targeted-resync shape — as action_toggle_litellm's `[litellm]`
        merge.
        """
        fresh = load_state(self.state_path)
        for model_id, fresh_state in fresh.models.items():
            current = self.state.models.get(model_id)
            if current is None:
                self.state.models[model_id] = fresh_state
            else:
                self.state.models[model_id] = replace(current, running=fresh_state.running)

    def _do_start(self, model_id: str) -> None:
        try:
            result = start_local_model(self.registry, model_id, self.state_path)
        except LocalControlError as exc:
            self.app.call_from_thread(self.app.notify, f"Failed to start {model_id}: {exc}", severity="error")
            return
        self._resync_running_flags()
        message = f"{model_id} is already running." if result.already_running else f"Started {model_id}."
        self.app.call_from_thread(self.app.notify, message)
        self.app.call_from_thread(self.reload)

    def _do_stop(self, model_id: str) -> None:
        try:
            result = stop_local_model(model_id, self.state_path)
        except LocalControlError as exc:
            self.app.call_from_thread(self.app.notify, f"Failed to stop {model_id}: {exc}", severity="error")
            return
        self._resync_running_flags()
        message = f"Stopped {model_id}." if result.stopped_model_id else f"{model_id} was not running."
        self.app.call_from_thread(self.app.notify, message)
        self.app.call_from_thread(self.reload)

    def _provider_list(self) -> list[str]:
        # Every provider configured in registry.toml, not just the ones
        # currently backing a model, so the Add dialog can always offer a
        # fresh choice. Sorted alphabetically for a stable, predictable
        # Select order. Falls back to the reconcilable-provider defaults
        # only when the registry has no provider entries at all.
        ids = sorted(p.id for p in self.registry.providers)
        return ids or sorted(DEFAULT_PROVIDER_IDS)

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

    def _current_model_id(self) -> str | None:
        """Model id under #model-table's cursor, or None (empty table)."""
        mt = self.query_one("#model-table", DataTable)
        return row_key_at(mt, mt.cursor_row)

    def _current_entry(self) -> ModelEntry | None:
        """ModelEntry under the cursor, or None (empty table, or the id
        under the cursor no longer matches a registry entry)."""
        mid = self._current_model_id()
        if mid is None:
            return None
        return next((m for m in self.registry.models if m.id == mid), None)

    def _current_discovered(self) -> DiscoveredModel | None:
        """DiscoveredModel under the cursor, or None (cursor is on a real
        model row, or the table is empty)."""
        mid = self._current_model_id()
        if mid is None:
            return None
        return self._discovered_by_key.get(mid)

    def _current_family(self) -> str | None:
        """The family of the model under the cursor, or None (empty table,
        or nothing to key off of). Used to default the Add dialog's family
        Select to "whatever I'm looking at" now that there's no
        family-scoped screen to inherit it from. Prefers a queued-but-
        unapplied move over the registry's family, matching the edit
        dialog's default (action_edit_model)."""
        entry = self._current_entry()
        if entry is None:
            return None
        return self.queued_moves.get(entry.id, entry.family)

    def _append_new_model_entry(
        self, variant: VariantSpec, family: str, source: str
    ) -> ModelEntry | None:
        """Build and persist a ModelEntry for a freshly submitted add or
        discovered-registration dialog result. Returns None (after
        notifying) on an id collision. Shared by _on_add_model and
        _on_register_discovered so the collision guard and the
        immediate save_registry() can't drift between the two — every
        add path persists the registry entry right away (the post-exit
        runner looks up models by id against a freshly-loaded-from-disk
        registry).
        """
        if any(m.id == variant["id"] for m in self.registry.models):
            self.app.notify("Model ID already exists")
            return None
        entry = _variant_to_model_entry(
            dict(variant), family=family, registry=self.registry, source=source
        )
        if entry.cost is not None:
            entry.pricing_updated_at = _now_iso()
        self.registry.models.append(entry)
        save_registry(self.registry, self.registry_path)
        self._last_provider_used = variant["provider"]
        return entry

    def action_add_model(self) -> None:
        from .forms import ModelForm

        providers = self._provider_list()
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
                family=self._current_family(),
                provider_kinds=self._provider_kinds(),
            ),
            self._on_add_model,
        )

    def _on_add_model(self, result) -> None:
        if result is None:
            return
        entry = self._append_new_model_entry(result.spec, result.family, result.source)
        if entry is None:
            return
        self.queued_ready[entry.id] = True
        self.reload()
        self._refresh_pending_bar()

    def _open_register_dialog(self, discovered: DiscoveredModel) -> None:
        from .forms import ModelForm

        self.app.push_screen(
            ModelForm(
                providers=self._provider_list(),
                discovered=discovered,
                families=self._families_list(),
                family=None,
                provider_kinds=self._provider_kinds(),
            ),
            lambda result: self._on_register_discovered(result, discovered),
        )

    def _on_register_discovered(self, result, discovered: DiscoveredModel) -> None:
        if result is None:
            return
        entry = self._append_new_model_entry(result.spec, result.family, result.source)
        if entry is None:
            return
        # Already on disk — the artifact came from the provider's own
        # filesystem scan, so record it as ready directly (mirroring
        # what the reconcile worker already does for known models)
        # instead of queuing a ready-on: a queued ready-on would call
        # provider.download() at apply time, and for omlx that would
        # try to fetch discovered.variant_id (a bare directory
        # basename) as an HF repo id, which HF rejects outright.
        self.state.set(
            entry.id,
            ModelState(ready=True, disk_path=discovered.path, size_bytes=discovered.size_bytes),
        )
        self.discovered = [d for d in self.discovered if d is not discovered]
        self.reload()
        self._refresh_pending_bar()

    def action_delete_model(self) -> None:
        entry = self._current_entry()
        if entry is None:
            return
        mid = entry.id
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
        discovered = self._current_discovered()
        if discovered is not None:
            self._open_register_dialog(discovered)
            return
        entry = self._current_entry()
        if entry is None:
            return
        mid = entry.id
        from .forms import ModelForm

        spec = model_entry_to_variant(entry)
        self.app.push_screen(
            ModelForm(
                providers=self._provider_list(),
                variant=spec,
                families=self._families_list(),
                family=self.queued_moves.get(mid, entry.family),
                provider_kinds=self._provider_kinds(),
                pricing_updated_at=entry.pricing_updated_at,
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
        or its path is unknown. A discovered (unregistered) row shows
        its provider-reported path directly, tagged "(unregistered)" —
        it has no state.disk_path yet since it isn't in the registry.
        """
        try:
            details = self.query_one("#details-panel", Static)
            mt = self.query_one("#model-table", DataTable)
        except NoMatches:
            return  # not mounted (e.g. screen teardown race)
        mid = row_key_at(mt, cursor_row)
        if mid is None:
            details.update("path: —")
            return
        discovered = self._discovered_by_key.get(mid)
        if discovered is not None:
            details.update(Text(f"path: {discovered.path} (unregistered)"))
            return
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
        old_entry = next((m for m in self.registry.models if m.id == updated["id"]), None)
        if old_entry is None:
            return
        new_entry = _variant_to_model_entry(
            updated, family=old_entry.family, registry=self.registry
        )
        if _cost_changed(old_entry.cost, new_entry.cost):
            new_entry.pricing_updated_at = _now_iso()
        else:
            new_entry.pricing_updated_at = old_entry.pricing_updated_at
        for i, m in enumerate(self.registry.models):
            if m.id == updated["id"]:
                self.registry.models[i] = new_entry
                break
        # Persist the edit's registry metadata (location, model name,
        # fetch split, …) immediately: unlike every other mutating
        # action here, an edit that doesn't move families queues nothing,
        # so the escape-apply path is never reached and PendingChanges'
        # final save_registry() would never run. Family changes from the
        # dialog stay queued as moves and are applied (and saved) at
        # apply time, as before.
        save_registry(self.registry, self.registry_path)
        if result.family != old_entry.family:
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
            # This screen is the app's root now — nothing to pop back to,
            # so Escape with no pending changes exits immediately.
            self.app.exit()
            return
        from .forms import ConfirmExitDialog

        show_reminder = getattr(self.app, "_price_refresh_skipped_or_failed", False)
        self.app.push_screen(
            ConfirmExitDialog(
                ready=list(self.queued_ready.items()),
                deletes=list(self.queued_deletes.values()),
                exposes=list(self.queued_exposes.items()),
                moves=list(self.queued_moves.items()),
                show_price_reminder=show_reminder,
            ),
            self._on_exit_confirm,
        )

    def _on_exit_confirm(self, choice: str | None) -> None:
        if choice == "apply":
            self.app.exit(
                QueuedOps(
                    ready=dict(self.queued_ready),
                    deletes=dict(self.queued_deletes),
                    moves=dict(self.queued_moves),
                    exposes=dict(self.queued_exposes),
                )
            )
            return
        if choice == "discard":
            self._restore_snapshot()
            # In-place edits save the registry to disk immediately
            # (_on_edit_model) — restoring the in-memory snapshot alone
            # would leave a discarded edit persisted on disk, so write the
            # restored registry back out too.
            save_registry(self.registry, self.registry_path)
            self.queued_ready.clear()
            self.queued_deletes.clear()
            self.queued_moves.clear()
            self.queued_exposes.clear()
            self._ready_cascade_for_expose.clear()
            self.app.exit(None)
            return
        # "cancel" or None: stay on the model screen, queue preserved.
        return

    def _retake_snapshot(self) -> None:
        """Capture the discard baseline from the current registry/state.

        Called once, from __init__. There is no later retake: this
        screen exits (via Apply/Discard) rather than resuming, and
        apply() itself only ever runs after the TUI process has exited
        (see main.py's run_queued_ops).
        """
        self._snapshot_models = [ModelEntry(**_entry_kwargs(m)) for m in self.registry.models]
        self._snapshot_state_entries = {
            mid: ModelState(**_state_kwargs(s)) for mid, s in self.state.models.items()
        }

    def _restore_snapshot(self) -> None:
        """Restore the in-memory registry/state to the last-taken snapshot
        (see _retake_snapshot), dropping any queued mutations.

        The snapshot spans the whole registry (this screen isn't scoped to
        one family), so restore is a straight replace rather than a
        per-family merge.
        """
        self.registry.models = list(self._snapshot_models)
        # Replace state entries that were in the snapshot.
        for mid in self._snapshot_state_entries:
            self.state.set(mid, self._snapshot_state_entries[mid])
        # Defensive: drop state entries that somehow leaked in during this
        # session but weren't in the snapshot. (No session path writes
        # state.models outside apply(), so this is a no-op under normal
        # discard flows.)
        for mid in list(self.state.models):
            if mid not in self._snapshot_state_entries:
                self.state.models.pop(mid, None)
