"""Modal forms for the TUI."""

from __future__ import annotations

import math
from typing import Literal, NamedTuple, TypeVar, cast

from textual.app import ComposeResult
from textual.binding import Binding
from textual.containers import Horizontal, Vertical
from textual.css.query import NoMatches
from textual.screen import ModalScreen
from textual.widgets import Button, Input, Label, Select

from ..ollama_caps import auto_detect_model_info
from ..providers.base import VariantSpec
from ..registry import Cost, _cost_to_dict


def parse_model(
    provider: str, model: str, *, is_native: bool = False
) -> tuple[str | None, str | None, str | None]:
    """Parse the dialog's single `model` input into provider-specific fields.

    Returns a (variant_name, repo_id, filename) tuple. Exactly one of
    (variant_name) or (repo_id) is set; filename is set only for HF
    providers when the model input includes a path within the repo.

    ollama: model is the ollama tag verbatim. repo_id and filename
    are None. Raises ValueError if the model contains '/' (ollama
    tags don't include slashes).

    llamacpp / omlx: model is parsed on '/'.
      - 1 segment: invalid; HF repos are always 'org/name'.
      - 2 segments: whole repo. repo_id = full input, filename = "".
      - 3+ segments: one specific file. repo_id = first two joined
        by '/', filename = remaining segments joined by '/'.

    Native providers (is_native=True): `model` is used verbatim as the
    model name; blank input defaults to the sentinel "native". No
    slash-splitting — `id` becomes f"{provider}/{model_name}" regardless
    of any '/' the user types.

    openrouter and any other provider that is neither ollama, an HF
    provider (llamacpp/omlx), nor native: `model` is stored whole as
    the model name (e.g. "anthropic/claude-opus") with no repo/files
    split.

    Leading/trailing whitespace on `model` is trimmed before parsing.
    Empty input raises ValueError for HF providers.
    """
    model = model.strip()
    if is_native:
        return (model or "native", None, None)
    if provider == "ollama":
        if "/" in model:
            raise ValueError("ollama tags must not contain '/'")
        if not model:
            raise ValueError("ollama tag is required")
        return (model, None, None)
    if provider not in ("llamacpp", "omlx"):
        # openrouter and any other non-HF, non-native provider: plain
        # string, no repo/files split.
        if not model:
            raise ValueError(f"{provider} model is required")
        return (model, None, None)
    # HF providers
    if not model:
        raise ValueError(f"{provider} model is required")
    parts = model.split("/")
    if len(parts) < 2:
        raise ValueError(f"{provider} model must be 'org/repo' (or 'org/repo/file')")
    if not parts[0]:
        raise ValueError("repo org must not be empty")
    repo_id = "/".join(parts[:2])
    filename = "/".join(parts[2:])  # empty string if len == 2
    return (model, repo_id, filename)


def _parse_price(value: str, field: str) -> float:
    """Parse a non-negative finite price. Raises ValueError on bad input."""
    try:
        p = float(value)
    except ValueError:
        raise ValueError(f"{field} must be a number") from None
    if not math.isfinite(p):
        raise ValueError(f"{field} must be finite") from None
    if p < 0:
        raise ValueError(f"{field} must be non-negative") from None
    return p


def parse_cost_fields(kind: str, price_mtok: str, price_period: str, period: str) -> Cost:
    """Build a Cost from the dialog fields. Raises ValueError on bad input.

    Mirrors parse_model's contract: provider-dispatched parsing that
    raises ValueError so the dialog can surface the message inline.
    """
    if kind == "free":
        return Cost(kind="free")
    if kind == "per_token":
        p = _parse_price(price_mtok, "price_per_million_tokens")
        return Cost(kind="per_token", price_per_million_tokens=p)
    if kind == "subscription":
        p = _parse_price(price_period, "price_per_period")
        return Cost(kind="subscription", price_per_period=p, period=period)
    raise ValueError(f"unknown cost kind: {kind}")


def _price_str(value: float) -> str:
    """Prefill string for a price Input: shortest round-trip, no float noise.

    Whole numbers drop the decimal (20.0 -> "20"); everything else uses
    repr, which Python guarantees round-trips losslessly (9.99 -> "9.99").
    """
    if math.isfinite(value) and value == int(value):
        return str(int(value))
    return repr(value)


