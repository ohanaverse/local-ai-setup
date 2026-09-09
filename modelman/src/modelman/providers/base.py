"""Abstract base class for model providers."""

from __future__ import annotations

from abc import ABC, abstractmethod
from typing import Any, Protocol, TypedDict


class _Runner(Protocol):
    """Shared shape for the optional subprocess/HF-call runner providers
    accept so tests can substitute a mock. `*args` (rather than a fixed
    `args: list[str]`) is the more general signature: it covers ollama's
    single-list-positional call style and llamacpp/omlx's freeform
    `snapshot_download`-style calls without narrowing either."""

    def __call__(self, *args: Any, **kwargs: Any) -> Any: ...


class VariantSpec(TypedDict, total=False):
    """A single model variant within a family manifest. All fields optional
    in the TypedDict sense, but providers require specific ones at runtime."""

    id: str  # stable id within the family
    provider: str  # "ollama" | "llamacpp" | "omlx" | "mlx_lm_server"
    name: str  # provider-specific (e.g. "ornith-1.5:35b" for ollama)
    repo: str | None  # HF repo id (for llamacpp/omlx); the mlx_lm_server
    # target's repo when locally-produced isn't in play
    files: list[str] | None  # files in repo (for llamacpp)
    quantizations: list[str] | None  # quant tags (for omlx)
    local_path: str | None  # locally-produced mlx-lm model directory (no HF repo)
    draft_repo: str | None  # mlx_lm_server speculative-decoding draft: HF repo id
    draft_local_path: str | None  # mlx_lm_server draft: locally-produced model directory
    model_info: dict | None  # freeform LiteLLM model_info keys
    location: str | None  # "local" | "cloud"
    # Cost as a plain dict so providers can JSON-serialize VariantSpec if
    # needed; None when unset. Use registry._cost_from_dict() to rebuild a
    # registry.Cost object.
    cost: dict[str, Any] | None


class LocalModel(TypedDict):
    """A model that exists on the local machine."""

    variant_id: str
    path: str
    size_bytes: int | None


class Provider(ABC):
    """Base class for all model providers."""

    name: str = ""

    def __init__(self, config: dict):
        self.config = config

    @abstractmethod
    def is_downloaded(self, variant: VariantSpec) -> bool: ...

    @abstractmethod
    def download(self, variant: VariantSpec) -> str:
        """Download the variant. Returns the local path on success."""
        ...

    @abstractmethod
    def list_local(self) -> list[LocalModel]: ...

    def resolve_local(
        self, variants: list[VariantSpec]
    ) -> list[LocalModel | None] | None:
        """Resolve several variants in one pass: presence, on-disk path, and
        size for each, positionally aligned with `variants`.

        Returns `None` when the provider has no batch implementation —
        callers fall back to per-variant `is_downloaded()`/`size_of()`
        (and `list_local()` for paths). Providers whose check is one
        subprocess serving every variant (e.g. ollama's single `ollama
        list`) override this so a caller reconciling N models costs one
        subprocess instead of N.

        Per-variant results mirror the per-variant methods: `None` means
        not downloaded; a `LocalModel` means present (with whatever
        `path`/`size_bytes` the provider can report — either may be
        unknown). A default "resolve everything via the per-variant
        methods" implementation would defeat the point; the base returns
        `None` so unimplemented is unimplemented.
        """
        return None

    def size_of(self, variant: VariantSpec) -> int | None:
        """Return the on-disk size in bytes for this variant, or None if unknown.

        Providers override this when they can determine a size. Default is None
        so unknown providers don't crash the size columns.
        """
        return None

    def path_of(self, variant: VariantSpec) -> str | None:
        """Return the on-disk path for this variant, or None if not downloaded.

        Providers override this when they can locate a downloaded model. Default
        is None so unknown providers don't crash path columns.
        """
        return None

    def artifact_paths(self, variant: VariantSpec) -> frozenset[str]:
        """Return the set of on-disk paths that make up this variant.

        Defaults to a single-element set from path_of(). Providers whose
        artifact spans multiple directories (e.g. mlx_lm_server's target+
        draft pairing) override this so find_shared_artifact_owner() can
        detect sharing on *any* side and avoid deleting weights still in
        use by another registry entry.
        """
        p = self.path_of(variant)
        return frozenset([p]) if p is not None else frozenset()

    def cleanup_partial_download(self, variant: VariantSpec) -> None:
        """Remove any on-disk remnants of a cancelled or failed download.

        Called by DownloadManager after a download is cancelled or fails.
        Default is a no-op: providers whose download mechanism has no
        partial-artifact cleanup to do (Ollama's `pull` is resumable and
        reconciles its own state on the next attempt) don't need to
        override this.
        """
        return
