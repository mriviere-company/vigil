"""/health and the 503-on-unavailable-backend degraded-mode contract."""

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
    client_backend_unavailable: TestClient,
) -> None:
    response = client_backend_unavailable.post("/predict", json={"content": "hello"})

    assert response.status_code == 503
    assert "detail" in response.json()
