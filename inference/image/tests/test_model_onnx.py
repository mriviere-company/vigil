"""Unit tests for OnnxModel, with the onnxruntime boundary mocked out so
this is fully testable without the real model file (fetched separately by
scripts/fetch_model.py, gitignored, not present in CI/dev).
"""

from typing import Any

import numpy as np
import pytest
from PIL import Image

from app.model import OnnxModel


def _png_bytes(color: tuple[int, int, int] = (12, 34, 56)) -> bytes:
    import io

    image = Image.new("RGB", (8, 8), color)
    buffer = io.BytesIO()
    image.save(buffer, format="PNG")
    return buffer.getvalue()


class _FakeSession:
    def __init__(self, logits: list[list[float]]) -> None:
        self._logits = np.array(logits, dtype=np.float32)

    def get_inputs(self) -> list[Any]:
        class _Input:
            name = "pixel_values"

        return [_Input()]

    def run(self, output_names: list[str] | None, feed: dict[str, Any]) -> list[np.ndarray]:
        assert "pixel_values" in feed
        pixel_values = feed["pixel_values"]
        assert pixel_values.shape == (1, 3, 224, 224)
        assert pixel_values.dtype == np.float32
        return [self._logits]


@pytest.fixture
def patched_session(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(
        "app.model.InferenceSession",
        lambda *_args, **_kwargs: _FakeSession(logits=[[0.0, 3.0]]),
    )


def test_nsfw_is_softmax_probability_of_nsfw_class(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    # id2label: {0: "normal", 1: "nsfw"} -> nsfw is index 1.
    monkeypatch.setattr(
        "app.model.InferenceSession",
        lambda *_args, **_kwargs: _FakeSession(logits=[[0.0, 3.0]]),
    )

    model = OnnxModel(model_path="/fake/model.onnx", model_version="v1")
    scores = model.predict(_png_bytes())

    exp0, exp1 = np.exp(0.0), np.exp(3.0)
    expected_nsfw = exp1 / (exp0 + exp1)
    assert scores["nsfw"] == pytest.approx(expected_nsfw, abs=1e-6)


def test_violence_is_always_zero(monkeypatch: pytest.MonkeyPatch, patched_session: None) -> None:
    model = OnnxModel(model_path="/fake/model.onnx", model_version="v1")
    scores = model.predict(_png_bytes())

    assert scores["violence"] == 0.0


def test_model_version_comes_from_constructor(
    monkeypatch: pytest.MonkeyPatch, patched_session: None
) -> None:
    model = OnnxModel(model_path="/fake/model.onnx", model_version="nsfw-vit-onnx-1")

    assert model.model_version == "nsfw-vit-onnx-1"


def test_scores_within_unit_interval(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(
        "app.model.InferenceSession",
        lambda *_args, **_kwargs: _FakeSession(logits=[[-2.0, 5.0]]),
    )

    model = OnnxModel(model_path="/fake/model.onnx", model_version="v1")
    scores = model.predict(_png_bytes())

    assert 0.0 <= scores["nsfw"] <= 1.0
