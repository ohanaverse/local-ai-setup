"""Sync modelman's ollama cloud entries with https://ollama.com/pricing.

`parse_pricing` is the ONLY code that knows the page's HTML shape. When
Ollama changes the page, `modelman ollama-catalog sync` exits 3 and saves
the raw HTML; update `parse_pricing` (and tests/fixtures/
ollama_pricing.html) — nothing else should need to change.
See docs/superpowers/specs/2026-09-28-ollama-catalog-sync-design.md.
"""

from __future__ import annotations

import hashlib
import re
import subprocess
import tempfile
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass, field, replace
from datetime import UTC, datetime
from html.parser import HTMLParser
from pathlib import Path
from typing import Any

import requests

from .providers.ollama import _says_not_found
from .registry import Cost, ModelEntry, Registry, _cost_to_dict
from .time_pricing import TimePrice, Window

PRICING_URL = "https://ollama.com/pricing"
OFFPEAK_LABEL = "off-peak"
MIN_ROWS = 5

_WEEKDAYS = ["mon", "tue", "wed", "thu", "fri"]
_OFFPEAK_SUFFIX = re.compile(r"\s*\(\s*off[\s-]*peak\s*\)\s*$", re.IGNORECASE)
_PRICE = re.compile(r"^\$\s*(\d+(?:\.\d+)?)$")
_NO_PRICE = {"", "-", "—", "–"}
# What an ollama model name looks like once the off-peak suffix is gone. A
# cell that doesn't match (spaces, parens, footnote markers) means the page
# wording changed — fail loudly instead of registering a junk tag.
_MODEL_NAME = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:/-]*$")


class CatalogFetchError(Exception):
    """The pricing page could not be downloaded."""


class CatalogParseError(Exception):
    """The pricing page no longer has the shape parse_pricing expects."""

    def __init__(self, check: str) -> None:
        super().__init__(check)
        self.check = check


@dataclass(frozen=True)
class PriceTriple:
    input: float | None
    cache: float | None
    output: float | None
    # Fields ("input"/"cache"/"output") whose cell was present but not
    # recognized as a price. They are None above, but a sync must keep the
    # registry's existing value rather than clear it (only a real "-" cell
    # clears a price).
    unknown: frozenset[str] = frozenset()


@dataclass
class CatalogModel:
    name: str
    prices: PriceTriple
    offpeak: PriceTriple | None = None


@dataclass
class Catalog:
    models: list[CatalogModel]
    warnings: list[str] = field(default_factory=list)


def offpeak_time_price(p: PriceTriple) -> TimePrice:
    """Ollama's published off-peak window: outside 12:00-18:00 UTC on
    weekdays, and all day on weekends."""
    return TimePrice(
        label=OFFPEAK_LABEL,
        timezone="UTC",
        input_price_per_million=p.input,
        cache_price_per_million=p.cache,
        output_price_per_million=p.output,
        windows=[
            Window(days=list(_WEEKDAYS), start="00:00", end="12:00"),
            Window(days=list(_WEEKDAYS), start="18:00", end="24:00"),
            Window(days=["sat", "sun"], start="00:00", end="24:00"),
        ],
    )


_HTTP_SESSION: requests.Session | None = None


def _default_http_runner(url: str, **kwargs: Any) -> requests.Response:
    """One Session per process: the library tag lookups run 8-wide, and a
    bare `requests.get` per model re-handshakes TLS for every one of them.
    Safe to share across those threads — urllib3's pool is thread-safe and
    `http.cookiejar` (which `Session` mutates on a Set-Cookie) locks."""
    global _HTTP_SESSION
    if _HTTP_SESSION is None:
        _HTTP_SESSION = requests.Session()
    return _HTTP_SESSION.get(url, timeout=30, **kwargs)


def fetch_pricing_html(runner: Any = None) -> str:
    try:
        response = (runner or _default_http_runner)(PRICING_URL)
        response.raise_for_status()
        return str(response.text)
    except Exception as exc:  # noqa: BLE001 — any fetch failure is exit 2
        raise CatalogFetchError(str(exc)) from exc