class ModelFormResult(NamedTuple):
    """ModelForm's dismiss payload: the VariantSpec plus the family the
    user chose. Family is deliberately separate from VariantSpec — the
    spec dict is the provider-facing contract and has no family field;
    ModelScreen maps family onto ModelEntry.family."""

    spec: VariantSpec
    family: str


T = TypeVar("T")


class ModelmanModal(ModalScreen[T]):
    """Base class for modelman dialogs.

    Provides shared conventions:
    - Escape cancels the modal (priority binding so it fires even when
      an Input is focused — Textual's default Input.Escape would
      otherwise swallow it for cursor-positioning).
    - Buttons are composed left-to-right in the order supplied by the
      subclass; destructive dialogs focus the safe button initially so
      pressing Enter or clicking the default doesn't do damage.
    - The Horizontal button row is right-aligned and the buttons
      themselves carry a left margin so they don't jam together.
    """

    BINDINGS = [
        Binding("escape", "cancel", "Cancel", priority=True),
    ]

    DEFAULT_CSS = """
    ModelmanModal { align: center middle; }
    ModelmanModal > Vertical { width: 60; height: auto; padding: 1 2; border: round $primary; }
    ModelmanModal Label { margin-bottom: 1; }
    ModelmanModal Input { margin-bottom: 1; }
    ModelmanModal Horizontal { height: auto; align-horizontal: right; }
    ModelmanModal Button { margin-left: 1; }
    """

    # Subclasses populate this in compose(); the base mounts the buttons
    # in on_mount so the Horizontal container is attached first (Textual
    # rejects mount() calls on unattached containers).
    _pending_buttons: list[Button] = []

    def _button_row(self, buttons: list[Button]) -> Horizontal:
        """Return an empty right-aligned Horizontal; the base will mount
        the given buttons into it once it is attached."""
        self._pending_buttons = list(buttons)
        return Horizontal(id="button-row")

    def _focus_button(self, button_id: str) -> None:
        button = self.query_one(f"#{button_id}", Button)
        button.focus()

    def action_cancel(self) -> None:
        self.dismiss(None)

    def on_mount(self) -> None:
        # Mount any deferred buttons into the row, then run the
        # subclass's on_mount for focus handling.
        if self._pending_buttons:
            row = self.query_one("#button-row", Horizontal)
            for button in self._pending_buttons:
                row.mount(button)
            self._pending_buttons = []
        self._modal_on_mount()

    def _modal_on_mount(self) -> None:
        """Subclass hook for setting initial focus. The base on_mount
        calls this after mounting deferred buttons."""


class AddFamilyModal(ModelmanModal[tuple[str, str] | None]):
    """Prompt for a family name and optional display name.

    Returns `(family, display_name)` on Create — display_name falls
    back to family when left blank. FamilyScreen owns the StateStore
    mutation + save after this dismisses; the modal itself performs
    no disk I/O (mirrors ModelForm dismissing `ModelFormResult(spec,
    family)` for ModelScreen to apply to the Registry — while this
    modal itself returns its plain `(family, display_name)` tuple).
    """

    def compose(self) -> ComposeResult:
        with Vertical():
            yield Label("Family name (required):")
            yield Input(id="family-name", placeholder="e.g. ornith-1.5")
            yield Label("Display name (optional):")
            yield Input(id="display-name", placeholder="e.g. Ornith 1.5")
            yield self._button_row(
                [
                    Button("Cancel", id="cancel", variant="default"),
                    Button("Create", id="create", variant="primary"),
                ]
            )

    def _modal_on_mount(self) -> None:
        # Drop the cursor in the required field so the user can type
        # without an extra Tab press.
        self.query_one("#family-name", Input).focus()

    def on_button_pressed(self, event: Button.Pressed) -> None:
        if event.button.id == "cancel":
            self.dismiss(None)
            return
        self._submit()

    def on_input_submitted(self, event: Input.Submitted) -> None:
        self._submit()

    def _submit(self) -> None:
        name = self.query_one("#family-name", Input).value.strip()
        display = self.query_one("#display-name", Input).value.strip()
        if not name:
            return
        self.dismiss((name, display or name))


