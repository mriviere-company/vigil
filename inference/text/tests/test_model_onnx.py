"""Unit tests for OnnxModel, with the onnxruntime/tokenizers boundary mocked
out so this is fully testable without the real model file (fetched
separately by scripts/fetch_model.py, gitignored, not present in CI/dev).
"""

from typing import Any

import numpy as np
import pytest

from app.model import OnnxModel


class _FakeEncoding:
    def __init__(self, ids: list[int], attention_mask: list[int]) -> None:
        self.ids = ids
        self.attention_mask = attention_mask


class _FakeTokenizer:
    def encode(self, content: str) -> _FakeEncoding:
        # Fixed-length fake tokenization; content is irrelevant to the fake.
        return _FakeEncoding(ids=[101, 2000, 2001, 102], attention_mask=[1, 1, 1, 1])


class _FakeSession:
    def __init__(self, logits: list[list[float]]) -> None:
        self._logits = np.array(logits, dtype=np.float32)

    def get_inputs(self) -> list[Any]:
        class _Input:
            def __init__(self, name: str) -> None:
                self.name = name

        return [_Input("input_ids"), _Input("attention_mask")]

    def run(self, output_names: list[str] | None, feed: dict[str, Any]) -> list[np.ndarray]:
        assert "input_ids" in feed
        assert "attention_mask" in feed
        return [self._logits]


@pytest.fixture
def patched_onnx(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr("app.model.Tokenizer.from_file", lambda _path: _FakeTokenizer())


def test_toxicity_is_the_label_zero_logit(
    monkeypatch: pytest.MonkeyPatch, patched_onnx: None
) -> None:
    # unitary/multilingual-toxic-xlm-roberta's config.json has a single
    # output label (id2label={"0": "toxic"}), unlike the old toxic-bert
    # backend's six per-category logits. Single-label output means
    # "toxicity" is just sigmoid(logits[0]).
    monkeypatch.setattr(
        "app.model.InferenceSession",
        lambda *_args, **_kwargs: _FakeSession(logits=[[3.0]]),
    )

    model = OnnxModel(
        model_path="/fake/model.onnx", tokenizer_path="/fake/tokenizer.json", model_version="v1"
    )
    scores = model.predict("some content")

    assert scores["toxicity"] == pytest.approx(1 / (1 + pow(2.718281828, -3.0)), abs=1e-3)


def test_toxicity_reads_label_index_zero_specifically_not_the_max(
    monkeypatch: pytest.MonkeyPatch, patched_onnx: None
) -> None:
    # Pins the mapping to "index 0", not "whichever logit is largest": a
    # session that (contrary to the real checkpoint) returned extra logits
    # with a bigger value at another index must still be read at index 0.
    # This guards against silently reintroducing toxic-bert's max-over-labels
    # reduction, which would be wrong for this single-label model.
    monkeypatch.setattr(
        "app.model.InferenceSession",
        lambda *_args, **_kwargs: _FakeSession(logits=[[0.5, 5.0]]),
    )

    model = OnnxModel(
        model_path="/fake/model.onnx", tokenizer_path="/fake/tokenizer.json", model_version="v1"
    )
    scores = model.predict("some content")

    assert scores["toxicity"] == pytest.approx(1 / (1 + pow(2.718281828, -0.5)), abs=1e-3)


def test_spam_is_always_zero(monkeypatch: pytest.MonkeyPatch, patched_onnx: None) -> None:
    monkeypatch.setattr(
        "app.model.InferenceSession",
        lambda *_args, **_kwargs: _FakeSession(logits=[[5.0, 5.0]]),
    )

    model = OnnxModel(
        model_path="/fake/model.onnx", tokenizer_path="/fake/tokenizer.json", model_version="v1"
    )
    scores = model.predict("anything")

    assert scores["spam"] == 0.0


def test_model_version_comes_from_constructor(
    monkeypatch: pytest.MonkeyPatch, patched_onnx: None
) -> None:
    monkeypatch.setattr(
        "app.model.InferenceSession",
        lambda *_args, **_kwargs: _FakeSession(logits=[[0.0]]),
    )

    model = OnnxModel(
        model_path="/fake/model.onnx",
        tokenizer_path="/fake/tokenizer.json",
        model_version="toxic-bert-onnx-1",
    )

    assert model.model_version == "toxic-bert-onnx-1"