def save_failed_html(html: str, directory: Path | None = None) -> Path:
    stamp = datetime.now().strftime("%Y%m%d-%H%M%S")
    path = Path(directory or tempfile.gettempdir()) / f"ollama-pricing-{stamp}.html"
    path.write_text(html)
    return path


class _TableCollector(HTMLParser):
    """Every <table> as a list of rows; each row a list of cell texts."""

    def __init__(self) -> None:
        super().__init__(convert_charrefs=True)
        self.tables: list[list[list[str]]] = []
        self._open: list[int] = []
        self._row: list[str] | None = None
        self._cell: list[str] | None = None

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        if tag == "table":
            self.tables.append([])
            self._open.append(len(self.tables) - 1)
        elif tag == "tr" and self._open:
            self._row = []
        elif tag in ("td", "th") and self._row is not None:
            self._cell = []

    def handle_endtag(self, tag: str) -> None:
        if tag in ("td", "th") and self._cell is not None and self._row is not None:
            self._row.append(" ".join("".join(self._cell).split()))
            self._cell = None
        elif tag == "tr" and self._row is not None and self._open:
            if self._row:
                self.tables[self._open[-1]].append(self._row)
            self._row = None
        elif tag == "table" and self._open:
            self._open.pop()

    def handle_data(self, data: str) -> None:
        if self._cell is not None:
            self._cell.append(data)


def _map_columns(header: list[str]) -> dict[str, int] | None:
    lower = [h.lower() for h in header]

    def find(pred: Any) -> int | None:
        return next((i for i, h in enumerate(lower) if pred(h)), None)

    cols = {
        "model": find(lambda h: "model" in h),
        "cache": find(lambda h: "cache" in h),
        "input": find(lambda h: "input" in h and "cache" not in h),
        "output": find(lambda h: "output" in h),
    }
    if any(v is None for v in cols.values()):
        return None
    return {k: v for k, v in cols.items() if v is not None}


def parse_pricing(html: str) -> Catalog:
    collector = _TableCollector()
    collector.feed(html)
    if not collector.tables:
        raise CatalogParseError("no <table> found on the page")

    found = None
    for table in collector.tables:
        if table and (cols := _map_columns(table[0])) is not None:
            found = (cols, table[1:])
            break
    if found is None:
        headers = [t[0] for t in collector.tables if t]
        raise CatalogParseError(
            f"no table has Model/Input/Cached/Output headers (found headers: {headers})"
        )
    cols, rows = found
    if len(rows) < MIN_ROWS:
        raise CatalogParseError(f"found {len(rows)} model rows, expected at least {MIN_ROWS}")

    warnings: list[str] = []
    cells_seen = 0
    unrecognized = 0

    def price(cell: str, where: str) -> float | None:
        nonlocal cells_seen, unrecognized
        cells_seen += 1
        text = cell.strip().replace(",", "")
        if text in _NO_PRICE:
            return None
        if (m := _PRICE.match(text)) is not None:
            return float(m.group(1))
        unrecognized += 1
        warnings.append(f"{where}: unrecognized price {cell!r}; existing price kept")
        return None

    width = max(cols.values()) + 1
    base: dict[str, PriceTriple] = {}
    offpeak: dict[str, PriceTriple] = {}
    order: list[str] = []
    for n, row in enumerate(rows, start=1):
        if len(row) < width:
            raise CatalogParseError(f"row {n} has {len(row)} cells, expected at least {width}")
        raw_name = row[cols["model"]]
        name = _OFFPEAK_SUFFIX.sub("", raw_name).strip()
        if not name:
            raise CatalogParseError(f"row {n} has an empty model name")
        if not _MODEL_NAME.match(name):
            raise CatalogParseError(f"row {n} model name {raw_name!r} is not an ollama tag")
        before = unrecognized
        values = {}
        unknown = set()
        for key, label in (("input", "input"), ("cache", "cached input"), ("output", "output")):
            values[key] = price(row[cols[key]], f"{name} {label}")
            if unrecognized > before:
                unknown.add(key)
                before = unrecognized
        triple = PriceTriple(**values, unknown=frozenset(unknown))
        target = offpeak if name != raw_name.strip() else base
        if name in target:
            raise CatalogParseError(f"duplicate row for {raw_name!r}")
        target[name] = triple
        if target is base:
            order.append(name)

    if cells_seen and unrecognized * 2 > cells_seen:
        raise CatalogParseError(
            f"{unrecognized} of {cells_seen} price cells unrecognized — price format changed?"
        )
    orphans = sorted(set(offpeak) - set(base))
    if orphans:
        raise CatalogParseError(f"off-peak rows with no base row: {orphans}")
    models = [CatalogModel(name, base[name], offpeak.get(name)) for name in order]
    return Catalog(models=models, warnings=warnings)


