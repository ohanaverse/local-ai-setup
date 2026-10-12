"""Every failure modeltiers reports. The CLI turns each into exit code 1."""


class ModelTiersError(Exception):
    """Base class: the CLI prints the message and exits 1."""


class ConfigError(ModelTiersError):
    """A committed data file, a path or a flag is invalid."""


class InventoryError(ModelTiersError):
    """`wt model list --json` could not be run or read."""


class FetchError(ModelTiersError):
    """A download failed. `retryable` is true for a timeout, a 5xx status or
    an Arena `error` body: the cases that get one more attempt."""

    def __init__(self, message: str, *, retryable: bool = False) -> None:
        super().__init__(message)
        self.retryable = retryable


class SourceParseError(ModelTiersError):
    """A snapshot is not in the shape its parser knows, or is under its
    minimum row count."""


class SourceUnavailable(ModelTiersError):
    """One or more sources failed and have no good snapshot to fall back on."""

    def __init__(self, names: list[str]) -> None:
        super().__init__("no usable data and no good snapshot for: " + ", ".join(names))
        self.names = names


class OutputFileError(ModelTiersError):
    """`tiers.toml` is missing or unreadable when `show` or `explain` needs it."""
