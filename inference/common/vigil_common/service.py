"""Shared FastAPI service plumbing: startup model loading with degraded-mode
fallback, and the /health endpoint — identical, by contract (docs/api.md),
across every inference service.

Each service keeps its own `_load_backend()` (what "loading a model" means
genuinely differs per service — text needs a tokenizer, image doesn't) and
passes it to `make_lifespan`; everything downstream of that call is the
same in both services, so it lives here instead of being copy-pasted.
"""

import logging
from collections.abc import AsyncIterator, Callable
from contextlib import AbstractAsyncContextManager, asynccontextmanager

from fastapi import APIRouter, FastAPI, Request
from pydantic import BaseModel

logger = logging.getLogger(__name__)


class HealthResponse(BaseModel):
    status: str
    model_loaded: bool


def make_lifespan(
    load_backend: Callable[[], object],
) -> Callable[[FastAPI], AbstractAsyncContextManager[None]]:
    """Builds a lifespan that loads the model backend once at startup. A load
    failure is logged and leaves app.state.model as None rather than crashing
    the process — the gateway's circuit breaker reads /health's model_loaded
    instead.

    `load_backend` is typed `() -> object` rather than a shared backend
    protocol on purpose: the services' ModelBackend protocols differ (text
    scores str, image scores bytes) and this layer never calls predict() —
    it only stores the backend for the service's own endpoints to use.
    """

    @asynccontextmanager
    async def lifespan(app: FastAPI) -> AsyncIterator[None]:
        try:
            app.state.model = load_backend()
        except Exception:
            logger.exception("model backend failed to load; serving with model_loaded=false")
            app.state.model = None
        yield

    return lifespan


# A single module-level router is safe to include in multiple apps:
# include_router copies routes into the app, and the handler itself is
# stateless (it reads the per-app state off the incoming request).
health_router = APIRouter()


@health_router.get("/health", response_model=HealthResponse)
async def health(request: Request) -> HealthResponse:
    model: object | None = request.app.state.model
    return HealthResponse(status="ok", model_loaded=model is not None)