CATALOG_NAME_KEY = "catalog_name"
_OLLAMA = "ollama"


def cloud_tag(name: str) -> str:
    """Page name -> pulled ollama tag (`glm-5.3` -> `glm-5.3:cloud`,
    `gpt-oss:120b` -> `gpt-oss:120b-cloud`)."""
    return f"{name}-cloud" if ":" in name else f"{name}:cloud"


def is_cloud_tag(tag: str) -> bool:
    return tag.endswith(":cloud") or tag.endswith("-cloud")


LIBRARY_TAGS_URL = "https://ollama.com/library/{name}/tags"


def verified_tags(registry: Registry, ollama_tags: list[str]) -> dict[str, str]:
    """Page name -> tag, for catalog entries whose tag `ollama list` shows:
    a pulled tag exists, so it needn't be looked up again.

    A name claimed by two pulled entries (an earlier re-tag that saved its
    addition but was killed before deleting the entry it replaced) is left
    out — which of the two the page publishes is exactly what the library
    lookup answers, and answering it from the registry picks one by
    position. Unanswered, the lookup re-tags instead of removing the entry
    that is actually current."""
    pulled = set(ollama_tags)
    seen: dict[str, str] = {}
    ambiguous: set[str] = set()
    for m in registry.models:
        name = m.extra.get(CATALOG_NAME_KEY) if m.provider_id == _OLLAMA else None
        if not name or m.model_name not in pulled:
            continue
        if name in seen and seen[name] != m.model_name:
            ambiguous.add(name)
        seen[name] = m.model_name
    return {n: t for n, t in seen.items() if n not in ambiguous}


def _lookup_cloud_tag(name: str, runner: Any) -> tuple[str | None, str | None]:
    """(tag, warning) for a bare page name from its ollama.com/library page."""
    try:
        response = (runner or _default_http_runner)(LIBRARY_TAGS_URL.format(name=name))
        response.raise_for_status()
        html = str(response.text)
    except Exception as exc:  # noqa: BLE001 — any fetch failure skips this model
        return None, f"{name}: could not read its ollama.com/library tags ({exc}); skipped"
    tags = set(re.findall(rf'href="/library/{re.escape(name)}:([A-Za-z0-9._-]+)"', html))
    sized = sorted(t for t in tags if t.endswith("-cloud"))
    if "cloud" in tags:
        return f"{name}:cloud", None
    if len(sized) == 1:
        return f"{name}:{sized[0]}", None
    found = f"several cloud tags ({', '.join(sized)})" if sized else "no cloud tag"
    return None, f"{name}: {found} on ollama.com/library; skipped"


