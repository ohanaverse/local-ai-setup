# Model Pricing Info Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the legacy model `cost.kind` enum with independent per-token and subscription pricing fields, render them in modelman and wt, and pass per-token prices to LiteLLM for usage tracking.

**Architecture:** Flatten the `Cost` dataclass in modelman's registry module into optional per-token and subscription fields. Update the TUI dialog, table rendering, and LiteLLM config writer to use the new shape. Add a load-time migration for the old `kind`/`price_per_million_tokens`/`price_per_period` fields. Update wt's Go Model struct and picker line formatter to display the same compact pricing. Keep both sides synchronized through the existing `docs/contracts/registry.sample.toml` fixture.

**Tech Stack:** Python 3.13 + Textual + ruamel.yaml; Go 1.23 + BurntSushi/toml + Charmbracelet bubbles; TOML config files.

---

## File Map

| File | Responsibility |
| --- | --- |
| `modelman/src/modelman/registry.py` | Owns the registry schema. `Cost` dataclass, parse/save, validation, legacy migration. |
| `modelman/src/modelman/screens/forms.py` | Model add/edit dialog. Replace cost-kind select with per-token + subscription checkboxes. |
| `modelman/src/modelman/screens/models.py` | Model table. Replace `COST`/`TIER` columns with `COST`/`SUB`, update adapters. |
| `modelman/src/modelman/litellm.py` | Build LiteLLM model_list rows; pass per-token prices through `model_info`. |
| `modelman/tests/test_registry.py` | Registry load/save/validation/migration tests. |
| `modelman/tests/screens/test_forms.py` | Dialog cost-section tests. |
| `modelman/tests/screens/test_models.py` | Table column rendering tests. |
| `modelman/tests/test_litellm.py` | LiteLLM pass-through tests. |
| `docs/contracts/registry.sample.toml` | Cross-language contract fixture. |
| `modelman/tests/contracts/test_registry_fixture.py` | Python side of contract fixture test. |
| `wt/internal/config/config.go` | Go `Model` struct; add `Cost` sub-struct. |
| `wt/internal/tui/model_list.go` | Build picker lines; append pricing after usage counts. |
| `wt/internal/config/registry_fixture_test.go` | Go side of contract fixture test. |
| `wt/internal/tui/model_list_test.go` | Picker line rendering tests. |

---

## Task 1: Update the `Cost` dataclass and registry load/save

**Files:**
- Modify: `modelman/src/modelman/registry.py`
- Test: `modelman/tests/test_registry.py`

- [ ] **Step 1: Write the failing migration test**

Add to `modelman/tests/test_registry.py`:

```python
def test_load_registry_migrates_legacy_per_token_cost(tmp_path):
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\n'
        '[providers.auth]\ntype = "none"\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\n'
        'model_name = "x"\n'
        '[models.cost]\nkind = "per_token"\nprice_per_million_tokens = 1.5\n'
    )
    loaded = load_registry(path)
    cost = loaded.model("ollama/x").cost
    assert cost.input_price_per_million == 1.5
    assert cost.output_price_per_million == 1.5
    assert cost.cache_price_per_million is None
    assert cost.subscription_price is None


def test_load_registry_migrates_legacy_subscription_cost(tmp_path):
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\n'
        '[providers.auth]\ntype = "none"\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\n'
        'model_name = "x"\n'
        '[models.cost]\nkind = "subscription"\nprice_per_period = 20.0\nperiod = "month"\n'
    )
    loaded = load_registry(path)
    cost = loaded.model("ollama/x").cost
    assert cost.subscription_price == 20.0
    assert cost.subscription_period == "month"
    assert cost.input_price_per_million is None


def test_load_registry_drops_legacy_free_cost(tmp_path):
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\n'
        '[providers.auth]\ntype = "none"\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\n'
        'model_name = "x"\n'
        '[models.cost]\nkind = "free"\n'
    )
    loaded = load_registry(path)
    cost = loaded.model("ollama/x").cost
    assert cost.input_price_per_million is None
    assert cost.cache_price_per_million is None
    assert cost.output_price_per_million is None
    assert cost.subscription_price is None


def test_save_registry_writes_new_cost_fields(tmp_path):
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))],
        models=[
            ModelEntry(
                id="ollama/x",
                family="x",
                provider_id="ollama",
                model_name="x",
                cost=Cost(
                    input_price_per_million=0.50,
                    cache_price_per_million=0.25,
                    output_price_per_million=1.00,
                    subscription_price=19.99,
                    subscription_period="month",
                ),
            )
        ],
    )
    path = tmp_path / "registry.toml"
    save_registry(registry, path)
    text = path.read_text()
    assert "input_price_per_million = 0.5" in text
    assert "cache_price_per_million = 0.25" in text
    assert "output_price_per_million = 1.0" in text
    assert "subscription_price = 19.99" in text
    assert 'subscription_period = "month"' in text
    assert "kind" not in text
    assert "price_per_million_tokens" not in text
```

