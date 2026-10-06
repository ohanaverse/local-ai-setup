import pytest


@pytest.fixture
def shared_owner():
    """Ask the shared-artifact guard, as the delete queue does: the id of the
    entry that owns something deleting `deleting` would remove, or None.

    `deleting` and `other` are the only two models in the registry, with one
    default provider row for each provider id they use. `provider` answers
    for `deleting`'s row; an entry on another row is asked through a provider
    built from that row."""

    def owner(provider, deleting, other):
        from modelman.registry import (
            ProviderEntry,
            Registry,
            find_shared_artifact_owner,
            model_entry_to_variant,
        )

        registry = Registry(
            providers=[
                ProviderEntry(id=provider_id, name=provider_id, location="local")
                for provider_id in dict.fromkeys((deleting.provider_id, other.provider_id))
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


def _other_case(tmp_path, directory, monkeypatch):
    swapped = directory.with_name(directory.name.swapcase())
    if not swapped.exists():
        pytest.skip("case-sensitive filesystem: another letter case is another directory")
    return str(swapped)


# #235: ways to name one directory that are not the string the provider
# derives for it. Each takes (tmp_path, directory, monkeypatch) and returns
# the spelling; `directory` is under tmp_path and already exists.
SPELLINGS = {
    "other-case": _other_case,
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
