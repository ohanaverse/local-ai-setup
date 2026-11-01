"""Run `evalplus.evaluate` with a raised generation cap.

EvalPlus 0.3.1 hardcodes `max_new_tokens=768` in `DecoderBase` and exposes no
flag for it. A thinking model spends that budget on its reasoning and returns
an empty answer (scored 0 pass@1), or `content: null` (which crashes EvalPlus's
sanitizer). This module is the EvalPlus CLI with two patches applied first:

1. `DecoderBase.max_new_tokens` is the value of `LLMBENCH_EVALPLUS_MAX_NEW_TOKENS`.
2. A reply whose `content` is null becomes an empty string, which sanitizes to
   empty code and fails that one problem instead of the whole run.
3. EvalPlus's 100 s `SIGALRM` around each request is disabled. A reply of
   thousands of tokens takes longer than that on a local model; the alarm would
   abort the request, EvalPlus would resend it forever (queueing behind itself
   on a one-request-at-a-time server), and the cap raise would never help. The
   `[coding] timeout_s` limit on the whole subprocess still bounds the run.

Run as `python -m llmbench.benchmark.eval.evalplus_launcher <evalplus args>`;
`evalplus_runner` does so only when a suite sets `[coding] max_new_tokens`.
"""

from __future__ import annotations

import functools
import os
import signal
import types

ENV = "LLMBENCH_EVALPLUS_MAX_NEW_TOKENS"


def apply_patches(max_new_tokens: int) -> None:
    from evalplus.gen.util import openai_request
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

    # openai_request only uses signal.signal/SIGALRM/alarm; keep the first two
    # and make every alarm(n) a cancel.
    openai_request.signal = types.SimpleNamespace(  # type: ignore[attr-defined]
        SIGALRM=signal.SIGALRM,
        signal=signal.signal,
        alarm=lambda _seconds: signal.alarm(0),
    )


def main() -> None:
    raw = os.environ.get(ENV)
    if raw is None:
        raise SystemExit(
            f"{ENV} is not set; evalplus_runner sets it to the [coding] max_new_tokens"
        )
    apply_patches(int(raw))
    from evalplus.evaluate import main as evaluate_main

    evaluate_main()


if __name__ == "__main__":
    main()
