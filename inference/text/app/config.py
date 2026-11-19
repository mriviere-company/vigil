"""12-factor config: every tunable comes from an env var with a sane local
default, so `MODEL_BACKEND=stub uv run uvicorn app.main:app` works with zero
setup. Settings() re-reads the environment on every call rather than being a
frozen module-level singleton, so tests can monkeypatch env vars per-case.

`model_backend` and `model_path` are inherited from CommonSettings (shared,
verbatim-identical across services); everything below is text-specific.
"""

from vigil_common.settings import CommonSettings


class Settings(CommonSettings):
    max_content_length: int = 10000

    # Only used when model_backend == "onnx".
    tokenizer_path: str | None = None
    model_version: str = "multilingual-toxic-xlm-r-onnx-int8-1"


def get_settings() -> Settings:
    return Settings()
