"""Model runners for the OLD and NEW backends.

Deliberately duplicates (rather than imports) the label-mapping logic from
inference/text/app/model.py: benchmarks/ is its own isolated project (see
pyproject.toml) with no dependency on the service's code or venv, so the
two runners here are hand-written to match what the service actually does
for each backend. If the service's mapping ever changes, this file needs a
matching update — there is no shared import to keep them in sync
automatically, which is a deliberate isolation-vs-DRY tradeoff (see
benchmarks/README.md).
"""

from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path

import numpy as np
from huggingface_hub import hf_hub_download
from onnxruntime import InferenceSession, SessionOptions
from tokenizers import Tokenizer

# Xenova/toxic-bert: the OLD backend being retired. Six per-category
# logits (toxic/severe_toxic/obscene/threat/insult/identity_hate); "is this
# toxic in any way" is the max sigmoid over them — mirrors
# inference/text/app/model.py's OnnxModel *before* this bet's swap.
OLD_MODEL_REPO = "Xenova/toxic-bert"
OLD_MODEL_REVISION = "65cbc2607cb7fad64f98d1e8a5a3d1f8577895fd"
OLD_MODEL_FILENAME = "onnx/model_quantized.onnx"
OLD_TOKENIZER_FILENAME = "tokenizer.json"

# unitary/multilingual-toxic-xlm-roberta: the NEW backend. Single output
# label ("toxic") -- mirrors inference/text/app/model.py's OnnxModel
# *after* this bet's swap. Loaded from the SAME on-disk file the service
# itself loads (not re-exported/re-quantized here), so the benchmark is
# measuring the artifact that will actually ship.
NEW_MODEL_REPO = "unitary/multilingual-toxic-xlm-roberta"
NEW_MODEL_REVISION = "4ad6f5c104d9ce813a1a2f33cac0c5b579ef6ee5"
NEW_MODEL_DIR = Path(__file__).resolve().parent.parent / "inference" / "text" / "models"
NEW_MODEL_QUANTIZED = NEW_MODEL_DIR / "onnx" / "model_quantized.onnx"
NEW_MODEL_FP32 = NEW_MODEL_DIR / "onnx" / "model.onnx"
NEW_TOKENIZER = NEW_MODEL_DIR / "tokenizer.json"


def _single_threaded_session(model_path: Path | str) -> InferenceSession:
    # Single intra/inter-op thread: the accuracy pass doesn't need
    # multiple threads, and the latency pass specifically wants a
    # single-thread measurement (see run_benchmark.py) reflecting the
    # ~0.25-1 vCPU Fargate allocation described in CLAUDE.md, not this dev
    # host's 16 logical cores.
    options = SessionOptions()
    options.intra_op_num_threads = 1
    options.inter_op_num_threads = 1
    return InferenceSession(
        str(model_path), sess_options=options, providers=["CPUExecutionProvider"]
    )


def _sigmoid(x: np.ndarray) -> np.ndarray:
    return 1.0 / (1.0 + np.exp(-x))


@dataclass
class ModelRunner:
    name: str
    model_version: str
    _session: InferenceSession
    _tokenizer: Tokenizer
    _needs_token_type_ids: bool
    _reduce: Callable[[np.ndarray], float]

    def predict(self, text: str) -> float:
        encoding = self._tokenizer.encode(text)
        input_ids = np.array([encoding.ids], dtype=np.int64)
        attention_mask = np.array([encoding.attention_mask], dtype=np.int64)
        feed = {"input_ids": input_ids, "attention_mask": attention_mask}
        if self._needs_token_type_ids:
            feed["token_type_ids"] = np.zeros_like(input_ids)

        (logits,) = self._session.run(None, feed)
        return self._reduce(logits[0])


def load_old_model(cache_dir: Path) -> ModelRunner:
    model_path = hf_hub_download(
        repo_id=OLD_MODEL_REPO,
        filename=OLD_MODEL_FILENAME,
        revision=OLD_MODEL_REVISION,
        local_dir=str(cache_dir),
    )
    tokenizer_path = hf_hub_download(
        repo_id=OLD_MODEL_REPO,
        filename=OLD_TOKENIZER_FILENAME,
        revision=OLD_MODEL_REVISION,
        local_dir=str(cache_dir),
    )
    return ModelRunner(
        name="old (Xenova/toxic-bert, int8)",
        model_version="toxic-bert-onnx-1",
        _session=_single_threaded_session(model_path),
        _tokenizer=Tokenizer.from_file(tokenizer_path),
        _needs_token_type_ids=True,
        _reduce=lambda logits: float(np.max(_sigmoid(logits))),
    )


def load_new_model(*, fp32: bool = False) -> ModelRunner:
    model_path = NEW_MODEL_FP32 if fp32 else NEW_MODEL_QUANTIZED
    if not model_path.exists():
        raise FileNotFoundError(
            f"{model_path} not found. Run the export script first:\n"
            "  cd inference/text && uv sync --group export "
            "&& uv run --group export python scripts/fetch_model.py"
        )
    if not NEW_TOKENIZER.exists():
        raise FileNotFoundError(f"{NEW_TOKENIZER} not found (see above).")

    model_version = (
        "multilingual-toxic-xlm-r-fp32" if fp32 else "multilingual-toxic-xlm-r-onnx-int8-1"
    )
    return ModelRunner(
        name=f"new (multilingual XLM-R, {'fp32' if fp32 else 'int8'})",
        model_version=model_version,
        _session=_single_threaded_session(model_path),
        _tokenizer=Tokenizer.from_file(str(NEW_TOKENIZER)),
        _needs_token_type_ids=False,
        _reduce=lambda logits: float(_sigmoid(logits)[0]),
    )
