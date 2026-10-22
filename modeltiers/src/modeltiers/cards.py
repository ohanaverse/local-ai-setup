"""Model cards: where a click-through from a model's canonical key goes.

The link is worked out by `run` and written to `tiers.toml`, so `show` and
the page, which read that file alone, fetch nothing. The first rule that
answers wins:

1. the record OpenRouter gives the model names its open weights' Hugging Face
   repository: that card, which the lab itself writes;
2. the key's lab (the part before the `/`) is in `vendors.toml`: the lab's own
   catalog of its models, a directory and not the model's page;
3. OpenRouter scored the model: its OpenRouter page;
4. no link.

`vendors.toml` holds each lab's address as it was read. Labs move their
catalogs, so a run follows each address's redirects and keeps where they end
in `<cache>/vendor-links.toml`; `refresh` says which of the three an address
came from.
"""

import tomllib
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from pathlib import Path
from typing import Any
from urllib.parse import quote, urlsplit

import tomli_w

from modeltiers import configfile
from modeltiers.errors import ConfigError, FetchError
from modeltiers.fetch import Resolver
from modeltiers.records import ScoreRecord

HUGGINGFACE = "huggingface"
VENDOR = "vendor"
OPENROUTER = "openrouter"
KINDS = (HUGGINGFACE, VENDOR, OPENROUTER)

CACHE_FILE = "vendor-links.toml"
_VENDOR_KEYS = frozenset({"prefix", "url", "note"})

# lab prefix, in lower case -> the address of its catalog of models
Vendors = Mapping[str, str]


@dataclass(frozen=True)
class Card:
    """A link and which of the rules above gave it."""

    kind: str  # one of KINDS
    url: str


def is_https(url: str) -> bool:
    """True for an address a link may point at: https, with a host, and no
    space or control character. A page writes a card's address as an `href`
    and must never be handed anything else."""
    parts = urlsplit(url)
    return (
        parts.scheme == "https"
        and bool(parts.netloc)
        and url.isprintable()
        and not any(c.isspace() for c in url)
    )


def parse_vendors(text: str, *, origin: str = "vendors.toml") -> Vendors:
    """The file's `[[vendor]]` entries as prefix -> address. Raises ConfigError
    for a missing or unknown key, an address that is not https, a prefix that is
    not lower case or is listed twice, and an entry with no note."""
    doc = configfile.parse_toml(origin, text)
    found: dict[str, str] = {}
    for number, entry in enumerate(configfile.tables(origin, doc, "vendor"), start=1):
        where = f"{origin}: [[vendor]] #{number}"
        configfile.known_keys(where, entry, _VENDOR_KEYS)
        prefix = configfile.text(where, entry, "prefix")
        url = configfile.text(where, entry, "url")
        configfile.text(where, entry, "note")
        if prefix != prefix.lower() or "/" in prefix:
            raise ConfigError(f"{where}: prefix {prefix!r} is not a lower-case lab name")
        if not is_https(url):
            raise ConfigError(f"{where}: url {url!r} is not an https address")
        if prefix in found:
            raise ConfigError(f"{where}: prefix {prefix!r} is listed twice")
        found[prefix] = url
    return found


def load_vendors(path: Path | None = None) -> Vendors:
    """Read `path`, or the committed `data/vendors.toml` when it is None."""
    return configfile.load(path, "vendors", parse_vendors)


def read_cache(cache: Path, vendors: Vendors) -> dict[str, str]:
    """What an earlier run kept: prefix -> address. Each entry says which
    committed address it was reached from (`from`), and one whose `from` is no
    longer the address in `vendors.toml` is dropped, so a corrected address is
    never shadowed by an old answer. A file that is missing or unreadable, and
    any entry that is malformed or not an https address, reads as absent; it
    is a convenience and never stops a run."""
    try:
        doc = tomllib.loads((cache / CACHE_FILE).read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return {}
    kept: dict[str, str] = {}
    for prefix, entry in doc.items():
        if not isinstance(entry, dict) or prefix not in vendors:
            continue
        url = entry.get("url")
        if entry.get("from") == vendors[prefix] and isinstance(url, str) and is_https(url):
            kept[prefix] = url
    return kept


def cache_text(urls: Mapping[str, str], vendors: Vendors) -> str:
    return tomli_w.dumps(
        {p: {"from": vendors[p], "url": url} for p, url in urls.items() if p in vendors}
    )


@dataclass(frozen=True)
class Refreshed:
    """The addresses a run uses, and what to tell the person about them."""

    urls: dict[str, str]  # prefix -> address, for every lab in the file
    answered: frozenset[str]  # the prefixes whose address a live check just confirmed
    notes: tuple[str, ...]  # a redirect followed, or a check that failed


def refresh(
    vendors: Vendors,
    cache: Mapping[str, str],
    resolver: Resolver | None,
    warn: Callable[[str], None] = lambda _message: None,
) -> Refreshed:
    """The address of each lab's catalog: where its redirects end now, else the
    address an earlier run kept, else the one in `vendors.toml`. `resolver` is
    None for an offline run, which checks nothing. An answer that is not an
    https address is a failed check. `warn` is called with each note. Only an
    address a check confirmed is worth keeping (`Refreshed.answered`): one that
    never answered would shadow the committed address of a later release."""
    urls: dict[str, str] = {}
    notes: list[str] = []
    answered: set[str] = set()
    for prefix, committed in vendors.items():
        start = cache.get(prefix, committed)
        urls[prefix] = start
        if resolver is None:
            continue
        # An address an earlier run kept may have died since; the committed one
        # is tried next, so a move that was undone is found.
        for tried in dict.fromkeys((start, committed)):
            try:
                final = resolver(tried)
            except FetchError as exc:
                notes.append(
                    f"vendor link {prefix}: could not check it ({exc}); keeping {urls[prefix]}"
                )
                continue
            if not is_https(final):
                notes.append(
                    f"vendor link {prefix}: {tried} ended at {final!r}, not https; keeping {urls[prefix]}"
                )
                continue
            if final != urls[prefix]:
                notes.append(f"vendor link {prefix}: {urls[prefix]} moved to {final}")
            urls[prefix] = final
            answered.add(prefix)
            break
    for note in notes:
        warn(note)
    return Refreshed(urls=urls, answered=frozenset(answered), notes=tuple(notes))


def card_for(key: str, record: ScoreRecord | None, vendors: Vendors) -> Card | None:
    """The link for a canonical key. `record` is OpenRouter's record for it,
    when it has one."""
    if record is not None and record.hugging_face_id:
        return Card(
            HUGGINGFACE, f"https://huggingface.co/{quote(record.hugging_face_id, safe='/')}"
        )
    lab = key.split("/", 1)[0].lower()
    if lab in vendors:
        return Card(VENDOR, vendors[lab])
    if record is not None:
        return Card(OPENROUTER, f"https://openrouter.ai/{quote(record.source_id, safe='/:')}")
    return None


def describe(entry: Mapping[str, Any]) -> Card | None:
    """The card a `[[models]]` or `[[candidates]]` entry of the file holds, or
    None when it holds none or a kind or address a link may not use."""
    kind, url = entry.get("card_kind"), entry.get("card_url")
    if kind in KINDS and isinstance(url, str) and is_https(url):
        return Card(str(kind), url)
    return None
