"""/health and the 503-on-unavailable-backend degraded-mode contract."""

import base64
from collections.abc import Callable

from fastapi.testclient import TestClient


def test_health_reports_model_loaded_true_with_stub(client: TestClient) -> None:
    response = client.get("/health")

    assert response.status_code == 200
    assert response.json() == {"status": "ok", "model_loaded": True}


def test_health_reports_model_loaded_false_when_backend_failed(
    client_backend_unavailable: TestClient,
) -> None:
    response = client_backend_unavailable.get("/health")

    assert response.status_code == 200
    assert response.json() == {"status": "ok", "model_loaded": False}


def test_predict_returns_503_when_backend_failed(
    client_backend_unavailable: TestClient, make_image: Callable[..., bytes]
) -> None:
    # A validation-passing payload, so the 503 is unambiguously about the
    # missing backend rather than a 422 short-circuiting first.
    payload = base64.b64encode(make_image()).decode()

    response = client_backend_unavailable.post("/predict", json={"image_base64": payload})

    assert response.status_code == 503
    assert "detail" in response.json()
