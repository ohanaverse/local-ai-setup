import pytest


@pytest.fixture
def shared_owner():
    """Ask the shared-artifact guard, as the delete queue does: the id of the
    entry that owns something deleting `deleting` would remove, or None.

    `deleting` and `other` are the only two models in the registry, on one
    provider row built from `deleting`'s provider id."""

    def owner(provider, deleting, other):
        from modelman.registry import (
            ProviderEntry,
            Registry,
            find_shared_artifact_owner,
            model_entry_to_variant,
        )

        registry = Registry(
            providers=[
                ProviderEntry(id=deleting.provider_id, name=deleting.provider_id, location="local")
            ],
            models=[deleting, other],
        )
        found = find_shared_artifact_owner(registry, provider, model_entry_to_variant(deleting))
        return found.id if found else None

    return owner


def _tilde(tmp_path, directory, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    return "~/" + str(directory.relative_to(tmp_path))


def _symlink(tmp_path, directory, monkeypatch):
    link = tmp_path / "link-to-model"
    link.symlink_to(directory, target_is_directory=True)
    return str(link)


# #235: ways to name one directory that are not the string the provider
# derives for it. Each takes (tmp_path, directory, monkeypatch) and returns
# the spelling; `directory` is under tmp_path.
SPELLINGS = {
    "tilde": _tilde,
    "trailing-slash": lambda tmp_path, directory, monkeypatch: f"{directory}/",
    "dot-dot": lambda tmp_path, directory, monkeypatch: str(
        directory.parent / "x" / ".." / directory.name
    ),
    "symlink": _symlink,
}


@pytest.fixture(params=sorted(SPELLINGS))
def respell(request, tmp_path, monkeypatch):
    """A function giving another spelling of a directory under tmp_path."""
    return lambda directory: SPELLINGS[request.param](tmp_path, directory, monkeypatch)