def resolve_cloud_tags(
    names: list[str], runner: Any = None, known: dict[str, str] | None = None
) -> tuple[dict[str, str | None], list[str]]:
    """Page name -> the cloud tag ollama actually publishes, or None.

    cloud_tag() is only a guess: some models have a `:cloud` alias, others
    only a sized one (`mistral-large-3:675b-cloud`). A bare name is looked
    up on its ollama.com/library tags page: `<name>:cloud` wins, else the
    single `<name>:*-cloud` tag. No tag, several, or an unreadable page
    resolves to None (with a warning) — never a guessed tag. A name that
    already pins a size (`gpt-oss:120b`) maps by cloud_tag() directly, and
    one in ``known`` (verified_tags()) keeps its pulled tag; the rest are
    looked up concurrently.
    """
    known = known or {}
    resolved: dict[str, str | None] = {}
    warnings: list[str] = []
    lookups = []
    for name in names:
        if ":" in name:
            resolved[name] = cloud_tag(name)
        elif name in known:
            resolved[name] = known[name]
        else:
            lookups.append(name)
    if lookups:
        with ThreadPoolExecutor(max_workers=min(8, len(lookups))) as pool:
            results = list(pool.map(lambda n: _lookup_cloud_tag(n, runner), lookups))
        for name, (tag, warning) in zip(lookups, results, strict=True):
            resolved[name] = tag
            if warning is not None:
                warnings.append(warning)
    return {name: resolved[name] for name in names}, warnings


@dataclass
class PriceUpdate:
    model_id: str
    catalog_name: str
    before: Cost | None
    after: Cost


@dataclass
class SyncPlan:
    """What a sync changes so that `ollama list`'s cloud stubs and the
    registry's ollama cloud entries both mirror the page."""

    catalog_size: int
    cloud_entries: int = 0  # registry ollama cloud entries before the sync
    updates: list[PriceUpdate] = field(default_factory=list)
    unchanged: list[str] = field(default_factory=list)
    additions: list[ModelEntry] = field(default_factory=list)
    # Registry ids (existing or added) whose tag `ollama list` lacks.
    pulls: list[str] = field(default_factory=list)
    # Registry ids of ollama cloud entries the page no longer lists; removed
    # (and `ollama rm`'d when pulled) whether or not they were pulled.
    removals: list[str] = field(default_factory=list)
    # Pulled cloud tags neither on the page nor in the registry.
    stray_tags: list[str] = field(default_factory=list)
    # Removed id -> the id it is re-added under: an entry an earlier sync
    # registered under a guessed tag, re-tagged to the one ollama publishes.
    replaced: dict[str, str] = field(default_factory=dict)
    # Registry ids of every page model: (re)exposed so each LiteLLM row is
    # rebuilt with current prices (wt writes prices only at expose time).
    routes: list[str] = field(default_factory=list)
    warnings: list[str] = field(default_factory=list)

    def mass_removal(self) -> bool:
        """More than half the registry's ollama cloud entries would go —
        more likely a page that parsed wrong than a real catalog change.
        Re-tagged entries come straight back, so they don't count."""
        gone = [mid for mid in self.removals if mid not in self.replaced]
        return len(gone) * 2 > self.cloud_entries

    def removal_digest(self) -> str | None:
        """Fingerprint of everything the plan deletes (registry removals and
        stray `ollama rm`s), or None if it deletes nothing. `sync --yes`
        applies deletions only under the digest the user approved, so a
        page that changed since the dry run can't delete unreviewed models."""
        items = sorted(self.removals) + sorted(f"rm {t}" for t in self.stray_tags)
        if not items:
            return None
        return hashlib.sha256("\n".join(items).encode()).hexdigest()[:12]


def _is_ollama_cloud(m: ModelEntry) -> bool:
    return m.provider_id == _OLLAMA and (m.location == "cloud" or is_cloud_tag(m.model_name))


def _stem(tag: str) -> str:
    return tag.split(":", 1)[0]


def _find_entry(registry: Registry, name: str, tag: str | None) -> ModelEntry | None:
    """The registry entry for page model ``name`` whose cloud tag is ``tag``
    (None: unknown). An entry already under the tag wins over one carrying
    the catalog_name, so an entry an earlier sync added under a guessed tag
    can't shadow it. With the tag unknown, fall back to the guessed tag,
    then to the only ollama cloud entry with the same name stem."""
    ollama = [m for m in registry.models if m.provider_id == _OLLAMA]
    if tag is not None and (hit := next((m for m in ollama if m.model_name == tag), None)):
        return hit
    if hit := next((m for m in ollama if m.extra.get(CATALOG_NAME_KEY) == name), None):
        return hit
    if tag is not None:
        return None
    guess = cloud_tag(name)
    if hit := next((m for m in ollama if m.model_name == guess), None):
        return hit
    kin = [m for m in ollama if _is_ollama_cloud(m) and _stem(m.model_name) == _stem(name)]
    return kin[0] if len(kin) == 1 else None


