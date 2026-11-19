"""CommonSettings: the shared fields/config every service's Settings
inherits, exercised directly (each service's own tests cover its subclass's
additional fields and env overrides)."""

import pytest

from vigil_common.settings import CommonSettings


def test_defaults() -> None:
    settings = CommonSettings()

    assert settings.model_backend == "stub"
    assert settings.model_path is None


def test_model_backend_is_read_from_env(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("MODEL_BACKEND", "onnx")

    assert CommonSettings().model_backend == "onnx"


def test_model_path_is_read_from_env(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("MODEL_PATH", "/some/model.onnx")

    assert CommonSettings().model_path == "/some/model.onnx"


def test_field_names_starting_with_model_do_not_raise() -> None:
    # Regression guard for the protected_namespaces=() setting: pydantic
    # would otherwise warn/raise on "model_*" field names colliding with
    # BaseModel's own namespace.
    CommonSettings()
