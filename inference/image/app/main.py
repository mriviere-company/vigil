"""FastAPI app: shared lifespan/health plumbing wired from vigil_common,
plus the service-specific /predict endpoint the Go gateway calls. Contract
(field names, status codes) is frozen in docs/api.md.
"""

import base64
import binascii
import io

from fastapi import FastAPI, HTTPException, Request
from PIL import Image, UnidentifiedImageError
from pydantic import BaseModel, field_validator
from vigil_common.log_config import RequestIDMiddleware, configure_logging
from vigil_common.service import health_router, make_lifespan

from app.config import get_settings
from app.model import ModelBackend, OnnxModel, StubModel

configure_logging(get_settings().log_level)

_ALLOWED_FORMATS = {"JPEG", "PNG", "WEBP"}


def _load_backend() -> ModelBackend:
    """Instantiate the configured ModelBackend. Raises on failure — the
    caller (lifespan) decides how to degrade.
    """
    settings = get_settings()
    if settings.model_backend == "stub":
        return StubModel()

    if not settings.model_path:
        raise ValueError("MODEL_PATH is required when MODEL_BACKEND=onnx")
    return OnnxModel(model_path=settings.model_path, model_version=settings.model_version)


app = FastAPI(lifespan=make_lifespan(_load_backend))
app.add_middleware(RequestIDMiddleware)
app.include_router(health_router)


class PredictRequest(BaseModel):
    image_base64: str

    @field_validator("image_base64")
    @classmethod
    def validate_image_base64(cls, value: str) -> str:
        try:
            decoded = base64.b64decode(value, validate=True)
        except (binascii.Error, ValueError) as exc:
            raise ValueError("image_base64 must be valid base64") from exc

        max_bytes = get_settings().max_image_bytes
        if len(decoded) > max_bytes:
            raise ValueError(f"image exceeds max size of {max_bytes} bytes")

        try:
            with Image.open(io.BytesIO(decoded)) as image:
                image_format = image.format
        except Image.DecompressionBombError as exc:
            raise ValueError("image exceeds Pillow's maximum pixel count") from exc
        except UnidentifiedImageError as exc:
            raise ValueError("image_base64 must decode to a JPEG, PNG, or WebP image") from exc

        if image_format not in _ALLOWED_FORMATS:
            raise ValueError(f"unsupported image format: {image_format}")

        return value


class Scores(BaseModel):
    nsfw: float
    violence: float


class PredictResponse(BaseModel):
    scores: Scores
    model_version: str


@app.post("/predict", response_model=PredictResponse)
async def predict(payload: PredictRequest, request: Request) -> PredictResponse:
    model: ModelBackend | None = request.app.state.model
    if model is None:
        raise HTTPException(status_code=503, detail="model backend not loaded")

    decoded = base64.b64decode(payload.image_base64)
    scores = model.predict(decoded)
    return PredictResponse(scores=Scores(**scores), model_version=model.model_version)