def _merged(page: PriceTriple, old: Any) -> PriceTriple:
    """``page`` with each unrecognized field replaced by ``old``'s value
    (``old`` is a Cost or TimePrice, or None when there is nothing to keep)."""

    def pick(key: str, attr: str) -> float | None:
        if key in page.unknown:
            kept: float | None = getattr(old, attr, None)
            return kept
        fresh: float | None = getattr(page, key)
        return fresh

    return PriceTriple(
        pick("input", "input_price_per_million"),
        pick("cache", "cache_price_per_million"),
        pick("output", "output_price_per_million"),
    )


def _with_catalog_prices(existing: Cost | None, cm: CatalogModel) -> Cost:
    base = existing if existing is not None else Cost()
    old_offpeak = next((tp for tp in base.time_prices if tp.label == OFFPEAK_LABEL), None)
    # Replace the off-peak row in place: time_prices resolve first-match-wins,
    # so moving it would change which row wins an overlapping window.
    rows = [tp for tp in base.time_prices if tp.label != OFFPEAK_LABEL]
    if cm.offpeak is not None:
        new_offpeak = offpeak_time_price(_merged(cm.offpeak, old_offpeak))
        at = base.time_prices.index(old_offpeak) if old_offpeak is not None else len(rows)
        rows.insert(at, new_offpeak)
    prices = _merged(cm.prices, existing)
    return replace(
        base,
        input_price_per_million=prices.input,
        cache_price_per_million=prices.cache,
        output_price_per_million=prices.output,
        time_prices=rows,
        extra=dict(base.extra),
    )


def _family_for(registry: Registry, name: str) -> str:
    stem = name.split(":", 1)[0]
    for m in registry.models:
        if m.provider_id == _OLLAMA and m.model_name.split(":", 1)[0] == stem:
            return m.family
    return stem


def _shared_subscription(
    registry: Registry, warnings: list[str]
) -> tuple[float | None, str | None]:
    subs = {
        (m.cost.subscription_price, m.cost.subscription_period)
        if m.cost is not None
        else (None, None)
        for m in registry.models
        if _is_ollama_cloud(m)
    }
    if len(subs) == 1:
        return next(iter(subs))
    if len(subs) > 1:
        warnings.append(
            "ollama cloud entries disagree on subscription pricing; new entries get none"
        )
    return (None, None)


