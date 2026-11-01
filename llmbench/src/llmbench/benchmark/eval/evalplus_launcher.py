"""Run `evalplus.evaluate` with a raised generation cap.

EvalPlus 0.3.1 hardcodes `max_new_tokens=768` in `DecoderBase` and exposes no
flag for it. A thinking model spends that budget on its reasoning and returns
an empty answer (scored 0 pass@1), or `content: null` (which crashes EvalPlus's
sanitizer). This module is the EvalPlus CLI with two patches applied first:

1. `DecoderBase.max_new_tokens` is the value of `LLMBENCH_EVALPLUS_MAX_NEW_TOKENS`.
2. A reply whose `content` is null becomes an empty string, which sanitizes to
   empty code and fails that one problem instead of the whole run.

Run as `python -m llmbench.benchmark.eval.evalplus_launcher <evalplus args>`;
`evalplus_runner` does so only when a suite sets `[coding] max_new_tokens`.
"""

from __future__ import annotations

import functools
import os

ENV = "LLMBENCH_EVALPLUS_MAX_NEW_TOKENS"


def apply_patches(max_new_tokens: int) -> None:
    from evalplus.provider.base import DecoderBase
    from evalplus.provider.openai import OpenAIChatDecoder

    original_init = DecoderBase.__init__

    @functools.wraps(original_init)
    def init(self, *args, **kwargs):  # type: ignore[no-untyped-def]
        kwargs["max_new_tokens"] = max_new_tokens
        original_init(self, *args, **kwargs)

    DecoderBase.__init__ = init  # type: ignore[method-assign]

    original_codegen = OpenAIChatDecoder.codegen

    @functools.wraps(original_codegen)
    def codegen(self, *args, **kwargs):  # type: ignore[no-untyped-def]
        return [out if out is not None else "" for out in original_codegen(self, *args, **kwargs)]

    OpenAIChatDecoder.codegen = codegen  # type: ignore[method-assign]


def main() -> None:
    apply_patches(int(os.environ[ENV]))
    from evalplus.evaluate import main as evaluate_main

    evaluate_main()


if __name__ == "__main__":
    main()
