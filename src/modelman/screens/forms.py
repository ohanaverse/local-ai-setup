"""Modal forms for the TUI."""

from __future__ import annotations

from typing import Literal, cast

from textual.app import ComposeResult
from textual.containers import Horizontal, Vertical
from textual.screen import ModalScreen
from textual.widgets import Button, Input, Label

from ..ollama_caps import auto_detect_model_info
from ..providers.base import VariantSpec


def parse_model(provider: str, model: str) -> tuple[str | None, str | None, str | None]:
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

    Leading/trailing whitespace on `model` is trimmed before parsing.
    Empty input raises ValueError for HF providers.
    """
    model = model.strip()
    if provider == "ollama":
        if "/" in model:
            raise ValueError("ollama tags must not contain '/'")
        if not model:
            raise ValueError("ollama tag is required")
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


class AddFamilyModal(ModalScreen[tuple[str, str] | None]):
    """Prompt for a family name and optional display name.

    Returns `(family, display_name)` on Create — display_name falls
    back to family when left blank. FamilyScreen owns the StateStore
    mutation + save after this dismisses; the modal itself performs
    no disk I/O (mirrors ModelForm returning a VariantSpec dict for
    ModelScreen to apply to the Registry).
    """

    DEFAULT_CSS = """
    AddFamilyModal { align: center middle; }
    AddFamilyModal > Vertical { width: 60; height: auto; padding: 1 2; border: round $primary; }
    AddFamilyModal Label { margin-bottom: 1; }
    AddFamilyModal Input { margin-bottom: 1; }
    AddFamilyModal Horizontal { height: auto; align-horizontal: right; }
    AddFamilyModal Button { margin-left: 1; }
    """

    def compose(self) -> ComposeResult:
        with Vertical():
            yield Label("Family name (required):")
            yield Input(id="family-name", placeholder="e.g. ornith-1.5")
            yield Label("Display name (optional):")
            yield Input(id="display-name", placeholder="e.g. Ornith 1.5")
            with Horizontal():
                yield Button("Cancel", id="cancel", variant="default")
                yield Button("Create", id="create", variant="primary")

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


class EditFamilyModal(ModalScreen[str | None]):
    """Edit the display_name of an existing family.

    The family slug is intentionally NOT editable here — changing it
    would orphan cross-references from models keyed by family. The
    slug is shown read-only so the user knows which family they're
    editing.

    Returns the new display_name on Save (falls back to the family
    slug if blanked, matching AddFamilyModal); None on Cancel.
    FamilyScreen owns the StateStore mutation + save.
    """

    DEFAULT_CSS = """
    EditFamilyModal { align: center middle; }
    EditFamilyModal > Vertical { width: 60; height: auto; padding: 1 2; border: round $primary; }
    EditFamilyModal Label { margin-bottom: 1; }
    EditFamilyModal Input { margin-bottom: 1; }
    EditFamilyModal Horizontal { height: auto; align-horizontal: right; }
    EditFamilyModal Button { margin-left: 1; }
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
            with Horizontal():
                yield Button("Cancel", id="cancel", variant="default")
                yield Button("Save", id="save", variant="primary")

    def on_mount(self) -> None:
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


class ConfirmModal(ModalScreen[bool]):
    """Generic yes/no confirmation. Default is No."""

    BINDINGS = [
        ("y", "answer(True)"),
        ("n", "answer(False)"),
        ("escape", "answer(False)"),
    ]

    DEFAULT_CSS = """
    ConfirmModal { align: center middle; }
    ConfirmModal > Vertical { width: 60; height: auto; padding: 1 2; border: round $primary; }
    ConfirmModal Label { margin-bottom: 1; }
    ConfirmModal Horizontal { height: auto; align-horizontal: right; }
    ConfirmModal Button { margin-left: 1; }
    """

    def __init__(self, message: str) -> None:
        super().__init__()
        self._message = message

    def compose(self) -> ComposeResult:
        with Vertical():
            yield Label(self._message)
            with Horizontal():
                yield Button("No", id="no", variant="default")
                yield Button("Yes", id="yes", variant="warning")

    def on_button_pressed(self, event: Button.Pressed) -> None:
        self.dismiss(event.button.id == "yes")

    def action_answer(self, value: bool) -> None:
        self.dismiss(value)


