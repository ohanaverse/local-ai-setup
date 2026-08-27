# Spec: simplify the add-model dialog

**Date:** 2026-09-02
**Status:** approved (in chat)
**Branch:** feat/tui

## Problem

The add-model dialog asks for six fields (provider, id, name, repo,
files, quantizations). Only one of these actually drives the
download:

- **ollama**: `name` (the ollama tag, e.g. `ornith-1.5:35b`).
- **llamacpp / omlx**: `repo` (HF repo id) and optionally `files`
  (one filename within the repo for per-quant GGUF selection).

The other fields are either vestigial (`quantizations` — oMLX
ignores it today), display-only (`name` for HF providers — never
read by the provider), or hand-rolled unique identifiers (`id`,
`provider` — provider is already determined by the model screen's
left-pane selection).

Worse, the dialog demands the user break the HF copy-paste flow in
half: HF's web UI offers a one-click copy of "the repo name"
(`unsloth/Ornith-1.5-35B-GGUF`) or "a path within the repo"
(`unsloth/Ornith-1.5-35B-GGUF/Ornith-1.5-35B-Q8_0.gguf`), and the
user currently has to manually split that into repo + file fields.

## Goal

The dialog accepts the HF paste format directly. One input field
called `model`. The dialog parses it (provider-dispatched) into the
underlying spec fields, and the providers consume the same shape
they always have.

Non-goals:
- Re-modeling `VariantSpec`. It still has `name`, `repo`, `files`,
  `quantizations`, `model_info`, etc. We're only simplifying the
  **dialog**, not the on-disk spec.
- Migrating existing manifests. They load unchanged. The new id
  derivation only applies to variants created via the dialog going
  forward.
- Adding HF dataset support, revision pinning, or auth/token UX.
  Those are separate changes.

## Design

### Dialog field

One Input widget, id `#model`. Placeholder depends on the provider
(set in `compose()` since the provider is known):

- ollama: `"e.g. ornith-1.5:35b"`
- llamacpp / omlx: `"org/repo[/path/to/file]"`

### Parsing

Provider-dispatched. Helper lives in `screens/forms.py` (next to
ModelForm) since it's only used by the dialog:

```python
def parse_model(provider: str, model: str) -> tuple[str | None, str | None, str | None]:
    """Return (variant_name, repo_id, filename) for the given provider.

    ollama: name = model, repo = None, filename = None.
    llamacpp / omlx: parse on '/'.
        1 segment -> invalid, raises ValueError.
        2 segments -> repo = model, filename = "".
        3+ segments -> repo = first two joined by '/',
                        filename = rest joined by '/'.
    """
```

The dialog calls `parse_model()` on Save. If it raises, the form
shows an inline error message (red Label under the input) and
returns without dismissing. If it returns, the dialog constructs
the spec from the parsed values.

### Spec construction

For all providers:
- `id` is derived (see below).
- `provider` is the static value from the model screen.
- `model_info` is auto-detected for ollama via the existing
  `ollama_caps.auto_detect_model_info(name)`, else `None`.

Per-provider field mapping:

- ollama: `name = model_input`, `repo = None`, `files = None`,
  `quantizations = None`.
- llamacpp / omlx: `name = model_input` (same as user typed, for
  display), `repo = parsed_repo_id`, `files = [parsed_filename]`
  if filename else `None`, `quantizations = None`.

### ID derivation

`id = f"{provider}/{model_input.replace('/', '--')}"`

Examples:
- `ollama/ornith-1.5:35b`
- `llamacpp/unsloth--Ornith-1.5-35B-GGUF--Ornith-1.5-35B-Q8_0.gguf`
- `omlx/org--repo`

Why escape `/` to `--`: the id is used as a dict key (in
`downloaded`), a row key in the model screen's DataTable, and as a
display string. If we kept slashes, `llamacpp/org/repo` is
ambiguous (is the provider `llamacpp`? `llamacpp/org`?). Replacing
`/` with `--` makes the boundary unambiguous and reversible for
display purposes.

Edit mode preserves the existing variant's id verbatim — the
parser doesn't re-derive ids. New variants get the new scheme.

### Validation

On Save:

1. Trim whitespace; reject empty input.
2. If provider is ollama: input must NOT contain `/`. (Ollama tags
   don't contain `/`; if you want to namespace a tag like
   `user/model:tag`, that's an ollama feature unrelated to this
   dialog and out of scope.)
3. If provider is llamacpp / omlx: input must contain at least one
   `/`. Split on `/`. Result must have >= 2 segments. The first
   segment (org) must be non-empty.

Any failure shows a red `Label(id="model-error")` under the input
with the message and aborts the submit.

### Edit mode

The Edit dialog pre-fills the input from the existing variant's
parsed representation:

- ollama: pre-fill with `variant["name"]`.
- llamacpp / omlx: pre-fill with the model_input that would have
  produced this spec. Reconstruction:
  ```
  model_input = variant["repo"]
  if variant["files"]:
      model_input += "/" + variant["files"][0]
  ```

Submitting re-parses the input and rebuilds the spec. Provider and
id are preserved (id from the variant, not re-derived).

## File map

Files touched:

- `src/modelman/screens/forms.py`
  - Add `parse_model(provider, model)` helper.
  - Reduce `ModelForm.compose()` to: provider label, model Input,
    error Label, Cancel/Save buttons.
  - Change `ModelForm._submit()` to use `parse_model()` and write
    the spec from the parsed result.
  - Remove `quantizations` and the redundant `name` / `repo` /
    `files` Inputs.
  - Edit-mode pre-fill logic.

- `tests/screens/test_forms.py`
  - Add unit tests for `parse_model()`.
  - Update existing ModelForm tests to reflect the single-input
    shape.
  - Add a test that verifies validation errors block Save.
  - Add an edit-mode test that verifies pre-fill reconstructs the
    input from existing repo+files.

Files NOT touched:

- `src/modelman/providers/*.py` — providers already consume
  `variant["repo"]` and `variant["files"]`. No change needed.
- `src/modelman/manifest.py` — `VariantSpec` is unchanged.
- `src/modelman/queue.py` — uses `variant["id"]` (still works) and
  `variant["repo"]` / `variant["files"]` (still works).
- `src/modelman/screens/models.py` — `action_add_model` still calls
  `ModelForm(providers=...)`. The constructor signature doesn't
  change.
- `src/modelman/screens/families.py` — display unchanged.

## Testing strategy

TDD. Tests first.

1. `parse_model` unit tests:
   - ollama tag returns name verbatim, no repo/files.
   - ollama tag with `/` raises ValueError.
   - HF 2-segment returns repo + empty filename.
   - HF 3-segment returns repo + filename.
   - HF deep path returns repo + joined filename.
   - HF 1-segment raises ValueError.

2. ModelForm tests (replacing the field-shape assertions):
   - Submitting with a valid ollama tag produces the right spec.
   - Submitting with a valid HF repo produces the right spec.
   - Submitting with a valid HF repo/file produces the right spec.
   - Submitting with invalid input shows an error and doesn't dismiss.
   - Edit mode pre-fills the input correctly from existing spec.

3. Existing test files (`tests/test_manifest.py`,
   `tests/test_providers/test_llamacpp.py`,
   `tests/test_providers/test_omlx.py`,
   `tests/screens/test_app_navigation.py`) — these should keep
   passing without changes. If any break, that's a signal the spec
   change has wider impact than expected, and we'll deal with it.

## Risks

- **Existing manifests with hand-rolled ids** (`ollama-35b`,
  `llamacpp-35b-q8`). These keep working — no migration. New
  variants get the new id scheme; they coexist.
- **User accidentally edits an existing variant and the new id
  derivation kicks in.** Edit mode preserves the existing id, so
  this doesn't happen. The test covers it.
- **id collisions.** Two variants with the same `(provider, model)`
  in the same family would collide. Today the dialog would have
  produced the same collision since the user picks whatever id
  they want. No regression.
- **`/display` in the UI.** Today, when the model screen shows
  the variant id, it'd display
  `llamacpp/unsloth--Ornith-1.5-35B-GGUF--Ornith-1.5-35B-Q8_0.gguf`,
  which is long. The display currently shows just the variant id
  in the table; we may want to shorten it later. Out of scope for
  this spec.

## Open question

Should the dialog offer a "browse HF" affordance to look up repos
by name, or just rely on the user pasting from the HF web UI? Out
of scope for this spec. Revisit later if/when the user asks.
