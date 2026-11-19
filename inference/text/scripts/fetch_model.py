"""Produces the quantized ONNX model + tokenizer used by OnnxModel.

Dev-only: needs the "export" dependency group (optimum+transformers, which
pull in torch), which is *not* installed by a plain `uv sync` — it would
otherwise drag a multi-hundred-MB training stack into the runtime image for
something only needed once, at build/dev time. Runtime stays onnxruntime +
tokenizers only (see [project.dependencies] in pyproject.toml).

Why export instead of a plain download: unlike the previous backend
(Xenova/toxic-bert, which shipped a ready `onnx/model_quantized.onnx` file we
could just hf_hub_download), unitary/multilingual-toxic-xlm-roberta has no
ready-made ONNX export on the Hub — checked both the model's own file list
(pytorch_model.bin only) and the usual onnx-community/Xenova conversion
orgs (neither has mirrored it). Unofficial third-party re-uploads with a
pre-quantized ONNX file do exist (e.g. hoan/multilingual-toxic-xlm-roberta-
dynamic-quantized), but we chose not to trust an unverified third party's
binary weights and quantization process for production model weights;
exporting from the official `unitary` checkpoint ourselves is one extra
step and gives us full provenance and control over the quantization.

MODEL_REVISION is pinned to a specific commit so re-running this script
later reproduces the same weights even if unitary pushes a new revision.

Usage (from inference/text/):
    uv sync --group export
    uv run --group export python scripts/fetch_model.py

Writes into inference/text/models/ (gitignored):
    models/onnx/model.onnx            fp32 export (kept for the benchmark's
                                       int8-vs-fp32 latency/drift note)
    models/onnx/model_quantized.onnx  int8 dynamic-quantized (what the
                                       service actually loads)
    models/tokenizer.json             fast tokenizer (converted from the
                                       original sentencepiece.bpe.model)

Point the app at the result with:
    MODEL_BACKEND=onnx
    MODEL_PATH=models/onnx/model_quantized.onnx
    TOKENIZER_PATH=models/tokenizer.json
"""

import shutil
import tempfile
from pathlib import Path

import numpy as np
from onnxruntime import InferenceSession
from onnxruntime.quantization import QuantType, quantize_dynamic
from tokenizers import Tokenizer

MODEL_ID = "unitary/multilingual-toxic-xlm-roberta"
MODEL_REVISION = "4ad6f5c104d9ce813a1a2f33cac0c5b579ef6ee5"

# Fixed FR/EN toxic + benign sentences used to sanity-check the quantized
# model against the fp32 export it came from. Not a benchmark (see
# benchmarks/ for that) — just a cheap "did quantization break anything"
# smoke check run at export time, every time.
SANITY_SENTENCES = [
    ("EN toxic", "You are a disgusting piece of garbage and everyone hates you."),
    ("EN benign", "I really enjoyed the concert last night, the band was great."),
    ("FR toxic", "T'es vraiment une ordure, je te déteste."),
    ("FR benign", "J'ai adoré le concert hier soir, le groupe était excellent."),
]

# Loose bound on acceptable fp32-vs-int8 drift for the sanity check below.
# Dynamic quantization of a 12-layer transformer typically drifts by low
# single-digit percentage points on sigmoid outputs; anything larger
# suggests the quantized graph is broken, not just "a bit less precise".
MAX_ACCEPTABLE_DRIFT = 0.1


def _sigmoid(x: np.ndarray) -> np.ndarray:
    return 1.0 / (1.0 + np.exp(-x))


def _score(session: InferenceSession, tokenizer: Tokenizer, text: str) -> float:
    encoding = tokenizer.encode(text)
    input_ids = np.array([encoding.ids], dtype=np.int64)
    attention_mask = np.array([encoding.attention_mask], dtype=np.int64)
    (logits,) = session.run(None, {"input_ids": input_ids, "attention_mask": attention_mask})
    return float(_sigmoid(logits[0])[0])


