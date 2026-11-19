"""Unit tests for StubModel — the deterministic, no-I/O default ModelBackend."""

from app.model import StubModel


def test_stub_model_version() -> None:
    assert StubModel().model_version == "stub-1"


def test_hate_keyword_drives_high_toxicity() -> None:
    scores = StubModel().predict("I absolutely hate this")

    assert scores["toxicity"] >= 0.9


def test_buy_now_keyword_drives_high_spam() -> None:
    scores = StubModel().predict("buy now while supplies last")

    assert scores["spam"] >= 0.9


def test_neutral_content_has_low_scores() -> None:
    scores = StubModel().predict("the weather is nice today")

    assert scores["toxicity"] < 0.1
    assert scores["spam"] < 0.1


def test_predict_is_pure() -> None:
    model = StubModel()

    assert model.predict("some content") == model.predict("some content")


def test_predict_returns_toxicity_and_spam_keys() -> None:
    scores = StubModel().predict("anything")

    assert set(scores.keys()) == {"toxicity", "spam"}
