from pathlib import Path

import pytest
from fakes import FakeResolver

from modeltiers import cards
from modeltiers.errors import ConfigError
from modeltiers.records import ScoreRecord

VENDORS = {"anthropic": "https://docs.example/anthropic", "google": "https://docs.example/google"}


def _record(source_id: str = "v/m", hugging_face_id: str | None = None) -> ScoreRecord:
    return ScoreRecord(
        source="openrouter", source_id=source_id, score=1.0, hugging_face_id=hugging_face_id
    )


# --- which link a model gets --------------------------------------------------


def test_open_weights_link_to_their_hugging_face_card_before_anything_else() -> None:
    card = cards.card_for("anthropic/x", _record("a/x", "Org/Model-1"), VENDORS)
    assert card == cards.Card(cards.HUGGINGFACE, "https://huggingface.co/Org/Model-1")


def test_a_closed_model_links_to_its_labs_catalog_not_to_openrouter() -> None:
    card = cards.card_for("Anthropic/claude-x", _record("anthropic/claude-x"), VENDORS)
    assert card == cards.Card(cards.VENDOR, VENDORS["anthropic"])


def test_a_lab_with_no_catalog_falls_back_to_the_openrouter_page() -> None:
    card = cards.card_for("liquid/lfm:free", _record("liquid/lfm-2.5:free"), VENDORS)
    assert card == cards.Card(cards.OPENROUTER, "https://openrouter.ai/liquid/lfm-2.5:free")


def test_a_model_openrouter_never_scored_still_gets_its_labs_catalog() -> None:
    assert cards.card_for("google/gemini-x", None, VENDORS) == cards.Card(
        cards.VENDOR, VENDORS["google"]
    )


def test_a_model_with_no_record_and_no_catalog_has_no_link() -> None:
    assert cards.card_for("liquid/lfm", None, VENDORS) is None
    assert cards.card_for("noslash", None, VENDORS) is None


def test_an_empty_hugging_face_id_is_no_id() -> None:
    assert cards.card_for("liquid/lfm", _record("liquid/lfm", ""), VENDORS) == cards.Card(
        cards.OPENROUTER, "https://openrouter.ai/liquid/lfm"
    )


def test_ids_are_percent_encoded_into_the_address() -> None:
    card = cards.card_for("x/y", _record("x/y", "Org/has space?#"), {})
    assert card == cards.Card(cards.HUGGINGFACE, "https://huggingface.co/Org/has%20space%3F%23")


@pytest.mark.parametrize(
    ("url", "ok"),
    [
        ("https://docs.example/models", True),
        ("http://docs.example/models", False),
        ("javascript:alert(1)", False),
        ("https://", False),
        ("https://docs.example/a b", False),
        ("https://docs.example/\n", False),
        ("//docs.example/models", False),
        ("", False),
    ],
)
def test_only_an_https_address_with_a_host_may_be_linked(url: str, ok: bool) -> None:
    assert cards.is_https(url) is ok


def test_describe_reads_a_card_back_from_an_entry_and_refuses_a_bad_one() -> None:
    good = {"card_kind": "vendor", "card_url": "https://docs.example/m"}
    assert cards.describe(good) == cards.Card("vendor", "https://docs.example/m")
    assert cards.describe({}) is None
    assert cards.describe({**good, "card_kind": "other"}) is None
    assert cards.describe({**good, "card_url": "javascript:alert(1)"}) is None
    assert cards.describe({**good, "card_url": 7}) is None


# --- vendors.toml -------------------------------------------------------------

_ONE = 'prefix = "acme"\nurl = "https://docs.example/m"\nnote = "read"\n'


def test_vendors_parse_to_prefix_and_address() -> None:
    assert cards.parse_vendors(f"[[vendor]]\n{_ONE}") == {"acme": "https://docs.example/m"}
    assert cards.parse_vendors("") == {}


@pytest.mark.parametrize(
    ("text", "message"),
    [
        (f"[[vendor]]\n{_ONE}extra = 1\n", "unknown key 'extra'"),
        ('[[vendor]]\nprefix = "acme"\nurl = "https://d.example/m"\n', "note is missing"),
        (_ONE.replace("https", "http").join(["[[vendor]]\n", ""]), "not an https address"),
        (f"[[vendor]]\n{_ONE.replace('acme', 'Acme')}", "not a lower-case lab name"),
        (f"[[vendor]]\n{_ONE.replace('acme', 'a/b')}", "not a lower-case lab name"),
        (f"[[vendor]]\n{_ONE}[[vendor]]\n{_ONE}", "listed twice"),
        ("vendor = 3", "not an array of tables"),
    ],
)
def test_a_bad_vendors_file_is_refused(text: str, message: str) -> None:
    with pytest.raises(ConfigError, match=message):
        cards.parse_vendors(text)