def _export_fp32(staging_dir: Path) -> None:
    """Exports the pytorch checkpoint to ONNX + saves the fast tokenizer.

    Imports optimum/transformers lazily, inside this function, so that
    merely importing this module (e.g. from a test) never requires the
    "export" group to be installed — only actually calling this does.
    """
    from optimum.onnxruntime import ORTModelForSequenceClassification
    from transformers import AutoTokenizer

    print(f"exporting {MODEL_ID}@{MODEL_REVISION} to ONNX (fp32)...")
    model = ORTModelForSequenceClassification.from_pretrained(
        MODEL_ID, revision=MODEL_REVISION, export=True
    )
    model.save_pretrained(staging_dir)

    tokenizer = AutoTokenizer.from_pretrained(MODEL_ID, revision=MODEL_REVISION)
    if not tokenizer.is_fast:
        raise RuntimeError(
            "expected a fast tokenizer (tokenizers-library-backed) so it can "
            "be loaded by the runtime's Tokenizer.from_file without transformers"
        )
    tokenizer.save_pretrained(staging_dir)


def _quantize(fp32_path: Path, int8_path: Path) -> None:
    print("quantizing to int8 (dynamic quantization)...")
    quantize_dynamic(
        model_input=str(fp32_path),
        model_output=str(int8_path),
        weight_type=QuantType.QInt8,
    )


def _run_sanity_check(fp32_path: Path, int8_path: Path, tokenizer_path: Path) -> None:
    tokenizer = Tokenizer.from_file(str(tokenizer_path))
    fp32_session = InferenceSession(str(fp32_path), providers=["CPUExecutionProvider"])
    int8_session = InferenceSession(str(int8_path), providers=["CPUExecutionProvider"])

    print(f"\n{'sample':<10} {'fp32':>8} {'int8':>8} {'|diff|':>8}")
    max_drift = 0.0
    for label, text in SANITY_SENTENCES:
        fp32_score = _score(fp32_session, tokenizer, text)
        int8_score = _score(int8_session, tokenizer, text)
        drift = abs(fp32_score - int8_score)
        max_drift = max(max_drift, drift)
        print(f"{label:<10} {fp32_score:>8.4f} {int8_score:>8.4f} {drift:>8.4f}")

        is_toxic_sample = "toxic" in label and "benign" not in label
        expected_high = is_toxic_sample
        for name, score in (("fp32", fp32_score), ("int8", int8_score)):
            if expected_high and score < 0.5:
                raise RuntimeError(f"{name} model scored a toxic sample low: {label}={score}")
            if not expected_high and score > 0.5:
                raise RuntimeError(f"{name} model scored a benign sample high: {label}={score}")

    print(f"\nmax fp32-vs-int8 drift: {max_drift:.4f}")
    if max_drift > MAX_ACCEPTABLE_DRIFT:
        raise RuntimeError(
            f"fp32-vs-int8 drift {max_drift:.4f} exceeds {MAX_ACCEPTABLE_DRIFT} "
            "-- quantization may have broken the model"
        )


def main() -> None:
    target_dir = Path(__file__).resolve().parent.parent / "models"
    onnx_dir = target_dir / "onnx"
    onnx_dir.mkdir(parents=True, exist_ok=True)

    with tempfile.TemporaryDirectory() as staging:
        staging_dir = Path(staging)
        _export_fp32(staging_dir)

        fp32_path = onnx_dir / "model.onnx"
        shutil.copy(staging_dir / "model.onnx", fp32_path)
        shutil.copy(staging_dir / "tokenizer.json", target_dir / "tokenizer.json")

    int8_path = onnx_dir / "model_quantized.onnx"
    _quantize(fp32_path, int8_path)
    _run_sanity_check(fp32_path, int8_path, target_dir / "tokenizer.json")

    fp32_mb = fp32_path.stat().st_size / 1e6
    int8_mb = int8_path.stat().st_size / 1e6
    print(f"\nfp32:      {fp32_path} ({fp32_mb:.1f} MB)")
    print(f"quantized: {int8_path} ({int8_mb:.1f} MB)")
    print(f"tokenizer: {target_dir / 'tokenizer.json'}")


if __name__ == "__main__":
    main()
