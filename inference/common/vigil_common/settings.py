"""Settings scaffolding shared by every inference service's own config.

Only the parts that are genuinely identical across services live here: the
pydantic-settings config quirk below, and the `model_backend` / `model_path`
fields every ModelBackend-fronted service needs regardless of what it's
actually classifying. Each service subclasses `CommonSettings` and adds its
own fields and its own defaults (max_content_length vs max_image_bytes,
model_version's default, tokenizer_path, ...) — no attempt is made to share
those, since they genuinely differ per service.
"""

from typing import Literal

from pydantic_settings import BaseSettings, SettingsConfigDict


class CommonSettings(BaseSettings):
    model_config = SettingsConfigDict(
        # Field names starting with "model_" would otherwise trip pydantic's
        # protected-namespace check (it guards against shadowing BaseModel's
        # own `model_*` methods) — none of ours do, so silence it.
        protected_namespaces=(),
    )

    model_backend: Literal["stub", "onnx"] = "stub"

    # Only used when model_backend == "onnx". Each service's own Settings
    # adds whatever else "onnx" mode needs (e.g. text's tokenizer_path).
    model_path: str | None = None