class EditFamilyModal(ModelmanModal[str | None]):
    """Edit the display_name of an existing family.

    The family slug is intentionally NOT editable here — changing it
    would orphan cross-references from models keyed by family. The
    slug is shown read-only so the user knows which family they're
    editing.

    Returns the new display_name on Save (falls back to the family
    slug if blanked, matching AddFamilyModal); None on Cancel.
    FamilyScreen owns the StateStore mutation + save.
    """

    def __init__(self, family: str, display_name: str) -> None:
        super().__init__()
        self._family = family
        self._display_name = display_name

    def compose(self) -> ComposeResult:
        with Vertical():
            yield Label("Family (cannot be changed):")
            yield Input(
                value=self._family,
                id="family-name",
                disabled=True,
                placeholder="e.g. ornith-1.5",
            )
            yield Label("Display name (optional):")
            yield Input(
                value=self._display_name,
                id="display-name",
                placeholder="e.g. Ornith 1.5",
            )
            yield self._button_row(
                [
                    Button("Cancel", id="cancel", variant="default"),
                    Button("Save", id="save", variant="primary"),
                ]
            )

    def _modal_on_mount(self) -> None:
        # Drop the cursor in the editable field so the user can edit
        # the display name without an extra Tab press.
        self.query_one("#display-name", Input).focus()

    def on_button_pressed(self, event: Button.Pressed) -> None:
        if event.button.id == "cancel":
            self.dismiss(None)
            return
        self._submit()

    def on_input_submitted(self, event: Input.Submitted) -> None:
        self._submit()

    def _submit(self) -> None:
        display = self.query_one("#display-name", Input).value.strip()
        self.dismiss(display or self._family)


class ConfirmModal(ModelmanModal[bool]):
    """Generic yes/no confirmation. Default is No."""

    BINDINGS = [
        ("y", "answer(True)"),
        ("n", "answer(False)"),
        Binding("escape", "answer(False)", show=False),
    ]

    def __init__(self, message: str) -> None:
        super().__init__()
        self._message = message

    def compose(self) -> ComposeResult:
        with Vertical():
            yield Label(self._message)
            yield self._button_row(
                [
                    Button("Yes", id="yes", variant="warning"),
                    Button("No", id="no", variant="default"),
                ]
            )

    def _modal_on_mount(self) -> None:
        # Safe default: focus No so an accidental Enter does not confirm.
        self._focus_button("no")

    def on_button_pressed(self, event: Button.Pressed) -> None:
        self.dismiss(event.button.id == "yes")

    def action_answer(self, value: bool) -> None:
        self.dismiss(value)