def plan_sync(
    registry: Registry,
    catalog: Catalog,
    ollama_tags: list[str],
    resolved: dict[str, str | None] | None = None,
) -> SyncPlan:
    """Pure: what a sync would change. ``ollama_tags`` is `ollama list`'s
    NAME column. ``resolved`` is resolve_cloud_tags()' map; None means
    "trust cloud_tag()" (no verified tags, so entries are never re-tagged).
    A None tag means the model's cloud tag is unknown: an existing entry
    still gets its prices, nothing is added or pulled for it, and nothing
    that may be it (entries or pulled cloud tags with its name) is removed."""
    plan = SyncPlan(
        catalog_size=len(catalog.models),
        cloud_entries=sum(1 for m in registry.models if _is_ollama_cloud(m)),
        warnings=list(catalog.warnings),
    )
    pulled = set(ollama_tags)
    matched: set[str] = set()
    now_listed_tags: set[str] = set()
    sub_price, sub_period = _shared_subscription(registry, plan.warnings)
    ids = {m.id for m in registry.models}

    for cm in catalog.models:
        tag = cloud_tag(cm.name) if resolved is None else resolved.get(cm.name)
        entry = _find_entry(registry, cm.name, tag)
        retagged = None
        if resolved is not None and tag is not None and entry and entry.model_name != tag:
            # Its tag isn't what ollama publishes (an earlier sync guessed):
            # left unmatched, so it is removed and re-added under the real
            # tag below.
            retagged, entry = entry, None
        if tag is None:
            # Still on the page, just unresolved: protect whatever may be it.
            stem = _stem(cm.name)
            matched.update(
                m.id for m in registry.models if _is_ollama_cloud(m) and _stem(m.model_name) == stem
            )
            now_listed_tags.add(cloud_tag(cm.name))
            now_listed_tags.update(t for t in ollama_tags if is_cloud_tag(t) and _stem(t) == stem)
        if entry is not None:
            matched.add(entry.id)
            plan.routes.append(entry.id)
            # Both: the entry's tag and the canonical pulled tag are listed.
            now_listed_tags.update(t for t in (entry.model_name, tag) if t is not None)
            after = _with_catalog_prices(entry.cost, cm)
            before_d = _cost_to_dict(entry.cost) if entry.cost is not None else None
            if before_d == _cost_to_dict(after) and entry.extra.get(CATALOG_NAME_KEY) == cm.name:
                plan.unchanged.append(entry.id)
            else:
                plan.updates.append(PriceUpdate(entry.id, cm.name, entry.cost, after))
            if tag is not None and entry.model_name not in pulled:
                plan.pulls.append(entry.id)
            continue
        if tag is None:
            continue  # unknown cloud tag; resolve_cloud_tags() warned
        now_listed_tags.add(tag)
        new_id = f"{_OLLAMA}/{tag}"
        if new_id in ids:
            owner = next((m for m in registry.models if m.id == new_id), None)
            if owner is None:
                where = "as an earlier addition from this page"
            elif owner.provider_id == _OLLAMA:
                where = f"with model_name {owner.model_name!r}"
            else:
                where = "on another provider"
            plan.warnings.append(f"{new_id} already exists {where}; not adding")
            continue
        ids.add(new_id)
        if retagged is not None:
            plan.replaced[retagged.id] = new_id
        plan.routes.append(new_id)
        # A re-tag is the same model under the tag ollama publishes, so build
        # the new entry off the one it replaces: the hand-set extras, the
        # `ollama show` model_info and the subscription survive the id change.
        # (The LiteLLM route name IS the id, so that one cannot.)
        cost = _with_catalog_prices(
            retagged.cost
            if retagged is not None and retagged.cost is not None
            else Cost(subscription_price=sub_price, subscription_period=sub_period),
            cm,
        )
        plan.additions.append(
            ModelEntry(
                id=new_id,
                family=retagged.family if retagged is not None else _family_for(registry, cm.name),
                provider_id=_OLLAMA,
                model_name=tag,
                location="cloud",
                source="curated",
                cost=cost,
                model_info=dict(retagged.model_info) if retagged is not None else {},
                extra={
                    **(retagged.extra if retagged is not None else {}),
                    CATALOG_NAME_KEY: cm.name,
                },
            )
        )
        if tag not in pulled:
            plan.pulls.append(new_id)

    registered = {m.model_name for m in registry.models if m.provider_id == _OLLAMA}
    plan.removals = [m.id for m in registry.models if _is_ollama_cloud(m) and m.id not in matched]
    plan.stray_tags = [
        t
        for t in ollama_tags
        if is_cloud_tag(t) and t not in now_listed_tags and t not in registered
    ]
    return plan


def apply_sync(registry: Registry, plan: SyncPlan) -> None:
    """Apply ``plan`` (computed against this same registry) in place."""
    now = datetime.now(UTC).replace(microsecond=0).isoformat()
    for update in plan.updates:
        entry = registry.model(update.model_id)
        entry.cost = update.after
        entry.extra[CATALOG_NAME_KEY] = update.catalog_name
        entry.pricing_updated_at = now
    for addition in plan.additions:
        addition.pricing_updated_at = now
        registry.models.append(addition)


