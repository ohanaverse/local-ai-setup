from datetime import UTC, datetime, timedelta, timezone
from pathlib import Path

from modeltiers import snapshots

T0 = datetime(2026, 10, 12, 15, 58, 0, tzinfo=UTC)


def _save_many(cache: Path, count: int, *, source: str = "arena") -> list[snapshots.Snapshot]:
    """`count` snapshots one minute apart, oldest first."""
    return [
        snapshots.save(cache, source, "json", b"{}", T0 + timedelta(minutes=i))
        for i in range(count)
    ]


def test_save_writes_the_bytes_under_the_source_and_the_utc_stamp(tmp_path: Path) -> None:
    snap = snapshots.save(tmp_path, "arena", "json", b"abc", T0)
    assert snap.path == tmp_path / "arena" / "20261012T155800Z.json"
    assert snap.path.read_bytes() == b"abc"
    assert snap.fetched_at == T0
    assert snap.bad is False


def test_the_stamp_is_utc_whatever_zone_now_is_in(tmp_path: Path) -> None:
    local = T0.astimezone(timezone(timedelta(hours=-7)))
    snap = snapshots.save(tmp_path, "epoch", "zip", b"", local)
    assert snap.path.name == "20261012T155800Z.zip"


def test_mark_bad_renames_and_keeps_the_bytes(tmp_path: Path) -> None:
    snap = snapshots.save(tmp_path, "arena", "json", b"abc", T0)
    bad = snapshots.mark_bad(snap)
    assert bad.path == tmp_path / "arena" / "20261012T155800Z.bad.json"
    assert bad.path.read_bytes() == b"abc"
    assert bad.bad is True
    assert bad.fetched_at == T0
    assert not snap.path.exists()


def test_list_is_newest_first_and_reads_bad_from_the_name(tmp_path: Path) -> None:
    first, second, third = _save_many(tmp_path, 3)
    snapshots.mark_bad(second)
    listed = snapshots.list_snapshots(tmp_path, "arena")
    assert [s.path.name for s in listed] == [
        "20261012T160000Z.json",
        "20261012T155900Z.bad.json",
        "20261012T155800Z.json",
    ]
    assert [s.bad for s in listed] == [False, True, False]


def test_good_snapshots_never_include_a_bad_one(tmp_path: Path) -> None:
    first, second = _save_many(tmp_path, 2)
    snapshots.mark_bad(second)
    assert [s.path for s in snapshots.good_snapshots(tmp_path, "arena")] == [first.path]


def test_a_source_with_no_directory_has_no_snapshots(tmp_path: Path) -> None:
    assert snapshots.list_snapshots(tmp_path, "arena") == []
    assert snapshots.good_snapshots(tmp_path, "arena") == []


def test_files_that_are_not_snapshots_are_ignored(tmp_path: Path) -> None:
    snap = snapshots.save(tmp_path, "arena", "json", b"{}", T0)
    (tmp_path / "arena" / "notes.txt").write_text("x")
    (tmp_path / "arena" / ".DS_Store").write_text("x")
    (tmp_path / "arena" / "20261399T999999Z.json").write_text("x")  # not a date
    assert snapshots.list_snapshots(tmp_path, "arena") == [snap]


def test_each_source_has_its_own_directory(tmp_path: Path) -> None:
    snapshots.save(tmp_path, "arena", "json", b"{}", T0)
    assert snapshots.list_snapshots(tmp_path, "epoch") == []


def test_prune_keeps_four_good_and_two_bad(tmp_path: Path) -> None:
    saved = _save_many(tmp_path, 10)
    for snap in saved[:4]:  # the four oldest become bad
        snapshots.mark_bad(snap)
    removed = snapshots.prune(tmp_path, "arena")
    left = snapshots.list_snapshots(tmp_path, "arena")
    assert [s.path.name for s in left if not s.bad] == [
        "20261012T160700Z.json",
        "20261012T160600Z.json",
        "20261012T160500Z.json",
        "20261012T160400Z.json",
    ]
    assert [s.path.name for s in left if s.bad] == [
        "20261012T160100Z.bad.json",
        "20261012T160000Z.bad.json",
    ]
    assert sorted(p.name for p in removed) == [
        "20261012T155800Z.bad.json",
        "20261012T155900Z.bad.json",
        "20261012T160200Z.json",
        "20261012T160300Z.json",
    ]


def test_prune_touches_one_source_only(tmp_path: Path) -> None:
    _save_many(tmp_path, 6, source="arena")
    _save_many(tmp_path, 6, source="epoch")
    snapshots.prune(tmp_path, "arena")
    assert len(snapshots.list_snapshots(tmp_path, "arena")) == 4
    assert len(snapshots.list_snapshots(tmp_path, "epoch")) == 6


def test_prune_with_nothing_to_remove_removes_nothing(tmp_path: Path) -> None:
    _save_many(tmp_path, 2)
    assert snapshots.prune(tmp_path, "arena") == []
    assert snapshots.prune(tmp_path, "epoch") == []