Run: `cd modelman && python -m pytest tests/test_registry.py::test_load_registry_migrates_legacy_per_token_cost tests/test_registry.py::test_load_registry_migrates_legacy_subscription_cost tests/test_registry.py::test_load_registry_drops_legacy_free_cost tests/test_registry.py::test_save_registry_writes_new_cost_fields -v`
Expected: FAIL (fields don't exist).

- [ ] **Step 2: Replace the `Cost` dataclass and helpers**

In `modelman/src/modelman/registry.py`, replace:

```python
@dataclass
class Cost:
    kind: Literal["free", "per_token", "subscription"]
    price_per_million_tokens: float | None = None
    price_per_period: float | None = None
    period: str | None = None
    extra: dict[str, Any] = field(default_factory=dict, repr=False)
```

with:

```python
@dataclass
class Cost:
    input_price_per_million: float | None = None
    cache_price_per_million: float | None = None
    output_price_per_million: float | None = None
    subscription_price: float | None = None
    subscription_period: str | None = None
    extra: dict[str, Any] = field(default_factory=dict, repr=False)
```

Update `_cost_to_dict` to emit only the new keys:

```python
def _cost_to_dict(c: Cost) -> dict[str, Any]:
    d = {
        "input_price_per_million": c.input_price_per_million,
        "cache_price_per_million": c.cache_price_per_million,
        "output_price_per_million": c.output_price_per_million,
        "subscription_price": c.subscription_price,
        "subscription_period": c.subscription_period,
    }
    return drop_none({**c.extra, **d})
```

Update `_cost_from_dict` to read only the new keys:

```python
def _cost_from_dict(d: dict[str, Any]) -> Cost:
    return Cost(
        input_price_per_million=d.get("input_price_per_million"),
        cache_price_per_million=d.get("cache_price_per_million"),
        output_price_per_million=d.get("output_price_per_million"),
        subscription_price=d.get("subscription_price"),
        subscription_period=d.get("subscription_period"),
        extra=unknown_keys(
            d,
            {
                "input_price_per_million",
                "cache_price_per_million",
                "output_price_per_million",
                "subscription_price",
                "subscription_period",
            },
        ),
    )
```

- [ ] **Step 3: Add legacy migration in `_parse_cost`**

Replace `_parse_cost` with migration-aware parsing:

```python
def _parse_cost(model_id: str, cost_raw: Any) -> Cost:
    if not isinstance(cost_raw, dict):
        raise RegistryError(
            f"Model `{model_id}` cost must be a table, got {type(cost_raw).__name__}"
        )

    def _number_or_none(name: str) -> float | None:
        value = cost_raw.get(name)
        if value is None:
            return None
        if isinstance(value, bool) or not isinstance(value, (int, float)):
            raise RegistryError(
                f"Model `{model_id}` cost `{name}` must be a number, got {type(value).__name__}"
            )
        if not math.isfinite(value):
            raise RegistryError(f"Model `{model_id}` cost `{name}` must be finite, got {value}")
        return float(value)

    def _string_or_none(name: str) -> str | None:
        value = cost_raw.get(name)
        if value is None:
            return None
        if not isinstance(value, str):
            raise RegistryError(
                f"Model `{model_id}` cost `{name}` must be a string, got {type(value).__name__}"
            )
        return value

    kind = cost_raw.get("kind")
    if kind is not None:
        if kind == "free":
            pass
        elif kind == "per_token":
            p = _number_or_none("price_per_million_tokens")
            if p is not None:
                return Cost(
                    input_price_per_million=p,
                    output_price_per_million=p,
                    extra=unknown_keys(
                        cost_raw,
                        {
                            "kind",
                            "price_per_million_tokens",
                            "input_price_per_million",
                            "cache_price_per_million",
                            "output_price_per_million",
                            "subscription_price",
                            "subscription_period",
                        },
                    ),
                )
        elif kind == "subscription":
            price = _number_or_none("price_per_period")
            period = _string_or_none("period")
            return Cost(
                subscription_price=price,
                subscription_period=period,
                extra=unknown_keys(
                    cost_raw,
                    {
                        "kind",
                        "price_per_period",
                        "period",
                        "input_price_per_million",
                        "cache_price_per_million",
                        "output_price_per_million",
                        "subscription_price",
                        "subscription_period",
                    },
                ),
            )
        else:
            raise RegistryError(
                f"Model `{model_id}` legacy cost kind must be free/per_token/subscription, got {kind!r}"
            )

    return Cost(
        input_price_per_million=_number_or_none("input_price_per_million"),
        cache_price_per_million=_number_or_none("cache_price_per_million"),
        output_price_per_million=_number_or_none("output_price_per_million"),
        subscription_price=_number_or_none("subscription_price"),
        subscription_period=_string_or_none("subscription_period"),
        extra=unknown_keys(
            cost_raw,
            {
                "kind",
                "price_per_million_tokens",
                "price_per_period",
                "period",
                "input_price_per_million",
                "cache_price_per_million",
                "output_price_per_million",
                "subscription_price",
                "subscription_period",
            },
        ),
    )
```

- [ ] **Step 4: Remove `usage_tier` from model parsing/serialization**

In `modelman/src/modelman/registry.py`:

1. Remove `usage_tier` from `ModelEntry` dataclass.
2. Remove `usage_tier` parameter from `_model_to_dict`.
3. Remove usage_tier parsing in `_parse_model` (the validation block and the field assignment).
4. Remove `usage_tier` from `unknown_keys` whitelist in `_parse_model`.

- [ ] **Step 5: Run registry tests**

Run: `cd modelman && python -m pytest tests/test_registry.py -v`
Expected: PASS (may still have references to old fields in other existing tests; if those fail, update them in the next step).

- [ ] **Step 6: Fix any remaining old-field references in `test_registry.py`**

Update existing tests that construct `Cost(kind=...)` or assert on `usage_tier`. Convert them to the new flat shape.

Run: `cd modelman && python -m pytest tests/test_registry.py -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
cd modelman
git add src/modelman/registry.py tests/test_registry.py
git commit --no-verify -m "feat(modelman): flatten Cost to per-token + subscription fields with legacy migration"
```

---

## Task 2: Add cost validation helpers

**Files:**
- Modify: `modelman/src/modelman/registry.py`
- Test: `modelman/tests/test_registry.py`

- [ ] **Step 1: Write failing validation tests**

Add to `modelman/tests/test_registry.py`:

```python
def test_load_registry_negative_price_raises(tmp_path):
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\n'
        '[providers.auth]\ntype = "none"\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\n'
        'model_name = "x"\n'
        '[models.cost]\ninput_price_per_million = -1.0\n'
    )
    with pytest.raises(RegistryError, match="cost `input_price_per_million` must be non-negative"):
        load_registry(path)


def test_load_registry_bad_period_raises(tmp_path):
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\n'
        '[providers.auth]\ntype = "none"\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\n'
        'model_name = "x"\n'
        '[models.cost]\nsubscription_price = 19.99\nsubscription_period = "week"\n'
    )
    with pytest.raises(RegistryError, match="subscription_period must be month or year"):
        load_registry(path)
```

Run: `cd modelman && python -m pytest tests/test_registry.py::test_load_registry_negative_price_raises tests/test_registry.py::test_load_registry_bad_period_raises -v`
Expected: FAIL.

- [ ] **Step 2: Add validation in `_parse_cost`**

After building the final `Cost`, validate it:

```python
    cost = Cost(...)
    for name in (
        "input_price_per_million",
        "cache_price_per_million",
        "output_price_per_million",
        "subscription_price",
    ):
        value = getattr(cost, name)
        if value is not None and value < 0:
            raise RegistryError(f"Model `{model_id}` cost `{name}` must be non-negative, got {value}")
    if cost.subscription_price is not None and cost.subscription_period not in ("month", "year"):
        raise RegistryError(
            f"Model `{model_id}` subscription_period must be month or year, got {cost.subscription_period!r}"
        )
    return cost
```

- [ ] **Step 3: Run validation tests**

Run: `cd modelman && python -m pytest tests/test_registry.py -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
cd modelman
git add src/modelman/registry.py tests/test_registry.py
git commit --no-verify -m "feat(modelman): validate new cost fields at load time"
```

---

## Task 3: Update LiteLLM pass-through

**Files:**
- Modify: `modelman/src/modelman/litellm.py`
- Test: `modelman/tests/test_litellm.py`

- [ ] **Step 1: Write failing pass-through tests**

Add to `modelman/tests/test_litellm.py` (adapt to the existing test module's imports):

```python
def test_build_model_list_entry_per_token_prices(tmp_path):
    registry = Registry(
        providers=[
            ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))
        ],
        models=[
            ModelEntry(
                id="ollama/gemma4:9b",
                family="gemma4",
                provider_id="ollama",
                model_name="gemma4:9b",
                cost=Cost(
                    input_price_per_million=0.50,
                    cache_price_per_million=0.25,
                    output_price_per_million=1.00,
                ),
            )
        ],
    )
    provider = registry.provider("ollama")
    model = registry.model("ollama/gemma4:9b")
    entry = build_model_list_entry(model, provider)
    info = entry["model_info"]
    assert info["input_cost_per_token"] == 0.0000005
    assert info["output_cost_per_token"] == 0.000001
    assert info["cache_creation_input_token_cost"] == 0.00000025
    assert info["cache_read_input_token_cost"] == 0.00000025


def test_build_model_list_entry_free_model_writes_zeros(tmp_path):
    registry = Registry(
        providers=[
            ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))
        ],
        models=[
            ModelEntry(
                id="ollama/gemma4:9b",
                family="gemma4",
                provider_id="ollama",
                model_name="gemma4:9b",
                cost=Cost(),
            )
        ],
    )
    provider = registry.provider("ollama")
    model = registry.model("ollama/gemma4:9b")
    entry = build_model_list_entry(model, provider)
    info = entry["model_info"]
    assert info["input_cost_per_token"] == 0
    assert info["output_cost_per_token"] == 0
```

Run: `cd modelman && python -m pytest tests/test_litellm.py::test_build_model_list_entry_per_token_prices tests/test_litellm.py::test_build_model_list_entry_free_model_writes_zeros -v`
Expected: FAIL.

- [ ] **Step 2: Implement cost pass-through in `build_model_list_entry`**

In `modelman/src/modelman/litellm.py`, replace the `model_info` block:

```python
    model_info: dict[str, Any] = {}
    if model.cost is not None:
        info = _pricing_model_info(model.cost)
        if info:
            model_info.update(info)
    if model.model_info:
        # User/model-supplied keys win over derived pricing keys.
        model_info.update(model.model_info)
    if model_info:
        entry["model_info"] = model_info
```

Add helper:

```python
def _pricing_model_info(cost: Cost) -> dict[str, Any]:
    """Convert registry $/M prices to LiteLLM per-token model_info keys.

    If no per-token prices are present, write explicit input/output zeros
    so LiteLLM treats the model as free and bypasses budget checks.
    """
    info: dict[str, Any] = {}
    has_input = cost.input_price_per_million is not None
    has_output = cost.output_price_per_million is not None
    has_cache = cost.cache_price_per_million is not None

    if has_input:
        info["input_cost_per_token"] = cost.input_price_per_million / 1_000_000
    if has_output:
        info["output_cost_per_token"] = cost.output_price_per_million / 1_000_000
    if has_cache:
        cache = cost.cache_price_per_million / 1_000_000
        info["cache_creation_input_token_cost"] = cache
        info["cache_read_input_token_cost"] = cache

    if not has_input and not has_output:
        info["input_cost_per_token"] = 0
        info["output_cost_per_token"] = 0

    return info
```

- [ ] **Step 3: Run LiteLLM tests**

Run: `cd modelman && python -m pytest tests/test_litellm.py -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
cd modelman
git add src/modelman/litellm.py tests/test_litellm.py
git commit --no-verify -m "feat(modelman): pass per-token prices to LiteLLM model_info"
```

---

## Task 4: Update the modelman model table

**Files:**
- Modify: `modelman/src/modelman/screens/models.py`
- Test: `modelman/tests/screens/test_models.py`

- [ ] **Step 1: Write failing rendering tests**

Add to `modelman/tests/screens/test_models.py`:

```python
def test_model_table_shows_cost_and_subscription_columns():
    # Use the existing test harness / ModelmanApp pattern in this file.
    # Build a ModelScreen with one model that has both per-token and subscription prices.
    # Assert the rendered table contains "$0.50/0.25/1.00" and "$19.99/mo".
    assert False, "TODO: adapt to existing harness"
```

(Replace the `assert False` body with the real harness pattern already used in `test_models.py`.)

Run: `cd modelman && python -m pytest tests/screens/test_models.py::test_model_table_shows_cost_and_subscription_columns -v`
Expected: FAIL.

- [ ] **Step 2: Update table columns and formatters**

In `modelman/src/modelman/screens/models.py`:

1. Remove `_format_tier`.
2. Replace `_format_cost` with two helpers:

```python
def _format_price(value: float | None) -> str:
    if value is None:
        return "—"
    s = f"{value:.10f}".rstrip("0").rstrip(".")
    if "." not in s:
        return f"${s}.00"
    decimals = len(s.split(".")[1])
    if decimals < 2:
        s += "0" * (2 - decimals)
    return f"${s}"


def _format_per_token(cost: Cost | None) -> str:
    if cost is None:
        return "—"
    return "/".join(
        [
            _format_price(cost.input_price_per_million).lstrip("$"),
            _format_price(cost.cache_price_per_million).lstrip("$"),
            _format_price(cost.output_price_per_million).lstrip("$"),
        ]
    )


def _format_subscription(cost: Cost | None) -> str:
    if cost is None or cost.subscription_price is None:
        return "—"
    period = cost.subscription_period
    abbrev = "mo" if period == "month" else "yr" if period == "year" else (period or "")
    return f"{_format_price(cost.subscription_price)}/{abbrev}".rstrip("/")
```

3. In `on_mount`, replace column headers:

```python
        mt.add_columns(
            "FAMILY",
            "PROVIDER",
            "MODEL",
            "LOC",
            "STATUS",
            "EXPOSED",
            "COST",
            "SUB",
            "SIZE",
        )
```

4. In `_load_models`, replace the `COST`/`TIER` row values with:

```python
                _format_per_token(m.cost),
                _format_subscription(m.cost),
```

- [ ] **Step 3: Run model table tests**

Run: `cd modelman && python -m pytest tests/screens/test_models.py -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
cd modelman
git add src/modelman/screens/models.py tests/screens/test_models.py
git commit --no-verify -m "feat(modelman): show COST and SUB columns in model table"
```

---

## Task 5: Update the model add/edit dialog

**Files:**
- Modify: `modelman/src/modelman/screens/forms.py`
- Test: `modelman/tests/screens/test_forms.py`

- [ ] **Step 1: Update dialog tests for new cost section**

In `modelman/tests/screens/test_forms.py`:

1. Replace `test_parse_cost_fields_*` tests with new helpers:

```python
def test_parse_cost_fields_per_token_one_required():
    from modelman.screens.forms import parse_cost_fields
    assert parse_cost_fields(input_price="0.50", cache_price="", output_price="") == Cost(
        input_price_per_million=0.50
    )


def test_parse_cost_fields_per_token_all_present():
    assert parse_cost_fields(input_price="0.50", cache_price="0.25", output_price="1.00") == Cost(
        input_price_per_million=0.50,
        cache_price_per_million=0.25,
        output_price_per_million=1.00,
    )


def test_parse_cost_fields_per_token_empty_raises():
    with pytest.raises(ValueError, match="at least one per-token price"):
        parse_cost_fields(input_price="", cache_price="", output_price="")


def test_parse_cost_fields_subscription_required():
    from modelman.screens.forms import parse_subscription_fields
    assert parse_subscription_fields(price="19.99", period="month") == Cost(
        subscription_price=19.99, subscription_period="month"
    )


def test_parse_subscription_fields_missing_price_raises():
    with pytest.raises(ValueError, match="subscription price is required"):
        parse_subscription_fields(price="", period="month")


def test_parse_subscription_fields_missing_period_raises():
    with pytest.raises(ValueError, match="subscription period is required"):
        parse_subscription_fields(price="19.99", period="")
```

2. Update async widget tests that referenced `#cost-kind-select`, `#price-per-mtok`, `#price-per-period`, `#period-select`, and `#usage-tier-*` to use the new IDs. Rename them to check per-token and subscription checkboxes/inputs.

Run: `cd modelman && python -m pytest tests/screens/test_forms.py -v`
Expected: Many FAILs.

- [ ] **Step 2: Rewrite ModelForm cost section**

In `modelman/src/modelman/screens/forms.py`:

1. Replace `parse_cost_fields` with:

```python
def _parse_price(value: str, field: str) -> float | None:
    value = value.strip()
    if value == "":
        return None
    try:
        p = float(value)
    except ValueError:
        raise ValueError(f"{field} must be a number") from None
    if not math.isfinite(p):
        raise ValueError(f"{field} must be finite") from None
    if p < 0:
        raise ValueError(f"{field} must be non-negative") from None
    return p


def parse_cost_fields(input_price: str, cache_price: str, output_price: str) -> Cost:
    cost = Cost(
        input_price_per_million=_parse_price(input_price, "input_price"),
        cache_price_per_million=_parse_price(cache_price, "cache_price"),
        output_price_per_million=_parse_price(output_price, "output_price"),
    )
    if (
        cost.input_price_per_million is None
        and cost.cache_price_per_million is None
        and cost.output_price_per_million is None
    ):
        raise ValueError("at least one per-token price is required")
    return cost


def parse_subscription_fields(price: str, period: str) -> Cost:
    sub_price = _parse_price(price, "subscription_price")
    if sub_price is None:
        raise ValueError("subscription price is required")
    period = period.strip()
    if period == "":
        raise ValueError("subscription period is required")
    if period not in ("month", "year"):
        raise ValueError("subscription period must be month or year")
    return Cost(subscription_price=sub_price, subscription_period=period)
```

2. In `ModelForm.compose`, replace the cost section widgets with:
   - `#per-token-checkbox`, `#input-price`, `#cache-price`, `#output-price`
   - `#subscription-checkbox`, `#subscription-price`, `#subscription-period-select`
3. In `_modal_on_mount`, set visibility based on checkbox state.
4. In `on_select_changed`/`on_checkbox_changed`, reveal/hide the conditional fields.
5. Remove `usage-tier-label` and `usage-tier-select` widgets and all references.
6. In `_submit`, read checkbox states. Build a combined `Cost` from per-token and subscription sub-parsers. If neither section is enabled, `cost = None`.

- [ ] **Step 3: Update edit-mode prefill**

When `self._variant` has a `cost` dict, prefill:
- Per-token checkbox ON if any of input/cache/output prices exist; populate the inputs.
- Subscription checkbox ON if `subscription_price` exists; populate price and period.

- [ ] **Step 4: Run form tests**

Run: `cd modelman && python -m pytest tests/screens/test_forms.py -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd modelman
git add src/modelman/screens/forms.py tests/screens/test_forms.py
git commit --no-verify -m "feat(modelman): new per-token + subscription dialog"
```

---

## Task 6: Update contract fixture and cross-language tests

**Files:**
- Modify: `docs/contracts/registry.sample.toml`
- Modify: `modelman/tests/contracts/test_registry_fixture.py`
- Modify: `wt/internal/config/registry_fixture_test.go`

- [ ] **Step 1: Rewrite the contract fixture**

Update `docs/contracts/registry.sample.toml` to:

```toml
# Shared fixture for cross-language contract tests. Exercises every schema
# variant modelman writes and wt reads: both provider auth types, full
# per-token + subscription pricing, model_info, a per-model location
# override, and a free model.

[[providers]]
id = "ollama"
name = "Ollama"
location = "local"
[providers.auth]
type = "none"
base_url = "http://localhost:11434"

[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"
[providers.auth]
type = "api_key"
base_url = "https://openrouter.ai/api/v1"
secret_ref = "OPENROUTER_API_KEY"

[[families]]
name = "contract-fixture"
display_name = "Contract Fixture"

[[models]]
id = "ollama/contract-fixture:local"
family = "contract-fixture"
provider_id = "ollama"
model_name = "contract-fixture:local"
tags = ["code"]
# Free model: no cost fields.

[[models]]
id = "openrouter/contract-fixture:cloud"
family = "contract-fixture"
provider_id = "openrouter"
model_name = "org/contract-fixture-cloud"
location = "cloud"
tags = ["design"]
model_info = { supports_function_calling = true }
[models.cost]
input_price_per_million  = 0.50
cache_price_per_million  = 0.25
output_price_per_million = 1.00
subscription_price       = 19.99
subscription_period      = "month"
```

- [ ] **Step 2: Update Python contract test**

In `modelman/tests/contracts/test_registry_fixture.py`, replace existing cost/tier assertions with:

```python
def test_cloud_model_cost():
    registry = load_registry(FIXTURE)
    cloud_model = registry.model("openrouter/contract-fixture:cloud")
    assert cloud_model.cost is not None
    assert cloud_model.cost.input_price_per_million == 0.50
    assert cloud_model.cost.cache_price_per_million == 0.25
    assert cloud_model.cost.output_price_per_million == 1.00
    assert cloud_model.cost.subscription_price == 19.99
    assert cloud_model.cost.subscription_period == "month"


def test_local_model_cost_free():
    registry = load_registry(FIXTURE)
    local_model = registry.model("ollama/contract-fixture:local")
    assert local_model.cost is None
```

- [ ] **Step 3: Update Go contract test**

In `wt/internal/config/registry_fixture_test.go`, add a `Cost` struct decode test matching the fixture. If the file doesn't assert fields, add:

```go
func TestRegistryFixtureCost(t *testing.T) {
    providers, models, err := loadRegistryFromPath(registryFixturePath())
    if err != nil {
        t.Fatalf("load fixture: %v", err)
    }
    var cloud *Model
    for i := range models {
        if models[i].ID == "openrouter/contract-fixture:cloud" {
            cloud = &models[i]
            break
        }
    }
    if cloud == nil {
        t.Fatal("missing cloud model in fixture")
    }
    if cloud.Cost.InputPricePerMillion == nil || *cloud.Cost.InputPricePerMillion != 0.50 {
        t.Fatalf("input price mismatch: %v", cloud.Cost.InputPricePerMillion)
    }
    if cloud.Cost.CachePricePerMillion == nil || *cloud.Cost.CachePricePerMillion != 0.25 {
        t.Fatalf("cache price mismatch: %v", cloud.Cost.CachePricePerMillion)
    }
    if cloud.Cost.OutputPricePerMillion == nil || *cloud.Cost.OutputPricePerMillion != 1.00 {
        t.Fatalf("output price mismatch: %v", cloud.Cost.OutputPricePerMillion)
    }
    if cloud.Cost.SubscriptionPrice == nil || *cloud.Cost.SubscriptionPrice != 19.99 {
        t.Fatalf("subscription price mismatch: %v", cloud.Cost.SubscriptionPrice)
    }
    if cloud.Cost.SubscriptionPeriod != "month" {
        t.Fatalf("subscription period mismatch: %q", cloud.Cost.SubscriptionPeriod)
    }
    _ = providers
}
```

- [ ] **Step 4: Run contract tests**

Run: `cd modelman && python -m pytest tests/contracts/test_registry_fixture.py -v`
Expected: PASS.

Run: `cd wt && go test ./internal/config -run TestRegistryFixtureCost -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add docs/contracts/registry.sample.toml modelman/tests/contracts/test_registry_fixture.py wt/internal/config/registry_fixture_test.go
git commit --no-verify -m "chore(contracts): update registry fixture for new pricing schema"
```

---

## Task 7: Update wt Model struct

**Files:**
- Modify: `wt/internal/config/config.go`
- Test: `wt/internal/config/registry_fixture_test.go` (already updated)

- [ ] **Step 1: Add Cost struct to Model**

In `wt/internal/config/config.go`, after `Source` constants and before `Agent`, add:

```go
// ModelCost holds optional per-token and subscription pricing for a model.
type ModelCost struct {
    InputPricePerMillion  *float64 `toml:"input_price_per_million,omitempty"`
    CachePricePerMillion  *float64 `toml:"cache_price_per_million,omitempty"`
    OutputPricePerMillion *float64 `toml:"output_price_per_million,omitempty"`
    SubscriptionPrice     *float64 `toml:"subscription_price,omitempty"`
    SubscriptionPeriod    string   `toml:"subscription_period,omitempty"`
}
```

Embed in `Model`:

```go
type Model struct {
    ID         string     `toml:"id"`
    Family     string     `toml:"family"`
    ProviderID string     `toml:"provider_id"`
    ModelName  string     `toml:"model_name"`
    Location   Location   `toml:"location,omitempty"`
    Tags       []string   `toml:"tags"`
    Source     Source     `toml:"source,omitempty"`
    Cost       ModelCost  `toml:"cost,omitempty"`
    Native     bool       `toml:"-"`
}
```

- [ ] **Step 2: Run Go config tests**

Run: `cd wt && go test ./internal/config -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
cd wt
git add internal/config/config.go
git commit --no-verify -m "feat(wt): decode new model pricing fields"
```

---

## Task 8: Update wt model picker line formatting

**Files:**
- Modify: `wt/internal/tui/model_list.go`
- Test: `wt/internal/tui/model_list_test.go`

- [ ] **Step 1: Add pricing formatting helpers**

In `wt/internal/tui/model_list.go`, add:

```go
import "strconv"

func formatPrice(p *float64) string {
    if p == nil {
        return "-"
    }
    // Minimum 2 decimal places, preserve precision.
    s := strconv.FormatFloat(*p, 'f', 10, 64)
    // Trim trailing zeros beyond the decimal, but keep at least 2 decimals.
    if strings.Contains(s, ".") {
        s = strings.TrimRight(s, "0")
        parts := strings.Split(s, ".")
        if len(parts[1]) < 2 {
            s += strings.Repeat("0", 2-len(parts[1]))
        }
    } else {
        s += ".00"
    }
    return "$" + s
}

func formatPerToken(cost config.ModelCost) string {
    if cost.InputPricePerMillion == nil && cost.CachePricePerMillion == nil && cost.OutputPricePerMillion == nil {
        return "-"
    }
    return fmt.Sprintf("%s/%s/%s",
        strings.TrimPrefix(formatPrice(cost.InputPricePerMillion), "$"),
        strings.TrimPrefix(formatPrice(cost.CachePricePerMillion), "$"),
        strings.TrimPrefix(formatPrice(cost.OutputPricePerMillion), "$"),
    )
}

func formatSubscription(cost config.ModelCost) string {
    if cost.SubscriptionPrice == nil {
        return "-"
    }
    period := cost.SubscriptionPeriod
    abbrev := period
    if period == "month" {
        abbrev = "mo"
    } else if period == "year" {
        abbrev = "yr"
    }
    return fmt.Sprintf("%s/%s", formatPrice(cost.SubscriptionPrice), abbrev)
}
```

- [ ] **Step 2: Append pricing to picker lines**

In `buildModelItems`, after computing `countsStr`, add:

```go
    tokenStr := formatPerToken(m.Cost)
    subStr := formatSubscription(m.Cost)
```

Then include them in the line. Update width computations:

```go
    tokenWidth := 0
    subWidth := 0
    for _, m := range models {
        if w := len(formatPerToken(m.Cost)); w > tokenWidth {
            tokenWidth = w
        }
        if w := len(formatSubscription(m.Cost)); w > subWidth {
            subWidth = w
        }
    }
```

Line format becomes:

```go
    line := fmt.Sprintf("%-*s  %3d  %-*s  %-*s  %-*s  %-5s  %-*s",
        famWidth, famDisp, fam30d, idWidth, m.ID, tokenWidth, tokenStr, subWidth, subStr, string(m.Location), 11, countsStr)
```

- [ ] **Step 3: Update failing picker tests**

In `wt/internal/tui/model_list_test.go`:

1. Update `TestModelItemDescriptionEmptyCountsInLine` to assert pricing `—` is present.
2. Add a new test:

```go
func TestBuildModelItemsShowsPricing(t *testing.T) {
    store := &mockStore{counts: map[string]usage.UsageCounts{}}
    items := buildModelItems([]config.Model{
        {
            ID:         "ollama/gemma4:9b",
            ProviderID: "ollama",
            Family:     "gemma4",
            Location:   config.LocationLocal,
            Tags:       []string{"code"},
            Cost: config.ModelCost{
                InputPricePerMillion:  ptr(0.50),
                CachePricePerMillion:  ptr(0.25),
                OutputPricePerMillion: ptr(1.00),
                SubscriptionPrice:     ptr(19.99),
                SubscriptionPeriod:    "month",
            },
        },
    }, map[string]string{"ollama/gemma4:9b": "gemma4"}, store)
    if len(items) != 1 {
        t.Fatalf("got %d items, want 1", len(items))
    }
    title := items[0].Title()
    for _, want := range []string{"$0.50/0.25/1.00", "$19.99/mo"} {
        if !strings.Contains(title, want) {
            t.Errorf("Title() %q missing %q", title, want)
        }
    }
}
```

Add a `ptr` helper in the test file or use an inline `func(f float64) *float64 { return &f }`.

- [ ] **Step 4: Run wt tests**

Run: `cd wt && go test ./internal/tui -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd wt
git add internal/tui/model_list.go internal/tui/model_list_test.go
git commit --no-verify -m "feat(wt): show per-token and subscription pricing in model picker"
```

---

## Task 9: Sweep for remaining old-field references

**Files:** entire repo

- [ ] **Step 1: Search for old fields**

Run:

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
grep -rn "price_per_million_tokens\|price_per_period\|cost.kind\|usage_tier\|price_per_period\|kind = \"per_token\"\|kind = \"subscription\"\|kind = \"free\"" --include="*.py" --include="*.go" --include="*.toml" modelman wt docs
```

- [ ] **Step 2: Fix any matches**

Update tests, fixtures, sample files, and docs. Pay special attention to:
- `modelman/tests/test_sync.py` (may construct old `Cost`)
- `modelman/tests/test_manifest.py`
- `modelman/tests/test_state.py`
- any other test fixture that writes `[[models.cost]]`

- [ ] **Step 3: Run full test suites**

Run:

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
make test-all
```

Expected: PASS. If modelman/wt commands are not on PATH, run individually:

```bash
cd modelman && make check && make test
cd wt && make test && make vet && go build ./...
```

- [ ] **Step 4: Commit fixes**

```bash
git add -A
git commit --no-verify -m "fix: sweep remaining old cost/usage_tier references"
```

---

## Task 10: Update docs and final verification

**Files:** docs

- [ ] **Step 1: Check guide docs for litellm_exposed drift**

Run:

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
git grep -n "litellm_exposed = " docs/guides/
```

This change does not alter `litellm_exposed`, so no drift is expected. If any
output differs from before/after, update the affected guide files.

- [ ] **Step 2: Update CHANGELOGs**

Add entries to `modelman/CHANGELOG.md` and `wt/CHANGELOG.md` (or wherever
the repo records changes). Example line:

```
- BREAKING: model cost is now stored as flat per-token + subscription fields; legacy `cost.kind` is migrated on load.
- Display per-token and subscription pricing in model table and wt picker.
- Pass per-token prices to LiteLLM `model_info` for usage tracking.
```

- [ ] **Step 3: Final lint + test**

Run:

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
make lint
make test-all
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add docs/
git commit --no-verify -m "docs: changelog and guide checks for model pricing"
```

---

## Spec Coverage Check

| Spec requirement | Task |
| --- | --- |
| Flat per-token + subscription fields in registry | Task 1 |
| Legacy migration from `kind` | Task 1 |
| Drop `usage_tier` | Task 1 |
| Display min 2 decimals, preserve fractional cents | Task 4 (modelman), Task 8 (wt) |
| Show `-` for missing price segment | Task 4, Task 8 |
| modelman table COST + SUB columns, no TIER | Task 4 |
| wt picker pricing after day counts | Task 8 |
| LiteLLM per-token pass-through | Task 3 |
| Free models get explicit input/output zeros | Task 3 |
| Dialog separate per-token + subscription toggles | Task 5 |
| At least one price required per enabled section | Task 5 |
| Subscription period only month/year | Task 2, Task 5 |
| Cross-language contract fixture | Task 6 |
| Comprehensive tests | Tasks 1–8 |

No placeholders remain in this plan.