class ModelForm(ModalScreen[VariantSpec | None]):
    """Add or edit a model. `variant=None` means add; else edit.

    The dialog asks for exactly one thing: the model name. The
    provider is locked in by the caller (model screen's left-pane
    selection for add; variant's own provider for edit). The model
    name is parsed provider-dispatched: ollama tags go straight to
    `variant.name`; HF model names are split into `repo` and
    `files` for llamacpp / omlx.

    See `parse_model()` for the parsing rules.
    """

    DEFAULT_CSS = """
    ModelForm { align: center middle; }
    ModelForm > Vertical { width: 80; height: auto; padding: 1 2; border: round $primary; }
    ModelForm Label { margin-top: 1; }
    ModelForm Input { margin-bottom: 1; }
    ModelForm #model-error { color: $error; text-style: bold; }
    ModelForm Horizontal { height: auto; align-horizontal: right; }
    ModelForm Button { margin-left: 1; }
    ModelForm #provider-label { color: $secondary; text-style: bold; }
    """

    def __init__(
        self,
        providers: list[str],
        variant: VariantSpec | None = None,
        default_provider: str | None = None,
    ) -> None:
        super().__init__()
        self._providers = providers
        self._variant = variant  # None for add
        # Used only in add mode (variant is None): the provider to
        # pre-fill in the static provider label. Edit mode always
        # uses the variant's own provider since provider is
        # immutable on edit.
        self._default_provider = default_provider

    def compose(self) -> ComposeResult:
        editing = self._variant is not None
        v: VariantSpec = self._variant if self._variant is not None else cast("VariantSpec", {})
        # Provider precedence: variant's own (edit) > explicit default
        # (add, when caller passes one and it's a known provider) > first
        # configured provider. The form doesn't render a Select — the
        # provider is fixed at compose time.
        if editing:
            initial_provider = v.get("provider") or self._providers[0]
        elif self._default_provider and self._default_provider in self._providers:
            initial_provider = self._default_provider
        else:
            initial_provider = self._providers[0]
        self._initial_provider: str = initial_provider

        # Reconstruct the dialog's single `model` input from the variant.
        # Edit mode pre-fills so the user sees the same string they would
        # have typed to produce this spec.
        model_val = self._reconstruct_model(v) if editing else ""

        placeholder = (
            "e.g. ornith-1.5:35b" if initial_provider == "ollama" else "org/repo[/path/to/file]"
        )

        with Vertical():
            yield Label(f"Provider: {initial_provider}", id="provider-label")
            yield Label("Model:")
            yield Input(
                value=model_val,
                placeholder=placeholder,
                id="model",
            )
            yield Label("", id="model-error")
            with Horizontal():
                yield Button("Cancel", id="cancel", variant="default")
                yield Button("Save", id="save", variant="primary")

    @staticmethod
    def _reconstruct_model(v: VariantSpec) -> str:
        """Build the dialog's `model` string from a stored VariantSpec.

        For HF providers the model string is `repo` plus `/file` if a
        single filename is stored. For ollama it's just the tag.
        """
        if v.get("provider") == "ollama":
            return v.get("name") or ""
        repo = v.get("repo") or ""
        files = v.get("files") or []
        if files:
            return f"{repo}/{files[0]}"
        return repo

    def on_mount(self) -> None:
        # Focus the model input so the user can paste / type immediately.
        self.query_one("#model", Input).focus()

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

    def _submit(self) -> None:
        provider = self._initial_provider
        raw = self.query_one("#model", Input).value
        try:
            name, repo, filename = parse_model(provider, raw)
        except ValueError as exc:
            self._show_error(str(exc))
            return
        self._clear_error()

        # Derive the id: provider + escaped model. Edit mode preserves
        # the existing variant's id (provider and id are immutable on
        # edit per the spec).
        if self._variant is not None:
            vid = self._variant["id"]
        else:
            vid = f"{provider}/{name.replace('/', '--')}"  # type: ignore[union-attr]

        # Carry over existing quantizations in edit mode; add mode has
        # none because the single model input cannot express them.
        existing_quantizations = (
            (self._variant or {}).get("quantizations") if self._variant is not None else None
        )
        spec: VariantSpec = {
            "id": vid,
            "provider": provider,
            "name": name or vid,
            "repo": repo,
            # variant["files"] is a list per the existing spec shape;
            # empty filename => no file filter => files=None. A
            # non-empty filename => single-element list.
            "files": [filename] if filename else None,
            "quantizations": existing_quantizations,
        }
        if provider == "ollama" and self._variant is None and name:
            spec["model_info"] = auto_detect_model_info(name)
        else:
            spec["model_info"] = (self._variant or {}).get("model_info")
        self.dismiss(spec)


