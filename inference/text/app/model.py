"""Model backends.

Design pattern — Strategy via typing.Protocol: `ModelBackend` is the seam
between HTTP layer and ML. `StubModel` is a deterministic, dependency-free
implementation used by every test and as the local default (MODEL_BACKEND=stub
requires zero setup). `OnnxModel` is the real implementation, selected in
production via the same env var. Neither the endpoint code nor the tests care
which one is behind the seam — that's the point of the interface.
"""

from typing import Any, Protocol

import numpy as np
from onnxruntime import InferenceSession
from tokenizers import Tokenizer


class ModelBackend(Protocol):
    """Anything that can score text content for toxicity/spam."""

    model_version: str

    def predict(self, content: str) -> dict[str, float]:
        """Return {"toxicity": <0..1>, "spam": <0..1>} for the given content."""
        ...


class StubModel:
    """Deterministic, no-I/O backend. Keyword-driven so tests can exercise
    both the "high score" and "low score" paths without a real model.
    """

    model_version = "stub-1"

    def predict(self, content: str) -> dict[str, float]:
        lowered = content.lower()
        toxicity = 0.95 if "hate" in lowered else 0.05
        spam = 0.95 if "buy now" in lowered else 0.05
        return {"toxicity": toxicity, "spam": spam}


class OnnxModel:
    """Real backend: an ONNX model run via onnxruntime (CPU) with
    tokenization via `tokenizers`. Kept to onnxruntime + tokenizers only (no
    torch/transformers) to stay inside the container size budget.

    Ships unitary/multilingual-toxic-xlm-roberta (XLM-R base, quantized to
    int8), which replaced the earlier Xenova/toxic-bert backend to add
    French (and 5 other languages) support — see benchmarks/README.md for
    the FR/EN comparison that motivated the swap.

    `toxicity` is `sigmoid(logits[TOXICITY_LABEL_INDEX])`. Unlike
    toxic-bert (one logit per toxic category — toxic/severe_toxic/obscene/
    threat/insult/identity_hate — reduced via max-over-labels), this
    checkpoint's HF config.json declares a single output label,
    `id2label={"0": "toxic"}` (verified against the actual classifier
    weights, not just the config, since the two can disagree for
    community re-exports of this model family). Reading index 0 explicitly
    — rather than reusing a max-over-labels reduction that would
    coincidentally give the same answer for a singleton output — keeps the
    mapping correct if a future model swap reintroduces multiple heads.

    `spam` is hardcoded to 0.0: this model has no spam signal at all — spam
    detection would need a second, different model, which is out of scope
    here (the ML is deliberately not the point of this project).
    """

    TOXICITY_LABEL_INDEX = 0

    def __init__(self, model_path: str, tokenizer_path: str, model_version: str) -> None:
        self.model_version = model_version
        self._tokenizer = Tokenizer.from_file(tokenizer_path)
        self._session = InferenceSession(model_path, providers=["CPUExecutionProvider"])
        self._input_names = {inp.name for inp in self._session.get_inputs()}

    def predict(self, content: str) -> dict[str, float]:
        encoding = self._tokenizer.encode(content)
        input_ids = np.array([encoding.ids], dtype=np.int64)
        attention_mask = np.array([encoding.attention_mask], dtype=np.int64)

        feed: dict[str, Any] = {"input_ids": input_ids, "attention_mask": attention_mask}
        if "token_type_ids" in self._input_names:
            feed["token_type_ids"] = np.zeros_like(input_ids)

        (logits,) = self._session.run(None, feed)
        sigmoid = 1.0 / (1.0 + np.exp(-logits[0]))
        toxicity = float(sigmoid[self.TOXICITY_LABEL_INDEX])

        return {"toxicity": toxicity, "spam": 0.0}
