"""Contract tests for POST /predict — field names and shapes are frozen in docs/api.md."""

import base64
from collections.abc import Callable

from fastapi.testclient import TestClient


def test_predict_response_shape_and_field_names(
    client: TestClient, make_image: Callable[..., bytes]
) -> None:
    payload = base64.b64encode(make_image()).decode()

    response = client.post("/predict", json={"image_base64": payload})

    assert response.status_code == 200
    body = response.json()
    assert set(body.keys()) == {"scores", "model_version"}
    assert set(body["scores"].keys()) == {"nsfw", "violence"}
    assert isinstance(body["model_version"], str)
    assert body["model_version"]


def test_predict_scores_within_unit_interval(
    client: TestClient, make_image: Callable[..., bytes]
) -> None:
    payload = base64.b64encode(make_image(color=(255, 0, 0))).decode()

    response = client.post("/predict", json={"image_base64": payload})

    scores = response.json()["scores"]
    assert 0.0 <= scores["nsfw"] <= 1.0
    assert 0.0 <= scores["violence"] <= 1.0


def test_predict_is_deterministic(client: TestClient, make_image: Callable[..., bytes]) -> None:
    payload = {"image_base64": base64.b64encode(make_image(color=(0, 0, 255))).decode()}

    first = client.post("/predict", json=payload).json()
    second = client.post("/predict", json=payload).json()

    assert first == second
