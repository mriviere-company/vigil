"""Shared pytest fixtures.

All tests exercise the app wired to StubModel (deterministic, no I/O) —
never a real ONNX model. That's the point of the ModelBackend protocol.
"""

from collections.abc import Iterator

import pytest
from fastapi.testclient import TestClient


@pytest.fixture
def client(monkeypatch: pytest.MonkeyPatch) -> Iterator[TestClient]:
    """App wired to StubModel via the default MODEL_BACKEND=stub."""
    monkeypatch.setenv("MODEL_BACKEND", "stub")

    from app.main import app

    with TestClient(app) as test_client:
        yield test_client


@pytest.fixture
def client_backend_unavailable(monkeypatch: pytest.MonkeyPatch) -> Iterator[TestClient]:
    """App whose backend failed to load, simulating a real startup failure.

    Points MODEL_BACKEND at "onnx" with a nonexistent model/tokenizer path so
    the lifespan's load step raises. The lifespan must catch that, log it,
    and keep serving with model_loaded: false instead of crashing.
    """
    monkeypatch.setenv("MODEL_BACKEND", "onnx")
    monkeypatch.setenv("MODEL_PATH", "/nonexistent/model.onnx")
    monkeypatch.setenv("TOKENIZER_PATH", "/nonexistent/tokenizer.json")

    from app.main import app

    with TestClient(app) as test_client:
        yield test_client
