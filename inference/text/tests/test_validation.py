"""Input validation: empty/over-length content -> 422 with Pydantic detail."""

import pytest
from fastapi.testclient import TestClient


def test_empty_content_is_rejected(client: TestClient) -> None:
    response = client.post("/predict", json={"content": "   "})

    assert response.status_code == 422


def test_content_over_max_length_is_rejected(
    client: TestClient, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setenv("MAX_CONTENT_LENGTH", "10")

    response = client.post("/predict", json={"content": "this is way more than ten characters"})

    assert response.status_code == 422


def test_missing_content_field_is_rejected(client: TestClient) -> None:
    response = client.post("/predict", json={})

    assert response.status_code == 422


def test_valid_content_within_default_max_length_is_accepted(client: TestClient) -> None:
    response = client.post("/predict", json={"content": "a normal comment"})

    assert response.status_code == 200
