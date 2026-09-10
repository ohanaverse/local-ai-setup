"""Modal forms for the TUI."""

from __future__ import annotations

import math
from typing import Literal, NamedTuple, TypeVar, cast

from textual.app import ComposeResult
from textual.binding import Binding
from textual.containers import Horizontal, Vertical
from textual.css.query import NoMatches
from textual.screen import ModalScreen
from textual.widgets import Button, Checkbox, Input, Label, Select

from ..ollama_caps import auto_detect_model_info
from ..providers.base import VariantSpec
from ..registry import Cost, _cost_from_dict, _cost_to_dict

# Providers whose model input is an HF-style 'org/repo[/file]' id, parsed
# the same way for both the default form kind ("local-only") and
# parse_model()'s repo/filename splitting. Hoisted to a single constant
# because this tuple used to be hardcoded independently in both places —
# a change to one without the other would make parse_model take the wrong
# branch for whatever default_form_kind now calls "local-only".
# mlx_lm_server is included because its target side uses the same repo
# format, even though its form kind is "dual-model" rather than
# "local-only".
HF_REPO_PROVIDERS: tuple[str, ...] = ("llamacpp", "omlx", "mlx_lm_server")


def default_form_kind(provider: str) -> str:
    """Default ModelForm 'kind' for a provider id when the caller's
    provider_kinds map doesn't cover it. ModelScreen._provider_kinds
    uses the same rule for its non-native providers, keeping the kind
    policy in one place."""
    if provider == "mlx_lm_server":
        return "dual-model"
    if provider in HF_REPO_PROVIDERS:
        return "local-only"
    if provider == "ollama":
        return "ollama"
    return "cloud-only"


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

    mlx_lm_server: the target side is parsed as an HF repo, mirroring
    llamacpp/omlx. The draft side is handled separately by parse_dual_model
    and _submit_dual_model; this function only deals with the single
    target-repo input.

    openrouter and any other provider that is neither ollama, an HF
    provider (llamacpp/omlx/mlx_lm_server), nor native: `model` is stored
    whole as the model name (e.g. "anthropic/claude-opus") with no repo/files
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
    if provider not in HF_REPO_PROVIDERS:
        # openrouter and any other non-HF, non-native provider: plain
        # string, no repo/files split.
        if not model:
            raise ValueError(f"{provider} model is required")
        return (model, None, None)
    # HF providers (llamacpp, omlx, and mlx_lm_server target side)
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


def _resolve_repo_or_local_path(
    repo: str, local_path: str, side: str
) -> tuple[str | None, str | None]:
    """Shared repo-vs-local-path discriminator for one side of a
    mutually-exclusive Input pair (a repo Input and a local-path Input,
    exactly one of which may be filled). `side` names the field group
    in error messages (e.g. "target", "draft", "model").

    Returns (repo_or_None, local_path_or_None). Raises ValueError when
    both or neither are set — the dialog never guesses which one the
    user meant.
    """
    repo = repo.strip()
    local_path = local_path.strip()
    if repo and local_path:
        raise ValueError(f"{side}: set either an HF repo or a local path, not both")
    if not repo and not local_path:
        raise ValueError(f"{side}: an HF repo or a local path is required")
    return (repo or None, local_path or None)


def parse_dual_model(
    target_repo: str,
    target_local_path: str,
    draft_repo: str,
    draft_local_path: str,
) -> tuple[str | None, str | None, str | None, str | None]:
    """Parse the "dual-model" kind's four Inputs (mlx_lm_server target+draft
    pairing) into (target_repo, target_local_path, draft_repo,
    draft_local_path).

    Each side (target, draft) is its own mutually-exclusive repo/local-path
    pair, resolved the same way the "local-only" kind resolves its single
    pair (see `_resolve_repo_or_local_path`) — reused here for consistency
    rather than reinventing a second discriminator. This is a standalone
    function rather than a branch of `parse_model()` because a pairing
    needs four output values, not parse_model's 3-tuple.

    Raises ValueError (naming the offending side) when a side has neither
    or both of its two inputs set.
    """
    t_repo, t_local_path = _resolve_repo_or_local_path(target_repo, target_local_path, "target")
    d_repo, d_local_path = _resolve_repo_or_local_path(draft_repo, draft_local_path, "draft")
    return (t_repo, t_local_path, d_repo, d_local_path)


