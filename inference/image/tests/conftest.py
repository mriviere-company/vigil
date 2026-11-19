"""Shared pytest fixtures.

All tests exercise the app wired to StubModel (deterministic, no I/O) —
never a real ONNX model. That's the point of the ModelBackend protocol.
"""

import io
from collections.abc import Callable, Iterator

import pytest
from fastapi.testclient import TestClient
from PIL import Image


@pytest.fixture
def make_image() -> Callable[..., bytes]:
    """Factory for tiny in-memory test images — no binary fixtures committed.

    Defaults to a small neutral-colored PNG; pass `color`/`fmt`/`size` to get
    other formats or the stub's threshold-triggering colors.
    """

    def _make_image(
        color: tuple[int, int, int] = (10, 20, 30),
        fmt: str = "PNG",
        size: tuple[int, int] = (4, 4),
    ) -> bytes:
        image = Image.new("RGB", size, color)
        buffer = io.BytesIO()
        image.save(buffer, format=fmt)
        return buffer.getvalue()

    return _make_image


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

    Points MODEL_BACKEND at "onnx" with a nonexistent model path so the
    lifespan's load step raises. The lifespan must catch that, log it, and
    keep serving with model_loaded: false instead of crashing.
    """
    monkeypatch.setenv("MODEL_BACKEND", "onnx")
    monkeypatch.setenv("MODEL_PATH", "/nonexistent/model.onnx")

    from app.main import app

    with TestClient(app) as test_client:
        yield test_client
