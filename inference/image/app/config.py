"""12-factor config: every tunable comes from an env var with a sane local
default, so `MODEL_BACKEND=stub uv run uvicorn app.main:app` works with zero
setup. Settings() re-reads the environment on every call rather than being a
frozen module-level singleton, so tests can monkeypatch env vars per-case.

`model_backend` and `model_path` are inherited from CommonSettings (shared,
verbatim-identical across services); everything below is image-specific.
"""

from vigil_common.settings import CommonSettings


class Settings(CommonSettings):
    max_image_bytes: int = 5_000_000
    model_version: str = "nsfw-vit-onnx-1"
    log_level: str = "INFO"


def get_settings() -> Settings:
    return Settings()