def _basename_of(repo_or_path: str) -> str:
    """Last '/'-separated segment of an HF repo id or filesystem path —
    used to build a readable name/id for local-path and dual-model
    entries (e.g. 'org/repo' -> 'repo', '/data/models/foo/' -> 'foo')."""
    trimmed = repo_or_path.rstrip("/")
    return trimmed.split("/")[-1] or repo_or_path


def _parse_price(value: str, field: str) -> float | None:
    """Parse a non-negative finite price. Empty string becomes None.
    Raises ValueError on bad input."""
    value = value.strip()
    if value == "":
        return None
    try:
        p = float(value)
    except ValueError:
        raise ValueError(f"{field} must be a number") from None
    if not math.isfinite(p):
        raise ValueError(f"{field} must be finite") from None
    if p < 0:
        raise ValueError(f"{field} must be non-negative") from None
    return p


def parse_cost_fields(input_price: str, cache_price: str, output_price: str) -> Cost:
    """Build a per-token Cost from the dialog fields.

    Empty fields become None. At least one price is required.
    Raises ValueError on bad input.
    """
    cost = Cost(
        input_price_per_million=_parse_price(input_price, "input_price"),
        cache_price_per_million=_parse_price(cache_price, "cache_price"),
        output_price_per_million=_parse_price(output_price, "output_price"),
    )
    if (
        cost.input_price_per_million is None
        and cost.cache_price_per_million is None
        and cost.output_price_per_million is None
    ):
        raise ValueError("at least one per-token price is required")
    return cost


