"""The answer-shape metrics: pure line counters over the model's answer.

Lines inside a fenced code block are code, not markdown: a `# comment` in a
bash block is not a heading and a `| pipe` in shell is not a table. So the
counters skip fenced lines, and a code fence counts once per block."""

import pytest

from llmbench.probe.thinking import (
    AnswerShape,
    answer_shape,
    is_bullet,
    is_fence,
    is_heading,
    is_numbered,
    is_table_line,
)


@pytest.mark.parametrize(
    ("line", "expected"),
    [
        ("# Title", True),
        ("###### Six", True),
        ("##\tTab", True),
        ("####### Seven is not a heading", False),
        ("#nospace", False),
        ("  # indented", False),
        ("text # not at start", False),
    ],
)
def test_is_heading(line, expected):
    assert is_heading(line) is expected


@pytest.mark.parametrize(
    ("line", "expected"),
    [
        ("- dash", True),
        ("* star", True),
        ("+ plus", True),
        ("  - nested", True),
        ("\t\t* nested deeper", True),
        ("-nospace", False),
        ("---", False),
        ("* * *", False),
        ("- - -", False),
        ("  _ _ _", False),
        ("*  *  *  *", False),
        ("- -", True),
        ("**bold** text", False),
        ("1. numbered is not a bullet", False),
    ],
)
def test_is_bullet(line, expected):
    assert is_bullet(line) is expected


@pytest.mark.parametrize(
    ("line", "expected"),
    [
        ("1. first", True),
        ("  10) nested tenth", True),
        ("1.5 apples", False),
        ("2024 was a year", False),
        ("- 1. a bullet first", False),
    ],
)
def test_is_numbered(line, expected):
    assert is_numbered(line) is expected


@pytest.mark.parametrize(
    ("line", "expected"),
    [
        ("| a | b |", True),
        ("|---|---|", True),
        ("   | indented row |", True),
        ("a | b", False),
        ("no pipes", False),
    ],
)
def test_is_table_line(line, expected):
    assert is_table_line(line) is expected


@pytest.mark.parametrize(
    ("line", "expected"),
    [
        ("```", True),
        ("```python", True),
        ("  ~~~", True),
        ("``inline``", False),
        ("text ```", False),
    ],
)
def test_is_fence(line, expected):
    assert is_fence(line) is expected


def test_answer_shape_counts_each_kind():
    text = "\n".join(
        [
            "# Plan",
            "## Step one",
            "- a",
            "  - nested a",
            "* b",
            "1. first",
            "2) second",
            "| x | y |",
            "|---|---|",
            "| 1 | 2 |",
            "plain prose",
        ]
    )
    assert answer_shape(text) == AnswerShape(
        headings=2, bullets=3, numbered=2, table_lines=3, code_fences=0
    )


def test_lines_inside_a_code_fence_are_not_counted():
    text = "\n".join(
        [
            "# Real heading",
            "```bash",
            "# a shell comment, not a heading",
            "ls | grep x",
            "| starts with a pipe",
            "- not a bullet",
            "1. not numbered",
            "```",
            "- real bullet",
        ]
    )
    assert answer_shape(text) == AnswerShape(
        headings=1, bullets=1, numbered=0, table_lines=0, code_fences=1
    )


def test_a_fence_closes_only_on_its_own_character():
    """A ~~~ line inside a ``` block is code; the block runs to the next ```."""
    text = "\n".join(["```", "~~~", "# still code", "```", "# heading"])
    assert answer_shape(text) == AnswerShape(
        headings=1, bullets=0, numbered=0, table_lines=0, code_fences=1
    )


def test_a_fence_line_with_an_info_string_does_not_close_a_block():
    """CommonMark: a closing fence carries no info string, so ```python inside
    an open ``` block is code, and the block stays open."""
    assert answer_shape("```\ncode\n```python\n# h\n- b\n") == AnswerShape(
        headings=0, bullets=0, numbered=0, table_lines=0, code_fences=1
    )


def test_a_shorter_fence_does_not_close_a_longer_one():
    """A block opened with four backticks is closed by four or more, not three."""
    text = "\n".join(["````", "```", "# inner", "```", "````", "# outer"])
    assert answer_shape(text) == AnswerShape(
        headings=1, bullets=0, numbered=0, table_lines=0, code_fences=1
    )


def test_a_longer_bare_fence_closes_a_block():
    text = "\n".join(["```", "# code", "   `````  ", "# heading"])
    assert answer_shape(text) == AnswerShape(
        headings=1, bullets=0, numbered=0, table_lines=0, code_fences=1
    )


def test_an_unclosed_fence_counts_once_and_swallows_the_rest():
    text = "\n".join(["- before", "```", "- inside", "# inside"])
    assert answer_shape(text) == AnswerShape(
        headings=0, bullets=1, numbered=0, table_lines=0, code_fences=1
    )


def test_two_blocks_count_two_fences():
    text = "```\na\n```\n\n~~~\nb\n~~~\n"
    assert answer_shape(text).code_fences == 2


def test_empty_answer_has_zero_shape():
    assert answer_shape("") == AnswerShape(0, 0, 0, 0, 0)