class ConfirmExitDialog(ModalScreen[Literal["apply", "cancel", "discard"]]):
    """Show pending downloads/deletes; let the user apply, cancel, or discard.

    - apply: confirm and run PendingChanges.apply() in the caller
    - cancel: return to the model screen, queue held in memory
    - discard: drop the queue, caller should restore the in-memory manifest
    """

    DEFAULT_CSS = """
    ConfirmExitDialog { align: center middle; }
    ConfirmExitDialog > Vertical { width: 70; height: auto; padding: 1 2; border: round $primary; }
    ConfirmExitDialog Label { margin-bottom: 1; }
    ConfirmExitDialog Horizontal { height: auto; align-horizontal: right; }
    ConfirmExitDialog Button { margin-left: 1; }
    """

    BINDINGS = [
        ("y", "answer('apply')"),
        ("n", "answer('cancel')"),
        ("d", "answer('discard')"),
    ]

    def __init__(
        self,
        downloads: list,
        deletes: list,
        exposes: list[tuple[str, bool]] | None = None,
    ) -> None:
        super().__init__()
        self._downloads = downloads
        self._deletes = deletes
        self._exposes = exposes or []

    def compose(self) -> ComposeResult:
        with Vertical():
            yield Label(
                f"Pending: download {len(self._downloads)} · delete {len(self._deletes)}"
                f" · expose {len(self._exposes)}"
            )
            for v in self._downloads:
                yield Label(f"  ↓ {v['id']} ({v['provider']})")
            for v in self._deletes:
                yield Label(f"  × {v['id']} ({v['provider']})")
            for model_id, exposed in self._exposes:
                mark = "L" if exposed else "–"
                yield Label(f"  {mark} {model_id}")
            yield Label("Apply, cancel, or discard these changes?")
            with Horizontal():
                yield Button("Cancel", id="cancel", variant="default")
                yield Button("Discard", id="discard", variant="warning")
                yield Button("Apply", id="apply", variant="primary")

    def on_button_pressed(self, event: Button.Pressed) -> None:
        bid = event.button.id
        if bid in ("apply", "cancel", "discard"):
            self.dismiss(bid)  # type: ignore[arg-type]

    def action_answer(self, value: str) -> None:
        if value in ("apply", "cancel", "discard"):
            self.dismiss(value)  # type: ignore[arg-type]


class CancelApplyDialog(ModalScreen[Literal["cancel", "wait"]]):
    """Ask the user whether to abort the running apply or keep waiting.

    - cancel: cancel the apply (kill current subprocess if possible, skip
      remaining items, do not save the manifest). Already-completed steps
      are NOT undone.
    - wait: dismiss the dialog; the apply keeps running.
    """

    DEFAULT_CSS = """
    CancelApplyDialog { align: center middle; }
    CancelApplyDialog > Vertical { width: 60; height: auto; padding: 1 2; border: round $warning; }
    CancelApplyDialog Label { margin-bottom: 1; }
    CancelApplyDialog Horizontal { height: auto; align-horizontal: right; }
    CancelApplyDialog Button { margin-left: 1; }
    """

    BINDINGS = [
        ("escape", "answer('wait')"),
        ("w", "answer('wait')"),
        ("c", "answer('cancel')"),
    ]

    def compose(self) -> ComposeResult:
        with Vertical():
            yield Label("Actions are still running.")
            yield Label("Cancel and stop here, or wait for them to finish?")
            with Horizontal():
                yield Button("Cancel", id="cancel", variant="warning")
                yield Button("Wait", id="wait", variant="primary")

    def on_button_pressed(self, event: Button.Pressed) -> None:
        bid = event.button.id
        if bid in ("cancel", "wait"):
            self.dismiss(bid)  # type: ignore[arg-type]

    def action_answer(self, value: str) -> None:
        if value in ("cancel", "wait"):
            self.dismiss(value)  # type: ignore[arg-type]