class ModelForm(ModelmanModal[ModelFormResult | None]):
    """Add or edit a model. `variant=None` means add; else edit.

    The dialog asks for the provider (add mode only), family, model
    name, location, cost, and (for ollama providers) the usage tier.
    The model name is parsed provider-dispatched: ollama tags go straight
    to `variant.name`; HF model names are split into `repo` and `files`
    for llamacpp / omlx; native providers keep the name verbatim;
    openrouter stores the plain string.

    The cost section is a kind Select (free / per_token / subscription)
    plus conditional price fields: per_token shows #price-per-mtok,
    subscription shows #price-per-period and #period-select. Only the
    relevant fields are visible (.display), but all widgets are always
    mounted. The usage-tier Select is visible only for ollama providers.

    On save the form dismisses `ModelFormResult(spec, family)`. The
    family Select defaults to the family the model screen is
    showing, and can target any known family (registry families
    plus explicitly created empty ones).

    See `parse_model()` and `parse_cost_fields()` for the parsing rules.
    """

    # Only the rules that differ from the ModelmanModal base are declared
    # here; the shared align/Input/Horizontal/Button rules are inherited
    # (Textual type selectors match subclasses via the MRO).
    DEFAULT_CSS = """
    ModelForm > Vertical { width: 80; }
    ModelForm Label { margin-top: 1; }
    ModelForm Select { margin-bottom: 1; }
    ModelForm #model-error { color: $error; text-style: bold; }
    ModelForm #provider-select { color: $secondary; text-style: bold; }
    """

    def __init__(
        self,
        providers: list[str],
        variant: VariantSpec | None = None,
        default_provider: str | None = None,
        families: list[str] | None = None,
        family: str | None = None,
        provider_kinds: dict[str, str] | None = None,
    ) -> None:
        super().__init__()
        self._providers = providers
        self._variant = variant  # None for add
        # Used only in add mode (variant is None): the provider to
        # pre-fill in the provider Select. Edit mode always uses the
        # variant's own provider since provider is immutable on edit.
        self._default_provider = default_provider
        self._family = family
        self._provider_kinds = provider_kinds or {}
        # Families the selector offers (sorted by the caller) and the
        # pre-selected family. When a caller passes neither, the
        # selector degrades to a single "unknown" entry — the TUI
        # always passes real values; only direct test callers hit
        # this default.
        #
        # When `family` (the pre-selected family) isn't in the list —
        # e.g. a queued move targeting a family that doesn't exist
        # yet — prepend it so the dialog opens on the target, not the
        # screen's current family. This is the only legitimate case
        # where the Select deviates from the caller's order.
        self._families: list[str] = (
            list(families) if families else ([family] if family else ["unknown"])
        )
        if self._family is not None and self._family not in self._families:
            self._families.insert(0, self._family)

    def compose(self) -> ComposeResult:
        editing = self._variant is not None
        v: VariantSpec = self._variant if self._variant is not None else cast("VariantSpec", {})
        if editing:
            initial_provider = v.get("provider") or self._providers[0]
        elif self._default_provider and self._default_provider in self._providers:
            initial_provider = self._default_provider
        else:
            initial_provider = self._providers[0]
        self._initial_provider: str = initial_provider

        model_val = self._reconstruct_model(v) if editing else ""
        kind = self._provider_kinds.get(initial_provider, self._default_kind(initial_provider))
        placeholder = (
            "e.g. ornith-1.5:35b"
            if kind == "ollama"
            else "leave blank for 'native', or a model name"
            if kind == "native"
            else "provider/model-name"
            if kind == "cloud-only"
            else "org/repo[/path/to/file]"
        )
        location_value = (
            "cloud"
            if kind in ("native", "cloud-only")
            else "local"
            if kind == "local-only"
            else v.get("location") or "local"
        )
        location_locked = kind in ("native", "cloud-only", "local-only")
        self._initial_provider_is_ollama: bool = kind == "ollama"

        # Cost & usage-tier prefill. Add mode starts free/untiered; edit
        # mode reads them from the variant. _price_str gives the shortest
        # round-trip representation ("20.0" -> "20", 9.99 -> "9.99") so
        # an untouched edit-save never changes the stored price (plain
        # `:g` truncates to 6 significant digits, so it would silently
        # round e.g. 3.14159265; repr keeps full precision without
        # binary-inexact noise).
        #
        # Edit mode with no cost pre-selects the "unset" sentinel so saving
        # without touching the cost section preserves cost=None. Custom
        # subscription periods outside month/year are added to the period
        # Select so they are not silently rewritten to the default month.
        initial_cost_kind = "free" if not editing else ""
        initial_price_mtok = ""
        initial_price_period = ""
        initial_period = "month"
        period_options = [("month", "month"), ("year", "year")]
        initial_tier = ""
        cost_raw = v.get("cost") if editing else None
        cost: Cost | None = None
        if isinstance(cost_raw, dict):
            cost = Cost(
                kind=cost_raw["kind"],
                price_per_million_tokens=cost_raw.get("price_per_million_tokens"),
                price_per_period=cost_raw.get("price_per_period"),
                period=cost_raw.get("period"),
            )
        elif isinstance(cost_raw, Cost):
            cost = cost_raw
        if cost is not None:
            initial_cost_kind = cost.kind
            if cost.kind == "per_token" and cost.price_per_million_tokens is not None:
                initial_price_mtok = _price_str(cost.price_per_million_tokens)
            elif cost.kind == "subscription":
                if cost.price_per_period is not None:
                    initial_price_period = _price_str(cost.price_per_period)
                if cost.period is not None:
                    initial_period = cost.period
                    if cost.period not in ("month", "year"):
                        period_options.append((cost.period, cost.period))
        usage_tier_value = v.get("usage_tier") if editing else None
        if usage_tier_value in ("low", "medium", "high", "extra high"):
            initial_tier = usage_tier_value

        with Vertical():
            yield Label("Provider:")
            yield Select(
                options=[(p, p) for p in self._providers],
                value=initial_provider,
                allow_blank=False,
                disabled=editing,
                id="provider-select",
            )
            yield Label("Family:")
            yield Select(
                options=[(f, f) for f in self._families],
                value=(self._family if self._family in self._families else self._families[0]),
                allow_blank=False,
                id="family-select",
            )
            yield Label("Model:")
            yield Input(
                value=model_val,
                placeholder=placeholder,
                id="model",
            )
            yield Label("", id="model-error")
            yield Label("Location:")
            yield Select(
                options=[("cloud", "cloud"), ("local", "local")],
                value=location_value,
                allow_blank=False,
                disabled=location_locked,
                id="location-select",
            )
            yield Label("Cost:")
            yield Select(
                options=[
                    ("—", ""),
                    ("free", "free"),
                    ("per_token", "per_token"),
                    ("subscription", "subscription"),
                ],
                value=initial_cost_kind,
                allow_blank=False,
                id="cost-kind-select",
            )
            yield Input(
                value=initial_price_mtok,
                placeholder="price per million tokens, e.g. 2.50",
                id="price-per-mtok",
            )
            yield Input(
                value=initial_price_period,
                placeholder="price per period, e.g. 20",
                id="price-per-period",
            )
            yield Select(
                options=period_options,
                value=initial_period,
                allow_blank=False,
                id="period-select",
            )
            yield Label("Usage tier (ollama cloud):", id="usage-tier-label")
            yield Select(
                options=[
                    ("—", ""),
                    ("low", "low"),
                    ("medium", "medium"),
                    ("high", "high"),
                    ("extra high", "extra high"),
                ],
                value=initial_tier,
                allow_blank=False,
                id="usage-tier-select",
            )
            yield self._button_row(
                [
                    Button("Cancel", id="cancel", variant="default"),
                    Button("Save", id="save", variant="primary"),
                ]
            )

    @staticmethod
    def _reconstruct_model(v: VariantSpec) -> str:
        """Build the dialog's `model` string from a stored VariantSpec.

        For HF providers the model string is `repo` plus `/file` if a
        single filename is stored. For ollama it's just the tag.
        For native providers it's the model name. For openrouter it's
        the plain model string.
        """
        provider = v.get("provider")
        if provider == "ollama" or provider not in ("llamacpp", "omlx"):
            return v.get("name") or ""
        repo = v.get("repo") or ""
        files = v.get("files") or []
        if files:
            return f"{repo}/{files[0]}"
        return repo

    def _modal_on_mount(self) -> None:
        # Focus the model input so the user can paste / type immediately.
        self.query_one("#model", Input).focus()
        # Apply initial visibility: cost fields follow the pre-selected
        # kind; the tier section only shows for ollama providers.
        self._set_cost_field_visibility(str(self.query_one("#cost-kind-select", Select).value))
        self._set_tier_visibility(self._initial_provider_is_ollama)

    def _set_cost_field_visibility(self, kind: str) -> None:
        """Show only the price fields relevant to the selected cost kind:
        free -> nothing; per_token -> #price-per-mtok; subscription ->
        #price-per-period + #period-select."""
        try:
            self.query_one("#price-per-mtok", Input).display = kind == "per_token"
            self.query_one("#price-per-period", Input).display = kind == "subscription"
            self.query_one("#period-select", Select).display = kind == "subscription"
        except NoMatches:
            # A Select.Changed can arrive before the conditional fields are
            # mounted; _modal_on_mount applies the initial state anyway.
            return

    def _set_tier_visibility(self, show: bool) -> None:
        """Show/hide the ollama usage-tier section (label + Select) as a unit."""
        try:
            self.query_one("#usage-tier-label", Label).display = show
            self.query_one("#usage-tier-select", Select).display = show
        except NoMatches:
            return

    def on_select_changed(self, event: Select.Changed) -> None:
        """Cost-kind changes show only the relevant price fields. In add
        mode, changing the provider must re-lock location, update the model
        input placeholder to match the new provider's expected format
        (native vs HF vs ollama vs cloud-only), and toggle the ollama
        usage-tier section."""
        if event.select.id == "cost-kind-select":
            self._set_cost_field_visibility(str(event.value))
            return
        if event.select.id != "provider-select":
            return
        if self._variant is not None:
            # Edit mode locks the provider; changes shouldn't happen.
            return
        provider = str(event.value)
        kind = self._provider_kinds.get(provider, self._default_kind(provider))
        new_placeholder = (
            "e.g. ornith-1.5:35b"
            if kind == "ollama"
            else "leave blank for 'native', or a model name"
            if kind == "native"
            else "provider/model-name"
            if kind == "cloud-only"
            else "org/repo[/path/to/file]"
        )
        model_input = self.query_one("#model", Input)
        model_input.placeholder = new_placeholder

        location_select = self.query_one("#location-select", Select)
        new_location = (
            "cloud"
            if kind in ("native", "cloud-only")
            else "local"
            if kind == "local-only"
            else "local"
        )
        location_locked = kind in ("native", "cloud-only", "local-only")
        location_select.value = new_location
        location_select.disabled = location_locked
        # The ollama usage-tier section follows the provider kind.
        self._set_tier_visibility(kind == "ollama")

    def on_button_pressed(self, event: Button.Pressed) -> None:
        if event.button.id == "cancel":
            self.dismiss(None)
            return
        self._submit()

    def on_input_submitted(self, event: Input.Submitted) -> None:
        self._submit()

    def _show_error(self, message: str) -> None:
        self.query_one("#model-error", Label).update(message)

    def _clear_error(self) -> None:
        self.query_one("#model-error", Label).update("")

    def _default_kind(self, provider: str) -> str:
        """Fallback kind when the caller didn't supply provider_kinds."""
        if provider in ("llamacpp", "omlx"):
            return "local-only"
        if provider == "ollama":
            return "ollama"
        return "cloud-only"

    def _submit(self) -> None:
        provider = str(self.query_one("#provider-select", Select).value)
        kind = self._provider_kinds.get(provider, self._default_kind(provider))
        raw = self.query_one("#model", Input).value
        try:
            name, repo, filename = parse_model(provider, raw, is_native=(kind == "native"))
        except ValueError as exc:
            self._show_error(str(exc))
            return
        self._clear_error()

        cost_kind = str(self.query_one("#cost-kind-select", Select).value)
        price_mtok = self.query_one("#price-per-mtok", Input).value
        price_period = self.query_one("#price-per-period", Input).value
        period = str(self.query_one("#period-select", Select).value)
        cost: Cost | None = None
        if cost_kind != "":
            try:
                cost = parse_cost_fields(cost_kind, price_mtok, price_period, period)
            except ValueError as exc:
                self._show_error(str(exc))
                return

        if self._variant is not None:
            vid = self._variant["id"]
        elif kind == "native":
            vid = f"{provider}/{name}"
        else:
            vid = f"{provider}/{name.replace('/', '--')}"  # type: ignore[union-attr]

        existing_quantizations = (
            (self._variant or {}).get("quantizations") if self._variant is not None else None
        )
        location = str(self.query_one("#location-select", Select).value)
        usage_tier: str | None = None
        if kind == "ollama":
            # The widget always exists, but read defensively anyway.
            tier_raw = str(self.query_one("#usage-tier-select", Select).value)
            if tier_raw in ("low", "medium", "high", "extra high"):
                usage_tier = tier_raw
        spec: VariantSpec = {
            "id": vid,
            "provider": provider,
            "name": name or vid,
            "repo": repo,
            "files": [filename] if filename else None,
            "quantizations": existing_quantizations,
            "location": location,
            "cost": _cost_to_dict(cost) if cost is not None else None,
            "usage_tier": usage_tier,
        }
        if provider == "ollama" and self._variant is None and name:
            spec["model_info"] = auto_detect_model_info(name)
        else:
            spec["model_info"] = (self._variant or {}).get("model_info")
        family = str(self.query_one("#family-select", Select).value)
        self.dismiss(ModelFormResult(spec=spec, family=family))


