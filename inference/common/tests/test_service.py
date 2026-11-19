"""Shared service plumbing: make_lifespan (backend loading + degraded mode)
and the /health router, exercised through a minimal FastAPI app exactly the
way each inference service wires them up.
"""

from collections.abc import Callable

from fastapi import FastAPI
from fastapi.testclient import TestClient

from vigil_common.service import HealthResponse, health_router, make_lifespan


class _Backend:
    model_version = "test-1"


def _make_app(load_backend: Callable[[], object]) -> FastAPI:
    app = FastAPI(lifespan=make_lifespan(load_backend))
    app.include_router(health_router)
    return app


def test_lifespan_stores_loaded_backend_on_app_state() -> None:
    backend = _Backend()
    app = _make_app(lambda: backend)

    with TestClient(app):
        assert app.state.model is backend


def test_lifespan_swallows_load_failure_and_leaves_model_none() -> None:
    def _explode() -> _Backend:
        raise RuntimeError("model file missing")

    app = _make_app(_explode)

    # Startup must not raise — the service keeps serving in degraded mode.
    with TestClient(app):
        assert app.state.model is None


def test_health_reports_model_loaded_true() -> None:
    app = _make_app(lambda: _Backend())

    with TestClient(app) as client:
        response = client.get("/health")

    assert response.status_code == 200
    assert response.json() == {"status": "ok", "model_loaded": True}


def test_health_reports_model_loaded_false_when_backend_failed() -> None:
    def _explode() -> _Backend:
        raise RuntimeError("model file missing")

    app = _make_app(_explode)

    with TestClient(app) as client:
        response = client.get("/health")

    assert response.status_code == 200
    assert response.json() == {"status": "ok", "model_loaded": False}


def test_health_response_field_names_match_contract() -> None:
    # Field names are frozen in docs/api.md.
    payload = HealthResponse(status="ok", model_loaded=True).model_dump()

    assert set(payload) == {"status", "model_loaded"}
