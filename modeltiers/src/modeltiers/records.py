"""The plain data every stage passes on. All frozen; no behaviour."""

from collections.abc import Mapping
from dataclasses import dataclass


@dataclass(frozen=True)
class InventoryRow:
    """One model wt reported. The join key is (provider_id, model_name)."""

    provider_id: str
    model_name: str
    location: str  # "cloud" | "local"
    registered: bool


@dataclass(frozen=True)
class Inventory:
    """`wt model list --json`, without its native rows."""

    rows: tuple[InventoryRow, ...]
    registry: str  # the registry path wt read
    providers: Mapping[str, str]  # provider family -> probe status
    native_dropped: int = 0


@dataclass(frozen=True)
class ModelsDevRow:
    """One provider-model row of models.dev."""

    provider: str  # models.dev provider id, e.g. "ollama-cloud"
    model_id: str  # the host's own id, e.g. "glm-5.3"
    canonical: str | None  # canonical_model_id, in models.dev's spelling
    tool_call: bool
    open_weights: bool
    context: int | None  # limit.context


@dataclass(frozen=True)
class ModelsDev:
    """Every provider-model row of one models.dev snapshot."""

    rows: tuple[ModelsDevRow, ...]


@dataclass(frozen=True)
class ScoreRecord:
    """One scored row of a benchmark source.

    `score` is the voting signal: OpenRouter's intelligence index, Arena's
    agent score, Epoch's capabilities index. `source_id` is the id as the
    source spells it; Arena's has its effort parenthesis removed and kept in
    `effort`.
    """

    source: str  # "openrouter" | "arena" | "epoch"
    source_id: str
    score: float
    ci_lower: float | None = None
    effort: str | None = None
    coding_index: float | None = None  # OpenRouter only; evidence, no vote
    agentic_index: float | None = None  # OpenRouter only; evidence, no vote
    hugging_face_id: str | None = None  # OpenRouter only; names the open weights' card