class ConfirmExitDialog(ModelmanModal[Literal["apply", "cancel", "discard"]]):
    """Show pending downloads/deletes; let the user apply, cancel, or discard.

    - apply: confirm and run PendingChanges.apply() in the caller
    - cancel: return to the model screen, queue held in memory
    - discard: drop the queue, caller should restore the in-memory manifest
    """

    # Only the width differs from the ModelmanModal base; the rest is inherited.
    DEFAULT_CSS = """
    ConfirmExitDialog > Vertical { width: 70; }
    """

    BINDINGS = [
        ("y", "answer('apply')"),
        ("n", "answer('cancel')"),
        ("d", "answer('discard')"),
        Binding("escape", "answer('cancel')", show=False),
    ]

    def __init__(
        self,
        ready: list[tuple[str, bool]],
        deletes: list,
        exposes: list[tuple[str, bool]] | None = None,
        moves: list[tuple[str, str]] | None = None,
    ) -> None:
        super().__init__()
        self._ready = ready
        self._deletes = deletes
        self._exposes = exposes or []
        self._moves = moves or []

    def compose(self) -> ComposeResult:
        ready_on = [mid for mid, target in self._ready if target]
        ready_off = [mid for mid, target in self._ready if not target]
        with Vertical():
            yield Label(
                f"Pending: ready {len(self._ready)} · delete {len(self._deletes)}"
                f" · move {len(self._moves)} · expose {len(self._exposes)}"
            )
            for mid in ready_on:
                yield Label(f"  ↓ {mid}")
            for mid in ready_off:
                yield Label(f"  ↑ {mid}")
            for v in self._deletes:
                yield Label(f"  × {v['id']} ({v['provider']})")
            for model_id, target in self._moves:
                yield Label(f"  → {model_id} → {target}")
            for model_id, exposed in self._exposes:
                mark = "Y" if exposed else "–"
                yield Label(f"  {mark} {model_id}")
            yield Label("Apply, cancel, or discard these changes?")
            yield self._button_row(
                [
                    Button("Cancel", id="cancel", variant="default"),
                    Button("Discard", id="discard", variant="warning"),
                    Button("Apply", id="apply", variant="primary"),
                ]
            )

    def _modal_on_mount(self) -> None:
        # Safe default: focus Cancel so an accidental Enter does nothing.
        self._focus_button("cancel")

    def on_button_pressed(self, event: Button.Pressed) -> None:
        bid = event.button.id
        if bid in ("apply", "cancel", "discard"):
            self.dismiss(bid)  # type: ignore[arg-type]

    def action_answer(self, value: str) -> None:
        if value in ("apply", "cancel", "discard"):
            self.dismiss(value)  # type: ignore[arg-type]