def parse_subscription_fields(price: str, period: str) -> Cost:
    """Build a subscription Cost from the dialog fields.

    Both price and period are required; period must be month or year.
    Raises ValueError on bad input.
    """
    sub_price = _parse_price(price, "subscription_price")
    if sub_price is None:
        raise ValueError("subscription price is required")
    period = period.strip()
    if period == "":
        raise ValueError("subscription period is required")
    if period not in ("month", "year"):
        raise ValueError("subscription period must be month or year")
    return Cost(subscription_price=sub_price, subscription_period=period)


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
    ModelScreen maps family onto ModelEntry.family.
    """

    spec: VariantSpec
    family: str
    pricing_updated_at: str | None = None


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
    ModelmanModal Label.pricing-label { margin-bottom: 0; }
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

    The dialog shows the family (display-only — family membership is
    owned by the screen and never edited here), then asks for the
    provider, model name, and location (all three only settable in add
    mode), and optional pricing sections. Per-token pricing is
    controlled by `#per-token-checkbox` and reveals `#input-price`,
    `#cache-price`, and `#output-price`. Subscription pricing is
    controlled by `#subscription-checkbox` and reveals
    `#subscription-price` and `#subscription-period-select`. Both
    sections can be enabled at the same time.

    The model name is parsed provider-dispatched: ollama tags go straight
    to `variant.name`; HF model names are split into `repo` and `files`
    for llamacpp / omlx; native providers keep the name verbatim;
    openrouter stores the plain string.

    On save the form dismisses `ModelFormResult(spec, family)`. The
    family Select defaults to the family the model screen is
    showing, and can target any known family (registry families
    plus explicitly created empty ones).

    See `parse_model()`, `parse_cost_fields()`, and
    `parse_subscription_fields()` for the parsing rules.
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
        pricing_updated_at: str | None = None,
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
        self._pricing_updated_at = pricing_updated_at

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
        local_path_val = self._reconstruct_local_path(v) if editing else ""
        target_repo_val, target_local_path_val, draft_repo_val, draft_local_path_val = (
            self._reconstruct_dual_model(v) if editing else ("", "", "", "")
        )
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
            if kind in ("local-only", "dual-model")
            else v.get("location") or "local"
        )
        location_locked = kind in ("native", "cloud-only", "local-only", "dual-model")

        # Pricing prefill. Add mode starts with both sections disabled;
        # edit mode reads the flat cost fields from the variant.
        # _price_str gives the shortest round-trip representation
        # ("20.0" -> "20", 9.99 -> "9.99") so an untouched edit-save
        # never changes the stored price.
        initial_per_token = False
        initial_input_price = ""
        initial_cache_price = ""
        initial_output_price = ""
        initial_subscription = False
        initial_subscription_price = ""
        initial_subscription_period = "month"

        cost_raw = v.get("cost") if editing else None
        cost: Cost | None = None
        if isinstance(cost_raw, dict):
            cost = _cost_from_dict(cost_raw)
        elif isinstance(cost_raw, Cost):
            cost = cost_raw
        if cost is not None:
            if cost.input_price_per_million is not None:
                initial_per_token = True
                initial_input_price = _price_str(cost.input_price_per_million)
            if cost.cache_price_per_million is not None:
                initial_per_token = True
                initial_cache_price = _price_str(cost.cache_price_per_million)
            if cost.output_price_per_million is not None:
                initial_per_token = True
                initial_output_price = _price_str(cost.output_price_per_million)
            if cost.subscription_price is not None:
                initial_subscription = True
                initial_subscription_price = _price_str(cost.subscription_price)
                if cost.subscription_period in ("month", "year"):
                    initial_subscription_period = cost.subscription_period

        with Vertical():
            yield Label("Family:")
            # Display-only: the family is owned by the screen the dialog
            # was opened from; changing it here would silently re-home
            # the model across cross-referenced keys.
            yield Select(
                options=[(f, f) for f in self._families],
                value=(self._family if self._family in self._families else self._families[0]),
                allow_blank=False,
                disabled=True,
                id="family-select",
            )
            yield Label("Provider:")
            yield Select(
                options=[(p, p) for p in self._providers],
                value=initial_provider,
                allow_blank=False,
                disabled=editing,
                id="provider-select",
            )
            yield Label("Model:", id="model-label")
            yield Input(
                value=model_val,
                placeholder=placeholder,
                disabled=editing,
                id="model",
            )
            # "local-only" kind (llamacpp/omlx): a second, optional field
            # for a locally-produced mlx-lm directory (mlx_lm.convert/dwq
            # output), mutually exclusive with the repo Input above.
            # Visibility is toggled by kind, not removed from the DOM, so
            # _modal_on_mount/on_select_changed can query it unconditionally.
            yield Label(
                "Local path (instead of HF repo):",
                classes="pricing-label",
                id="local-path-label",
            )
            yield Input(
                value=local_path_val,
                placeholder="/absolute/path/to/local/model/dir",
                disabled=editing,
                id="local-path",
            )
            # "dual-model" kind (mlx_lm_server): a target+draft pairing.
            # Each side reuses the same repo-vs-local-path mutual
            # exclusion as the local-only field above (see
            # parse_dual_model / _resolve_repo_or_local_path).
            yield Label("Target model:", id="target-label")
            yield Label("HF repo (org/repo):", classes="pricing-label", id="target-repo-label")
            yield Input(
                value=target_repo_val,
                placeholder="org/repo",
                disabled=editing,
                id="target-repo",
            )
            yield Label(
                "Local path (instead of repo):",
                classes="pricing-label",
                id="target-local-path-label",
            )
            yield Input(
                value=target_local_path_val,
                placeholder="/absolute/path/to/local/model/dir",
                disabled=editing,
                id="target-local-path",
            )
            yield Label("Draft model:", id="draft-label")
            yield Label("HF repo (org/repo):", classes="pricing-label", id="draft-repo-label")
            yield Input(
                value=draft_repo_val,
                placeholder="org/repo",
                disabled=editing,
                id="draft-repo",
            )
            yield Label(
                "Local path (instead of repo):",
                classes="pricing-label",
                id="draft-local-path-label",
            )
            yield Input(
                value=draft_local_path_val,
                placeholder="/absolute/path/to/local/model/dir",
                disabled=editing,
                id="draft-local-path",
            )
            yield Label("", id="model-error")
            yield Label("Location:")
            yield Select(
                options=[("cloud", "cloud"), ("local", "local")],
                value=location_value,
                allow_blank=False,
                disabled=editing or location_locked,
                id="location-select",
            )
            yield Label("Quantization:")
            quantization_prefill = (v.get("quantization") or "") if editing else ""
            yield Input(
                value=quantization_prefill,
                placeholder="e.g. Q4_K_M",
                disabled=editing,
                id="quantization",
            )
            yield Label("Per-token pricing:")
            yield Checkbox(
                "Enable per-token pricing",
                value=initial_per_token,
                id="per-token-checkbox",
            )
            yield Label("Input ($/M tokens):", classes="pricing-label", id="input-price-label")
            yield Input(
                value=initial_input_price,
                id="input-price",
            )
            yield Label("Cache ($/M tokens):", classes="pricing-label", id="cache-price-label")
            yield Input(
                value=initial_cache_price,
                id="cache-price",
            )
            yield Label("Output ($/M tokens):", classes="pricing-label", id="output-price-label")
            yield Input(
                value=initial_output_price,
                id="output-price",
            )
            yield Label("Subscription pricing:")
            yield Checkbox(
                "Enable subscription pricing",
                value=initial_subscription,
                id="subscription-checkbox",
            )
            yield Label("Amount:", classes="pricing-label", id="subscription-price-label")
            yield Input(
                value=initial_subscription_price,
                id="subscription-price",
            )
            yield Label("Period:", classes="pricing-label", id="subscription-period-label")
            yield Select(
                options=[("month", "month"), ("year", "year")],
                value=initial_subscription_period,
                allow_blank=False,
                id="subscription-period-select",
            )
            timestamp_text = (
                f"Token pricing updated: {self._pricing_updated_at}"
                if self._pricing_updated_at
                else "Token pricing updated: never"
            )
            yield Label(timestamp_text, id="pricing-timestamp-label")
            yield self._button_row(
                [
                    Button("Cancel", id="cancel", variant="default"),
                    Button("Save", id="save", variant="primary"),
                ]
            )

    @staticmethod
    def _reconstruct_model(v: VariantSpec) -> str:
        """Build the dialog's `model` string from a stored VariantSpec.

        For HF providers (llamacpp, omlx, and mlx_lm_server's target side)
        the model string is `repo` plus `/file` if a single filename is
        stored. For ollama it's just the tag. For native providers it's
        the model name. For openrouter it's the plain model string.
        """
        provider = v.get("provider")
        if provider == "ollama" or provider not in HF_REPO_PROVIDERS:
            return v.get("name") or ""
        repo = v.get("repo") or ""
        files = v.get("files") or []
        if files:
            return f"{repo}/{files[0]}"
        return repo

    @staticmethod
    def _reconstruct_local_path(v: VariantSpec) -> str:
        """Prefill the "local-only" kind's local-path Input on edit.

        Empty whenever the stored entry is repo-sourced (or has no
        local_path at all) — `_reconstruct_model` already covers that case
        via the repo field.
        """
        return v.get("local_path") or ""

    @staticmethod
    def _reconstruct_dual_model(v: VariantSpec) -> tuple[str, str, str, str]:
        """Prefill the "dual-model" kind's four Inputs on edit:
        (target_repo, target_local_path, draft_repo, draft_local_path).
        Target reuses the existing `repo`/`local_path` VariantSpec keys;
        draft uses the new `draft_repo`/`draft_local_path` keys.
        """
        return (
            v.get("repo") or "",
            v.get("local_path") or "",
            v.get("draft_repo") or "",
            v.get("draft_local_path") or "",
        )

    def _modal_on_mount(self) -> None:
        # Focus the first editable field: the provider Select in add
        # mode (so the user can pick a provider immediately); in edit
        # mode provider/model/location are all disabled, so drop the
        # cursor on the first editable pricing control instead.
        if self._variant is None:
            self.query_one("#provider-select", Select).focus()
        else:
            self.query_one("#per-token-checkbox", Checkbox).focus()
        # Apply initial visibility for the conditional pricing sections.
        per_token_cb = self.query_one("#per-token-checkbox", Checkbox)
        sub_cb = self.query_one("#subscription-checkbox", Checkbox)
        self._set_per_token_visibility(bool(per_token_cb.value))
        self._set_subscription_visibility(bool(sub_cb.value))
        # Apply initial visibility for the kind-specific field groups
        # (single model Input vs. local-path vs. target+draft pairing).
        kind = self._provider_kinds.get(
            self._initial_provider, self._default_kind(self._initial_provider)
        )
        self._set_single_model_visibility(kind != "dual-model")
        self._set_local_path_visibility(kind == "local-only")
        self._set_dual_model_visibility(kind == "dual-model")

    def _set_per_token_visibility(self, show: bool) -> None:
        """Show/hide the per-token labels and price Inputs as a unit."""
        try:
            self.query_one("#input-price-label", Label).display = show
            self.query_one("#input-price", Input).display = show
            self.query_one("#cache-price-label", Label).display = show
            self.query_one("#cache-price", Input).display = show
            self.query_one("#output-price-label", Label).display = show
            self.query_one("#output-price", Input).display = show
        except NoMatches:
            # A Checkbox.Changed can arrive before the conditional fields
            # are mounted; _modal_on_mount applies the initial state anyway.
            return

    def _set_subscription_visibility(self, show: bool) -> None:
        """Show/hide the subscription labels, price Input and period Select."""
        try:
            self.query_one("#subscription-price-label", Label).display = show
            self.query_one("#subscription-price", Input).display = show
            self.query_one("#subscription-period-label", Label).display = show
            self.query_one("#subscription-period-select", Select).display = show
        except NoMatches:
            return

    def _set_single_model_visibility(self, show: bool) -> None:
        """Show/hide the single-Input "Model" field (label + Input).

        Hidden for the "dual-model" kind, which uses the target/draft
        field group instead.
        """
        try:
            self.query_one("#model-label", Label).display = show
            self.query_one("#model", Input).display = show
        except NoMatches:
            return

    def _set_local_path_visibility(self, show: bool) -> None:
        """Show/hide the "local-only" kind's local-path label + Input."""
        try:
            self.query_one("#local-path-label", Label).display = show
            self.query_one("#local-path", Input).display = show
        except NoMatches:
            return

    def _set_dual_model_visibility(self, show: bool) -> None:
        """Show/hide the "dual-model" kind's target+draft field group."""
        try:
            for widget_id in (
                "#target-label",
                "#target-repo-label",
                "#target-repo",
                "#target-local-path-label",
                "#target-local-path",
                "#draft-label",
                "#draft-repo-label",
                "#draft-repo",
                "#draft-local-path-label",
                "#draft-local-path",
            ):
                self.query_one(widget_id).display = show
        except NoMatches:
            return

    def on_checkbox_changed(self, event: Checkbox.Changed) -> None:
        """Toggle visibility of the conditional pricing sections."""
        if event.checkbox.id == "per-token-checkbox":
            self._set_per_token_visibility(event.value)
        elif event.checkbox.id == "subscription-checkbox":
            self._set_subscription_visibility(event.value)

    def on_select_changed(self, event: Select.Changed) -> None:
        """In add mode, changing the provider must re-lock location and
        update the model input placeholder to match the new provider's
        expected format (native vs HF vs ollama vs cloud-only vs dual-model)."""
        if event.select.id != "provider-select":
            return
        # Edit mode: provider is locked (disabled Select), but Textual may
        # still fire Changed on mount. Skip location updates in edit mode
        # to preserve the variant's location value.
        if self._variant is not None:
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
        location_locked = kind in ("native", "cloud-only", "local-only", "dual-model")
        location_select.value = new_location
        location_select.disabled = location_locked

        self._set_single_model_visibility(kind != "dual-model")
        self._set_local_path_visibility(kind == "local-only")
        self._set_dual_model_visibility(kind == "dual-model")

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
        return default_form_kind(provider)

    def _parse_cost_from_fields(self) -> Cost | None:
        """Build the combined per-token/subscription Cost from the pricing
        checkboxes+Inputs. Shared by both the single-Input and dual-model
        submit paths so a pricing bug can't diverge between them. Raises
        ValueError (from parse_cost_fields/parse_subscription_fields) on
        bad input; returns None when neither section is enabled.
        """
        per_token_enabled = bool(self.query_one("#per-token-checkbox", Checkbox).value)
        subscription_enabled = bool(self.query_one("#subscription-checkbox", Checkbox).value)

        per_token_cost: Cost | None = None
        subscription_cost: Cost | None = None
        if per_token_enabled:
            per_token_cost = parse_cost_fields(
                self.query_one("#input-price", Input).value,
                self.query_one("#cache-price", Input).value,
                self.query_one("#output-price", Input).value,
            )
        if subscription_enabled:
            subscription_cost = parse_subscription_fields(
                self.query_one("#subscription-price", Input).value,
                str(self.query_one("#subscription-period-select", Select).value),
            )

        if per_token_cost is None and subscription_cost is None:
            return None
        return Cost(
            input_price_per_million=per_token_cost.input_price_per_million
            if per_token_cost
            else None,
            cache_price_per_million=per_token_cost.cache_price_per_million
            if per_token_cost
            else None,
            output_price_per_million=per_token_cost.output_price_per_million
            if per_token_cost
            else None,
            subscription_price=subscription_cost.subscription_price if subscription_cost else None,
            subscription_period=subscription_cost.subscription_period
            if subscription_cost
            else None,
        )

    def _submit(self) -> None:
        provider = str(self.query_one("#provider-select", Select).value)
        kind = self._provider_kinds.get(provider, self._default_kind(provider))

        if kind == "dual-model":
            self._submit_dual_model(provider)
            return

        raw = self.query_one("#model", Input).value
        local_path_raw = (
            self.query_one("#local-path", Input).value.strip() if kind == "local-only" else ""
        )

        local_path: str | None = None
        if kind == "local-only" and local_path_raw:
            if raw.strip():
                self._show_error("set either an HF repo or a local path, not both")
                return
            name: str | None = _basename_of(local_path_raw)
            repo: str | None = None
            filename: str | None = None
            local_path = local_path_raw
        else:
            try:
                name, repo, filename = parse_model(provider, raw, is_native=(kind == "native"))
            except ValueError as exc:
                self._show_error(str(exc))
                return
        self._clear_error()

        try:
            cost = self._parse_cost_from_fields()
        except ValueError as exc:
            self._show_error(str(exc))
            return

        quantization = self.query_one("#quantization", Input).value.strip() or None

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
        spec: VariantSpec = {
            "id": vid,
            "provider": provider,
            "name": name or vid,
            "repo": repo,
            "local_path": local_path,
            "files": [filename] if filename else None,
            "quantizations": existing_quantizations,
            "location": location,
            "cost": _cost_to_dict(cost) if cost is not None else None,
            "quantization": quantization,
        }
        if provider == "ollama" and self._variant is None and name:
            spec["model_info"] = auto_detect_model_info(name)
        else:
            spec["model_info"] = (self._variant or {}).get("model_info")
        family = str(self.query_one("#family-select", Select).value)
        self.dismiss(ModelFormResult(spec=spec, family=family, pricing_updated_at=self._pricing_updated_at))

    def _submit_dual_model(self, provider: str) -> None:
        """Handle Save for the "dual-model" kind (mlx_lm_server target+draft
        pairing). Builds a VariantSpec with the target's `repo`/`local_path`
        keys plus the new `draft_repo`/`draft_local_path` keys, instead of
        going through parse_model's single-Input 3-tuple (see
        parse_dual_model).

        Id convention: `<provider>/<target-basename>+draft-<draft-basename>`
        — self-describing so the pairing reads clearly in the TUI list and
        in wt's model picker (e.g. "mlx_lm_server/Qwen3.8-27B+draft-Qwen3.8-4B").
        """
        try:
            target_repo, target_local_path, draft_repo, draft_local_path = parse_dual_model(
                self.query_one("#target-repo", Input).value,
                self.query_one("#target-local-path", Input).value,
                self.query_one("#draft-repo", Input).value,
                self.query_one("#draft-local-path", Input).value,
            )
        except ValueError as exc:
            self._show_error(str(exc))
            return
        self._clear_error()

        try:
            cost = self._parse_cost_from_fields()
        except ValueError as exc:
            self._show_error(str(exc))
            return

        quantization = self.query_one("#quantization", Input).value.strip() or None

        # No target/draft-source guard needed here: parse_dual_model already
        # raised ValueError for any side with neither (or both) of its two
        # inputs, so target_repo|target_local_path and draft_repo|
        # draft_local_path each have exactly one source by this point.
        target_name = _basename_of(target_repo or target_local_path or "")
        draft_name = _basename_of(draft_repo or draft_local_path or "")
        combined_name = f"{target_name}+draft-{draft_name}"

        vid = self._variant["id"] if self._variant is not None else f"{provider}/{combined_name}"

        existing_quantizations = (
            (self._variant or {}).get("quantizations") if self._variant is not None else None
        )
        location = str(self.query_one("#location-select", Select).value)
        spec: VariantSpec = {
            "id": vid,
            "provider": provider,
            "name": combined_name,
            "repo": target_repo,
            "local_path": target_local_path,
            "draft_repo": draft_repo,
            "draft_local_path": draft_local_path,
            "files": None,
            "quantizations": existing_quantizations,
            "location": location,
            "cost": _cost_to_dict(cost) if cost is not None else None,
            "quantization": quantization,
            "model_info": (self._variant or {}).get("model_info"),
        }
        family = str(self.query_one("#family-select", Select).value)
        self.dismiss(ModelFormResult(spec=spec, family=family, pricing_updated_at=self._pricing_updated_at))


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
        show_price_reminder: bool = False,
    ) -> None:
        super().__init__()
        self._ready = ready
        self._deletes = deletes
        self._exposes = exposes or []
        self._moves = moves or []
        self._show_price_reminder = show_price_reminder

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
            if self._show_price_reminder:
                yield Label(
                    "token pricing may be stale — run `modelman refresh-prices`.",
                    id="price-reminder",
                )
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


class QuitBlockedModal(ModelmanModal[bool]):
    """Shown when the user tries to quit while downloads are active.

    Returns True if the user chose to review downloads (caller pushes
    DownloadScreen), False/None otherwise (stay put, downloads keep
    running).
    """

    BINDINGS = [
        Binding("escape", "answer(False)", show=False),
        ("r", "answer(True)"),
    ]

    def compose(self) -> ComposeResult:
        with Vertical():
            yield Label("Downloads are in progress.")
            yield Label("Cancel them from the download screen before quitting.")
            yield self._button_row(
                [
                    Button("Stay", id="stay", variant="default"),
                    Button("Review Downloads", id="review", variant="primary"),
                ]
            )

    def _modal_on_mount(self) -> None:
        self._focus_button("stay")

    def on_button_pressed(self, event: Button.Pressed) -> None:
        self.dismiss(event.button.id == "review")

    def action_answer(self, value: bool) -> None:
        self.dismiss(value)


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
