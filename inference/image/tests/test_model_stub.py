"""Unit tests for StubModel — the deterministic, no-I/O default ModelBackend."""

from collections.abc import Callable

from app.model import StubModel


def test_stub_model_version() -> None:
    assert StubModel().model_version == "stub-1"


def test_pure_red_top_left_pixel_drives_high_nsfw(make_image: Callable[..., bytes]) -> None:
    image = make_image(color=(255, 0, 0))

    scores = StubModel().predict(image)

    assert scores["nsfw"] >= 0.9


def test_pure_blue_top_left_pixel_drives_high_violence(make_image: Callable[..., bytes]) -> None:
    image = make_image(color=(0, 0, 255))

    scores = StubModel().predict(image)

    assert scores["violence"] >= 0.9


def test_neutral_image_has_low_scores(make_image: Callable[..., bytes]) -> None:
    image = make_image(color=(10, 20, 30))

    scores = StubModel().predict(image)

    assert scores["nsfw"] < 0.1
    assert scores["violence"] < 0.1


def test_predict_is_pure(make_image: Callable[..., bytes]) -> None:
    model = StubModel()
    image = make_image(color=(1, 2, 3))

    assert model.predict(image) == model.predict(image)


def test_predict_returns_nsfw_and_violence_keys(make_image: Callable[..., bytes]) -> None:
    scores = StubModel().predict(make_image())

    assert set(scores.keys()) == {"nsfw", "violence"}