class CancelApplyDialog(ModelmanModal[Literal["cancel", "wait"]]):
    """Ask the user whether to abort the running apply or keep waiting.

    - cancel: cancel the apply (kill current subprocess if possible, skip
      remaining items, do not save the manifest). Already-completed steps
      are NOT undone.
    - wait: dismiss the dialog; the apply keeps running.
    """

    # Only the border differs from the ModelmanModal base; the rest is inherited.
    DEFAULT_CSS = """
    CancelApplyDialog > Vertical { border: round $warning; }
    """

    BINDINGS = [
        Binding("escape", "answer('wait')", show=False),
        ("w", "answer('wait')"),
        ("c", "answer('cancel')"),
    ]

    def compose(self) -> ComposeResult:
        with Vertical():
            yield Label("Actions are still running.")
            yield Label("Cancel and stop here, or wait for them to finish?")
            yield self._button_row(
                [
                    Button("Cancel", id="cancel", variant="warning"),
                    Button("Wait", id="wait", variant="primary"),
                ]
            )

    def _modal_on_mount(self) -> None:
        # Safe default: focus Wait so an accidental Enter keeps the
        # apply running.
        self._focus_button("wait")

    def on_button_pressed(self, event: Button.Pressed) -> None:
        bid = event.button.id
        if bid in ("cancel", "wait"):
            self.dismiss(bid)  # type: ignore[arg-type]

    def action_answer(self, value: str) -> None:
        if value in ("cancel", "wait"):
            self.dismiss(value)  # type: ignore[arg-type]
