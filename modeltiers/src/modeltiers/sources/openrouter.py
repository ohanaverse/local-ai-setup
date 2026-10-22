"""OpenRouter's models API: `data[].benchmarks.artificial_analysis`."""

from modeltiers.errors import SourceParseError
from modeltiers.records import ScoreRecord
from modeltiers.sources._shape import load_json, number, optional_number, require_rows

SOURCE = "openrouter"
MIN_ROWS = 100


def parse(data: bytes, *, min_rows: int = MIN_ROWS) -> tuple[ScoreRecord, ...]:
    """One record per model that has an intelligence index. A model with no
    `benchmarks`, no `artificial_analysis` or a null index has no score and
    gives no record. The minimum counts records, not models."""
    doc = load_json(SOURCE, data)
    models = doc.get("data") if isinstance(doc, dict) else None
    if not isinstance(models, list):
        raise SourceParseError(f"{SOURCE}: expected an object with a data list")
    records: list[ScoreRecord] = []
    for index, model in enumerate(models):
        if not isinstance(model, dict) or not isinstance(model.get("id"), str):
            raise SourceParseError(f"{SOURCE}: data[{index}] has no string id")
        model_id: str = model["id"]
        benchmarks = model.get("benchmarks")
        if benchmarks is None:
            continue
        if not isinstance(benchmarks, dict):
            raise SourceParseError(f"{SOURCE}: {model_id}.benchmarks is not an object")
        indices = benchmarks.get("artificial_analysis")
        if indices is None:
            continue
        if not isinstance(indices, dict):
            raise SourceParseError(f"{SOURCE}: {model_id}.artificial_analysis is not an object")
        if indices.get("intelligence_index") is None:
            continue
        hugging_face_id = model.get("hugging_face_id")
        records.append(
            ScoreRecord(
                source=SOURCE,
                source_id=model_id,
                score=number(
                    SOURCE, f"{model_id}.intelligence_index", indices["intelligence_index"]
                ),
                coding_index=optional_number(
                    SOURCE, f"{model_id}.coding_index", indices.get("coding_index")
                ),
                agentic_index=optional_number(
                    SOURCE, f"{model_id}.agentic_index", indices.get("agentic_index")
                ),
                # "" is how a closed model says it has none.
                hugging_face_id=hugging_face_id
                if isinstance(hugging_face_id, str) and hugging_face_id
                else None,
            )
        )
    require_rows(SOURCE, len(records), min_rows)
    return tuple(records)