def test_a_vendors_file_is_read_from_a_path(tmp_path: Path) -> None:
    path = tmp_path / "v.toml"
    path.write_text(f"[[vendor]]\n{_ONE}")
    assert cards.load_vendors(path) == {"acme": "https://docs.example/m"}


# --- following redirects ------------------------------------------------------


def test_an_address_that_moved_is_replaced_and_said_so() -> None:
    told: list[str] = []
    resolver = FakeResolver({VENDORS["anthropic"]: "https://new.example/anthropic"})
    found = cards.refresh(VENDORS, {}, resolver, told.append)
    assert found.urls == {"anthropic": "https://new.example/anthropic", "google": VENDORS["google"]}
    assert told == list(found.notes)
    assert len(told) == 1
    assert f"{VENDORS['anthropic']} moved to https://new.example/anthropic" in told[0]


def test_the_check_starts_from_the_address_an_earlier_run_kept() -> None:
    resolver = FakeResolver()
    kept = {"anthropic": "https://kept.example/a"}
    found = cards.refresh(VENDORS, kept, resolver)
    assert found.urls["anthropic"] == "https://kept.example/a"
    assert resolver.calls == ["https://kept.example/a", VENDORS["google"]]
    assert found.notes == ()


def test_a_page_that_does_not_answer_keeps_the_address_it_had() -> None:
    resolver = FakeResolver(down=frozenset({VENDORS["google"]}))
    found = cards.refresh(VENDORS, {}, resolver)
    assert found.urls["google"] == VENDORS["google"]
    assert found.answered == {"anthropic"}
    assert len(found.notes) == 1 and "could not check it" in found.notes[0]
    assert "HTTP 503" in found.notes[0]


def test_a_kept_address_that_died_gives_way_to_the_committed_one_if_that_answers() -> None:
    kept = "https://kept.example/g"
    resolver = FakeResolver({VENDORS["google"]: "https://new.example/g"}, down=frozenset({kept}))
    found = cards.refresh(VENDORS, {"google": kept}, resolver)
    assert found.urls["google"] == "https://new.example/g"
    assert resolver.calls[1:] == [kept, VENDORS["google"]]
    assert any("could not check it" in note for note in found.notes)


def test_when_nothing_answers_the_kept_address_stays() -> None:
    kept = "https://kept.example/g"
    resolver = FakeResolver(down=frozenset({kept, VENDORS["google"]}))
    found = cards.refresh(VENDORS, {"google": kept}, resolver)
    assert found.urls["google"] == kept
    assert "google" not in found.answered
    assert len(found.notes) == 2


def test_an_answer_that_is_not_https_is_not_followed() -> None:
    resolver = FakeResolver({VENDORS["google"]: "http://downgraded.example/g"})
    found = cards.refresh(VENDORS, {}, resolver)
    assert found.urls["google"] == VENDORS["google"]
    assert "not https" in found.notes[0]


def test_an_offline_run_checks_nothing_and_uses_what_was_kept() -> None:
    found = cards.refresh(VENDORS, {"google": "https://kept.example/g"}, None)
    assert found.urls == {"anthropic": VENDORS["anthropic"], "google": "https://kept.example/g"}
    assert found.notes == ()


def test_the_kept_addresses_round_trip_and_a_bad_file_reads_as_nothing(tmp_path: Path) -> None:
    assert cards.read_cache(tmp_path, VENDORS) == {}
    kept = {"anthropic": "https://moved.example/a"}
    (tmp_path / cards.CACHE_FILE).write_text(cards.cache_text(kept, VENDORS))
    assert cards.read_cache(tmp_path, VENDORS) == kept
    (tmp_path / cards.CACHE_FILE).write_text("not = [toml")
    assert cards.read_cache(tmp_path, VENDORS) == {}
    (tmp_path / cards.CACHE_FILE).write_bytes(b"\xff\xfe")
    assert cards.read_cache(tmp_path, VENDORS) == {}


def test_a_kept_address_is_dropped_when_it_is_malformed_or_not_https_or_a_lab_left(
    tmp_path: Path,
) -> None:
    google = VENDORS["google"]
    (tmp_path / cards.CACHE_FILE).write_text(
        f'[google]\nfrom = "{google}"\nurl = "https://ok.example/g"\n'
        f'[anthropic]\nfrom = "{VENDORS["anthropic"]}"\nurl = "javascript:alert(1)"\n'
        '[gone]\nfrom = "https://x.example/"\nurl = "https://ok.example/x"\n'
        "bare = 3\n"
    )
    assert cards.read_cache(tmp_path, VENDORS) == {"google": "https://ok.example/g"}


def test_a_corrected_committed_address_is_not_shadowed_by_an_old_answer(tmp_path: Path) -> None:
    old = {"google": "https://old.example/g"}
    (tmp_path / cards.CACHE_FILE).write_text(
        cards.cache_text({"google": "https://kept.example/g"}, old)
    )
    # vendors.toml now says another address: what was kept from the old one is ignored.
    assert cards.read_cache(tmp_path, VENDORS) == {}
