"""FastAPI app: shared lifespan/health plumbing wired from vigil_common,
plus the service-specific /predict endpoint the Go gateway calls. Contract
(field names, status codes) is frozen in docs/api.md.
"""

from fastapi import FastAPI, HTTPException, Request
from pydantic import BaseModel, field_validator
from vigil_common.log_config import RequestIDMiddleware, configure_logging
from vigil_common.service import health_router, make_lifespan

from app.config import get_settings
from app.model import ModelBackend, OnnxModel, StubModel

configure_logging()


def _load_backend() -> ModelBackend:
    """Instantiate the configured ModelBackend. Raises on failure — the
    caller (lifespan) decides how to degrade.
    """
    settings = get_settings()
    if settings.model_backend == "stub":
        return StubModel()

    if not settings.model_path or not settings.tokenizer_path:
        raise ValueError("MODEL_PATH and TOKENIZER_PATH are required when MODEL_BACKEND=onnx")
    return OnnxModel(
        model_path=settings.model_path,
        tokenizer_path=settings.tokenizer_path,
        model_version=settings.model_version,
    )


app = FastAPI(lifespan=make_lifespan(_load_backend))
app.add_middleware(RequestIDMiddleware)
app.include_router(health_router)


class PredictRequest(BaseModel):
    content: str

    @field_validator("content")
    @classmethod
    def validate_content(cls, value: str) -> str:
        stripped = value.strip()
        if not stripped:
            raise ValueError("content must not be empty")

        max_length = get_settings().max_content_length
        if len(stripped) > max_length:
            raise ValueError(f"content exceeds max length of {max_length}")
        return stripped


class Scores(BaseModel):
    toxicity: float
    spam: float


class PredictResponse(BaseModel):
    scores: Scores
    model_version: str


@app.post("/predict", response_model=PredictResponse)
async def predict(payload: PredictRequest, request: Request) -> PredictResponse:
    model: ModelBackend | None = request.app.state.model
    if model is None:
        raise HTTPException(status_code=503, detail="model backend not loaded")

    scores = model.predict(payload.content)
    return PredictResponse(scores=Scores(**scores), model_version=model.model_version)
