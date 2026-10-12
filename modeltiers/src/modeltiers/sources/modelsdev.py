"""models.dev `api.json`: an object of providers, each with a `models` object
keyed by the host's own model id."""

from modeltiers.errors import SourceParseError
from modeltiers.records import ModelsDev, ModelsDevRow
from modeltiers.sources._shape import load_json, require_rows

SOURCE = "modelsdev"
MIN_ROWS = 1000


def _flag(where: str, row: dict[str, object], key: str) -> bool:
    value = row.get(key, False)
    if not isinstance(value, bool):
        raise SourceParseError(f"{SOURCE}: {where}.{key} is not a boolean: {value!r}")
    return value


def _context(where: str, row: dict[str, object]) -> int | None:
    limit = row.get("limit")
    if limit is None:
        return None
    if not isinstance(limit, dict):
        raise SourceParseError(f"{SOURCE}: {where}.limit is not an object")
    context = limit.get("context")
    if context is None:
        return None
    if isinstance(context, bool) or not isinstance(context, int):
        raise SourceParseError(f"{SOURCE}: {where}.limit.context is not an integer: {context!r}")
    return context


def parse(data: bytes, *, min_rows: int = MIN_ROWS) -> ModelsDev:
    """Every provider-model row. A missing `tool_call` or `open_weights` reads
    as false, a missing `canonical_model_id` or `limit.context` as None."""
    doc = load_json(SOURCE, data)
    if not isinstance(doc, dict):
        raise SourceParseError(f"{SOURCE}: expected an object of providers")
    rows: list[ModelsDevRow] = []
    for provider, entry in doc.items():
        models = entry.get("models") if isinstance(entry, dict) else None
        if not isinstance(models, dict):
            raise SourceParseError(f"{SOURCE}: provider {provider} has no models object")
        for model_id, row in models.items():
            where = f"{provider}/{model_id}"
            if not isinstance(row, dict):
                raise SourceParseError(f"{SOURCE}: {where} is not an object")
            canonical = row.get("canonical_model_id")
            if canonical is not None and not isinstance(canonical, str):
                raise SourceParseError(f"{SOURCE}: {where}.canonical_model_id is not a string")
            rows.append(
                ModelsDevRow(
                    provider=provider,
                    model_id=model_id,
                    canonical=canonical or None,
                    tool_call=_flag(where, row, "tool_call"),
                    open_weights=_flag(where, row, "open_weights"),
                    context=_context(where, row),
                )
            )
    require_rows(SOURCE, len(rows), min_rows)
    return ModelsDev(rows=tuple(rows))
