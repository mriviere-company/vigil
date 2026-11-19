"""Contract tests for POST /predict — field names and shapes are frozen in docs/api.md."""

from fastapi.testclient import TestClient


def test_predict_response_shape_and_field_names(client: TestClient) -> None:
    response = client.post("/predict", json={"content": "hello world"})

    assert response.status_code == 200
    body = response.json()
    assert set(body.keys()) == {"scores", "model_version"}
    assert set(body["scores"].keys()) == {"toxicity", "spam"}
    assert isinstance(body["model_version"], str)
    assert body["model_version"]


def test_predict_scores_within_unit_interval(client: TestClient) -> None:
    response = client.post("/predict", json={"content": "you are hateful and I hate you"})

    scores = response.json()["scores"]
    assert 0.0 <= scores["toxicity"] <= 1.0
    assert 0.0 <= scores["spam"] <= 1.0


def test_predict_is_deterministic(client: TestClient) -> None:
    payload = {"content": "buy now, limited offer!!!"}

    first = client.post("/predict", json=payload).json()
    second = client.post("/predict", json=payload).json()

    assert first == second
