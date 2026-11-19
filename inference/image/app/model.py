"""Model backends.

Design pattern — Strategy via typing.Protocol: `ModelBackend` is the seam
between HTTP layer and ML. `StubModel` is a deterministic, dependency-free
implementation used by every test and as the local default (MODEL_BACKEND=stub
requires zero setup). `OnnxModel` is the real implementation, selected in
production via the same env var. Neither the endpoint code nor the tests care
which one is behind the seam — that's the point of the interface.

Mirrors inference/text/app/model.py's shape; the only real difference is the
seam type (`bytes` here, `str` there) and the ML behind it, per the brief's
"each service keeps its own ModelBackend protocol" instruction — not shared
via common/.
"""

import io
from typing import Protocol

import numpy as np
from onnxruntime import InferenceSession
from PIL import Image


class ModelBackend(Protocol):
    """Anything that can score image content for nsfw/violence."""

    model_version: str

    def predict(self, image: bytes) -> dict[str, float]:
        """Return {"nsfw": <0..1>, "violence": <0..1>} for the given image bytes."""
        ...


class StubModel:
    """Deterministic, no-I/O backend. The top-left pixel's color is the
    thresh-triggering signal, so tests get precise control over both the
    "high score" and "low score" paths via tiny generated PNGs, without a
    real model.
    """

    model_version = "stub-1"

    def predict(self, image: bytes) -> dict[str, float]:
        with Image.open(io.BytesIO(image)) as decoded:
            top_left_pixel = decoded.convert("RGB").getpixel((0, 0))

        nsfw = 0.95 if top_left_pixel == (255, 0, 0) else 0.05
        violence = 0.95 if top_left_pixel == (0, 0, 255) else 0.05
        return {"nsfw": nsfw, "violence": violence}


class OnnxModel:
    """Real backend: a ViT-based NSFW image classifier run via onnxruntime
    (CPU). Kept to onnxruntime + pillow + numpy only (no torch/transformers)
    to stay inside the container size budget.

    Preprocessing (resize 224x224, RGB, rescale to [0, 1], normalize with
    mean=std=0.5 per channel) matches the source model's preprocessor_config
    (a standard ViTFeatureExtractor config — see scripts/fetch_model.py for
    the exact repo).

    `nsfw` is the softmax probability of the model's "nsfw" class
    (id2label: {0: "normal", 1: "nsfw"} per the model's config.json).

    `violence` is hardcoded to 0.0: this model has no violence signal at all
    — violence detection would need a second, different model, which is out
    of scope here (the ML is deliberately not the point of this project) —
    same documented pattern as `spam` in the text service.
    """

    _INPUT_SIZE = (224, 224)
    _MEAN = np.array([0.5, 0.5, 0.5], dtype=np.float32)
    _STD = np.array([0.5, 0.5, 0.5], dtype=np.float32)
    _NSFW_LABEL_INDEX = 1  # id2label: {0: "normal", 1: "nsfw"}

    def __init__(self, model_path: str, model_version: str) -> None:
        self.model_version = model_version
        self._session = InferenceSession(model_path, providers=["CPUExecutionProvider"])
        self._input_name = self._session.get_inputs()[0].name

    def predict(self, image: bytes) -> dict[str, float]:
        pixel_values = self._preprocess(image)
        (logits,) = self._session.run(None, {self._input_name: pixel_values})
        probabilities = self._softmax(logits[0])
        nsfw = float(probabilities[self._NSFW_LABEL_INDEX])
        return {"nsfw": nsfw, "violence": 0.0}

    def _preprocess(self, image: bytes) -> np.ndarray:
        with Image.open(io.BytesIO(image)) as decoded:
            # Explicit bilinear: the model card's preprocessor_config says
            # "resample": 2 (PIL BILINEAR) — don't rely on Pillow's default,
            # which is the higher-order bicubic.
            resized = decoded.convert("RGB").resize(
                self._INPUT_SIZE, resample=Image.Resampling.BILINEAR
            )

        pixels = np.asarray(resized, dtype=np.float32) / 255.0
        normalized = (pixels - self._MEAN) / self._STD
        channels_first = normalized.transpose(2, 0, 1)  # HWC -> CHW
        return channels_first[np.newaxis, ...]  # add batch dim -> NCHW

    @staticmethod
    def _softmax(logits: np.ndarray) -> np.ndarray:
        shifted = logits - np.max(logits)
        exp = np.exp(shifted)
        return exp / exp.sum()
