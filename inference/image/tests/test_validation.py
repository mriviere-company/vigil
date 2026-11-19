"""Input validation: bad/oversized/wrong-format payloads -> 422 with Pydantic detail."""

import base64
from collections.abc import Callable

import pytest
from fastapi.testclient import TestClient
from PIL import Image


def test_missing_image_base64_field_is_rejected(client: TestClient) -> None:
    response = client.post("/predict", json={})

    assert response.status_code == 422


def test_invalid_base64_is_rejected(client: TestClient) -> None:
    response = client.post("/predict", json={"image_base64": "not valid base64!!"})

    assert response.status_code == 422


def test_oversized_payload_is_rejected(
    client: TestClient, make_image: Callable[..., bytes], monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setenv("MAX_IMAGE_BYTES", "10")
    payload = base64.b64encode(make_image()).decode()

    response = client.post("/predict", json={"image_base64": payload})

    assert response.status_code == 422


def test_unsupported_format_is_rejected(client: TestClient) -> None:
    # Valid base64, decodes to bytes, but is not an image Pillow recognizes.
    payload = base64.b64encode(b"just some plain bytes, not an image").decode()

    response = client.post("/predict", json={"image_base64": payload})

    assert response.status_code == 422


def test_decompression_bomb_is_rejected(
    client: TestClient, make_image: Callable[..., bytes], monkeypatch: pytest.MonkeyPatch
) -> None:
    # Lower Pillow's pixel-count ceiling instead of generating a huge image,
    # so the DecompressionBombError path is exercised quickly and safely —
    # the app itself must never touch/disable MAX_IMAGE_PIXELS.
    monkeypatch.setattr(Image, "MAX_IMAGE_PIXELS", 4)
    payload = base64.b64encode(make_image(size=(4, 4))).decode()

    response = client.post("/predict", json={"image_base64": payload})

    assert response.status_code == 422


def test_valid_png_is_accepted(client: TestClient, make_image: Callable[..., bytes]) -> None:
    payload = base64.b64encode(make_image(fmt="PNG")).decode()

    response = client.post("/predict", json={"image_base64": payload})

    assert response.status_code == 200


def test_valid_jpeg_is_accepted(client: TestClient, make_image: Callable[..., bytes]) -> None:
    payload = base64.b64encode(make_image(fmt="JPEG")).decode()

    response = client.post("/predict", json={"image_base64": payload})

    assert response.status_code == 200


def test_valid_webp_is_accepted(client: TestClient, make_image: Callable[..., bytes]) -> None:
    payload = base64.b64encode(make_image(fmt="WEBP")).decode()

    response = client.post("/predict", json={"image_base64": payload})

    assert response.status_code == 200
