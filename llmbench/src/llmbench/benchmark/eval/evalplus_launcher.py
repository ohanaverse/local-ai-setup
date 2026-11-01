"""Run `evalplus.evaluate` with a raised generation cap.

EvalPlus 0.3.1 hardcodes `max_new_tokens=768` in `DecoderBase` and exposes no
flag for it. A thinking model spends that budget on its reasoning and returns
an empty answer (scored 0 pass@1), or `content: null` (which crashes EvalPlus's
sanitizer). This module is the EvalPlus CLI with four patches applied first:

1. `DecoderBase.max_new_tokens` is the value of `LLMBENCH_EVALPLUS_MAX_NEW_TOKENS`.
2. A reply whose `content` is null becomes an empty string, which sanitizes to
   empty code and fails that one problem instead of the whole run.
3. EvalPlus's `make_auto_request` is replaced. The original arms a 100 s
   `SIGALRM` around each request: a reply of thousands of tokens takes longer on
   a local model, the alarm aborts it, and EvalPlus resends it forever. It also
   retries every `openai.APIError` with no sleep and no attempt limit, so a
   server that rejects `max_tokens` with a 400 sends it into a hot loop. The
   replacement has no alarm, fails at once on a 4xx other than 429, and retries
   429, 5xx and connection errors up to `MAX_RETRIES` times, `RETRY_SLEEP_S`
   apart, then raises (the row ends with the server's message in its stderr).
   The `[coding] timeout_s` limit on the whole subprocess still bounds the run.
4. The OpenAI client's own 600 s request timeout is disabled for the same reason:
   a non-streamed reply of 16k tokens on a local model can take longer, the client
   would raise `APITimeoutError` (an `APIConnectionError`), and EvalPlus would
   resend the identical request forever.

Run as `python -m llmbench.benchmark.eval.evalplus_launcher <evalplus args>`;
`evalplus_runner` does so only when a suite sets `[coding] max_new_tokens`.
"""

from __future__ import annotations

import functools
import os
import time
from typing import Any

ENV = "LLMBENCH_EVALPLUS_MAX_NEW_TOKENS"
MAX_RETRIES = 5
RETRY_SLEEP_S = 5.0


def _bounded_request(*args: Any, **kwargs: Any) -> Any:
    """`make_auto_request` without the alarm and with a retry limit."""
    import openai
    from evalplus.gen.util import openai_request

    attempt = 0
    while True:
        try:
            return openai_request.make_request(*args, **kwargs)
        except openai.APIStatusError as e:
            # 429 and 5xx may clear on their own; any other status is a refusal
            # the same request will meet again.
            if e.status_code != 429 and e.status_code < 500:
                raise
            error: Exception = e
        except openai.APIConnectionError as e:
            error = e
        attempt += 1
        if attempt > MAX_RETRIES:
            raise RuntimeError(f"giving up after {MAX_RETRIES} retries: {error}") from error
        print(f"request failed ({error}); retry {attempt}/{MAX_RETRIES}", flush=True)
        time.sleep(RETRY_SLEEP_S)


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

    original_decoder_init = OpenAIChatDecoder.__init__

    @functools.wraps(original_decoder_init)
    def decoder_init(self, *args, **kwargs):  # type: ignore[no-untyped-def]
        original_decoder_init(self, *args, **kwargs)
        self.client = self.client.with_options(timeout=None)

    OpenAIChatDecoder.__init__ = decoder_init  # type: ignore[method-assign]

    original_codegen = OpenAIChatDecoder.codegen

    @functools.wraps(original_codegen)
    def codegen(self, *args, **kwargs):  # type: ignore[no-untyped-def]
        return [out if out is not None else "" for out in original_codegen(self, *args, **kwargs)]

    OpenAIChatDecoder.codegen = codegen  # type: ignore[method-assign]

    openai_request.make_auto_request = _bounded_request  # type: ignore[assignment]


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
