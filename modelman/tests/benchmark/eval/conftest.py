"""Shared fixtures for the eval-benchmark tests."""

import pytest

from modelman.providers.lifecycle.backends import BACKENDS

# Backends whose check_available() looks for a binary on PATH (shutil.which).
_BINARY_BACKENDS = ("omlx", "omlx-6bit", "mtplx", "mlx_lm_server")


@pytest.fixture(autouse=True)
def _binary_backends_available(monkeypatch):
    # preflight() asks each selected row's backend check_available(), which for
    # omlx/mtplx/mlx_lm_server is a PATH lookup. That passes on a dev machine
    # with those tools installed but fails on a bare CI runner ("omlx binary not
    # found on PATH"), so eval tests would pass or fail by host. Stubbing the
    # check keeps them hermetic; the real check is covered by the backend tests.
    for provider_id in _BINARY_BACKENDS:
        monkeypatch.setattr(BACKENDS[provider_id], "check_available", lambda: None)