def _fmt(cost: Cost | None) -> str:
    if cost is None:
        return "no cost"

    def p(v: float | None) -> str:
        return "-" if v is None else f"{v:g}"

    text = (
        f"{p(cost.input_price_per_million)}/{p(cost.cache_price_per_million)}/"
        f"{p(cost.output_price_per_million)}"
    )
    offpeak = next((tp for tp in cost.time_prices if tp.label == OFFPEAK_LABEL), None)
    if offpeak is not None:
        text += (
            f" (off-peak {p(offpeak.input_price_per_million)}/"
            f"{p(offpeak.cache_price_per_million)}/{p(offpeak.output_price_per_million)})"
        )
    return text


def format_plan(plan: SyncPlan, exposed: set[str] | frozenset[str] = frozenset()) -> str:
    """``exposed``: registry ids exposed through LiteLLM (state, not
    registry), so removals that will also be unexposed say so."""
    lines = [
        f"ollama.com/pricing: {plan.catalog_size} models "
        "(prices are input/cached/output per million tokens)"
    ]
    lines.append(f"Price updates ({len(plan.updates)}):")
    lines += [f"  {u.model_id}: {_fmt(u.before)} -> {_fmt(u.after)}" for u in plan.updates]
    lines.append(f"Registry additions ({len(plan.additions)}):")
    lines += [f"  {e.id} [family {e.family}]: {_fmt(e.cost)}" for e in plan.additions]
    lines.append(f"Unchanged prices: {len(plan.unchanged)}")
    lines.append(f"ollama pull ({len(plan.pulls)}):")
    lines += [f"  {mid}" for mid in plan.pulls]
    lines.append(
        f"Registry removals — off ollama.com/pricing or under a tag ollama doesn't publish; "
        f"`ollama rm` if pulled ({len(plan.removals)}):"
    )
    lines += [
        f"  {mid}"
        + (f" (re-tagged as {plan.replaced[mid]})" if mid in plan.replaced else "")
        + (" (exposed — will be unexposed)" if mid in exposed else "")
        for mid in plan.removals
    ]
    lines.append(f"ollama rm — pulled, unregistered, off the page ({len(plan.stray_tags)}):")
    lines += [f"  {tag}" for tag in plan.stray_tags]
    new_routes = [mid for mid in plan.routes if mid not in exposed]
    lines.append(
        f"LiteLLM routes — expose or refresh prices ({len(plan.routes)}; "
        f"proxy restarts only if config.yaml changes). Not yet exposed ({len(new_routes)}):"
    )
    lines += [f"  {mid}" for mid in new_routes]
    if (digest := plan.removal_digest()) is not None:
        lines.append(
            f"Removal digest: {digest} (apply non-interactively with "
            f"`--yes --approve-removals {digest}`)"
        )
    lines += [f"warning: {w}" for w in plan.warnings]
    return "\n".join(lines)


def _default_ollama_runner(args: list[str], **kwargs: Any) -> subprocess.CompletedProcess[str]:
    return subprocess.run(args, **kwargs)  # noqa: S603 — fixed argv


def list_ollama_tags(runner: Any = None) -> list[str] | None:
    """`ollama list` NAME column, or None if it can't be read."""
    try:
        r = (runner or _default_ollama_runner)(
            ["ollama", "list"], capture_output=True, text=True, timeout=30
        )
    except (OSError, subprocess.TimeoutExpired):
        return None
    if r.returncode != 0:
        return None
    tags = []
    for line in r.stdout.splitlines():
        line = line.strip()
        if line and not line.startswith("NAME"):
            tags.append(line.split()[0])
    return tags


def remove_ollama_tag(tag: str, runner: Any = None) -> None:
    """`ollama rm`, treating "not found" as already done — the same reading
    `OllamaProvider.delete` applies, via the same test. A tag that vanished
    between `ollama list` and here (a concurrent `ollama rm`, the TUI's
    delete, ollama pruning a retired stub) is gone, not a failure."""
    r = (runner or _default_ollama_runner)(
        ["ollama", "rm", tag], capture_output=True, text=True, timeout=60
    )
    if r.returncode != 0 and not _says_not_found(r.stderr):
        raise RuntimeError(f"`ollama rm {tag}` failed (exit {r.returncode}): {r.stderr.strip()}")
