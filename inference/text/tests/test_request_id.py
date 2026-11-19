"""Request-id propagation: echoed if provided, generated (uuid4) if absent."""

import uuid

from fastapi.testclient import TestClient


def test_provided_request_id_is_echoed_back(client: TestClient) -> None:
    given = "11111111-1111-1111-1111-111111111111"

    response = client.get("/health", headers={"X-Request-ID": given})

    assert response.headers["X-Request-ID"] == given


def test_missing_request_id_is_generated(client: TestClient) -> None:
    response = client.get("/health")

    generated = response.headers["X-Request-ID"]
    # Must be a valid uuid4 — round-trips through UUID() without raising.
    assert uuid.UUID(generated).version == 4


def test_two_requests_without_header_get_different_ids(client: TestClient) -> None:
    first = client.get("/health").headers["X-Request-ID"]
    second = client.get("/health").headers["X-Request-ID"]

    assert first != second
