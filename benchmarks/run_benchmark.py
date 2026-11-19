"""FR/EN toxicity benchmark: OLD (Xenova/toxic-bert) vs NEW (multilingual
XLM-R, quantized) on matched samples.

This is a kill-switch for the "the multilingual model gives a material
French advantage" strategy bet, not marketing material -- the verdict
thresholds directly below are fixed *before* this script is ever run
against real data, and are not touched again after seeing results. See
benchmarks/README.md for the full methodology and the actual verdict.

Usage (from benchmarks/):
    uv sync
    uv run python run_benchmark.py

Requires the NEW model to already be exported (see models.py's
load_new_model for the exact command if it's missing). Downloads the OLD
model and the FR/EN dataset samples on first run, into benchmarks/data/
(gitignored, never committed -- only the results JSON is).
"""

import json
import platform
import statistics
import time
from dataclasses import asdict
from datetime import UTC, datetime
from pathlib import Path

import numpy as np

from datasets_ import DATASET_LICENSE, DATASET_REPO, DATASET_REVISION, load_language_sample
from metrics import compute_language_metrics
from models import (
    NEW_MODEL_FP32,
    NEW_MODEL_REVISION,
    OLD_MODEL_REPO,
    OLD_MODEL_REVISION,
    ModelRunner,
    load_new_model,
    load_old_model,
)

# --- Fixed BEFORE looking at any results ------------------------------------

SEED = 42
N_PER_LANGUAGE = 1000  # >= 800 required; balanced 500 toxic / 500 non-toxic
LANGUAGES = ("en", "fr")

# Gateway production defaults (docs/api.md, .env.example) -- what a real
# deployer actually experiences, not an arbitrary eval-only cutoff.
FLAG_THRESHOLD = 0.5
REJECT_THRESHOLD = 0.85

# Verdict thresholds -- see benchmarks/README.md "Verdict" for the
# reasoning behind each number.
FR_ADVANTAGE_MIN_AUC_DELTA = 0.05  # NEW must beat OLD on FR by at least this much AUC
EN_PARITY_MAX_AUC_DROP = 0.02  # NEW must not be worse than OLD on EN by more than this
LATENCY_P95_BUDGET_MS = 100.0  # half the 200ms end-to-end budget, leaving
# headroom for gateway auth/rate-limit/circuit-breaker overhead + network hops

LATENCY_WARMUP = 20
LATENCY_SAMPLES = 200

DATA_DIR = Path(__file__).resolve().parent / "data"
RESULTS_PATH = Path(__file__).resolve().parent / "results" / "fr_en_benchmark.json"


def _score_all(model: ModelRunner, texts: list[str]) -> np.ndarray:
    return np.array([model.predict(text) for text in texts])


def _measure_latency(model: ModelRunner, texts: list[str]) -> dict[str, float]:
    """Single-thread mean/p95 wall-clock latency for one `predict()` call.

    Warmup discarded (first call pays session/allocator warmup cost, not
    representative of steady-state serving latency); timed samples cycle
    through `texts` so the measurement reflects realistic, varied-length
    FR/EN inputs rather than one repeated string.
    """
    for _ in range(LATENCY_WARMUP):
        model.predict(texts[0])

    timings_ms = []
    for i in range(LATENCY_SAMPLES):
        text = texts[i % len(texts)]
        start = time.perf_counter()
        model.predict(text)
        timings_ms.append((time.perf_counter() - start) * 1000)

    return {
        "n": len(timings_ms),
        "mean_ms": statistics.mean(timings_ms),
        "p95_ms": float(np.percentile(timings_ms, 95)),
    }


def _cpu_description() -> str:
    try:
        with open("/proc/cpuinfo") as f:
            for line in f:
                if line.startswith("model name"):
                    return line.split(":", 1)[1].strip()
    except OSError:
        pass
    return platform.processor() or platform.machine()


def main() -> None:
    DATA_DIR.mkdir(parents=True, exist_ok=True)
    RESULTS_PATH.parent.mkdir(parents=True, exist_ok=True)

    print(f"loading FR/EN samples ({N_PER_LANGUAGE}/language, seed={SEED})...")
    samples = {
        lang: load_language_sample(lang, N_PER_LANGUAGE, SEED, DATA_DIR) for lang in LANGUAGES
    }

    print("loading OLD model (Xenova/toxic-bert)...")
    old_model = load_old_model(DATA_DIR / "models" / "old")
    print("loading NEW model (multilingual XLM-R, int8 — the file the service loads)...")
    new_model = load_new_model(fp32=False)
    new_model_fp32 = load_new_model(fp32=True) if NEW_MODEL_FP32.exists() else None

    results: dict[str, dict[str, dict]] = {"old": {}, "new": {}}
    for lang in LANGUAGES:
        sample = samples[lang]
        print(f"scoring {lang}: old model over {len(sample.texts)} samples...")
        old_scores = _score_all(old_model, sample.texts)
        print(f"scoring {lang}: new model over {len(sample.texts)} samples...")
        new_scores = _score_all(new_model, sample.texts)

        old_metrics = compute_language_metrics(
            sample.labels, old_scores, FLAG_THRESHOLD, REJECT_THRESHOLD
        )
        new_metrics = compute_language_metrics(
            sample.labels, new_scores, FLAG_THRESHOLD, REJECT_THRESHOLD
        )
        results["old"][lang] = asdict(old_metrics)
        results["new"][lang] = asdict(new_metrics)

    print("measuring latency (single-thread, warmup + timed samples)...")
    all_texts = samples["en"].texts + samples["fr"].texts
    latency = {
        "old_int8": _measure_latency(old_model, all_texts),
        "new_int8": _measure_latency(new_model, all_texts),
    }
    if new_model_fp32 is not None:
        latency["new_fp32"] = _measure_latency(new_model_fp32, all_texts)

    fr_auc_delta = results["new"]["fr"]["roc_auc"] - results["old"]["fr"]["roc_auc"]
    en_auc_delta = results["new"]["en"]["roc_auc"] - results["old"]["en"]["roc_auc"]
    new_p95 = latency["new_int8"]["p95_ms"]

    verdict = {
        "fr_advantage": {
            "delta_auc": fr_auc_delta,
            "threshold": FR_ADVANTAGE_MIN_AUC_DELTA,
            "pass": fr_auc_delta >= FR_ADVANTAGE_MIN_AUC_DELTA,
        },
        "en_parity": {
            "delta_auc": en_auc_delta,
            "threshold": -EN_PARITY_MAX_AUC_DROP,
            "pass": en_auc_delta >= -EN_PARITY_MAX_AUC_DROP,
        },
        "latency_ok": {
            "p95_ms": new_p95,
            "budget_ms": LATENCY_P95_BUDGET_MS,
            "pass": new_p95 <= LATENCY_P95_BUDGET_MS,
        },
    }

    output = {
        "meta": {
            "run_at": datetime.now(UTC).isoformat(),
            "seed": SEED,
            "n_per_language": N_PER_LANGUAGE,
            "cpu": _cpu_description(),
            "dataset": {
                "repo": DATASET_REPO,
                "revision": DATASET_REVISION,
                "license": DATASET_LICENSE,
            },
            "models": {
                "old": {"repo": OLD_MODEL_REPO, "revision": OLD_MODEL_REVISION},
                "new": {
                    "repo": "unitary/multilingual-toxic-xlm-roberta",
                    "revision": NEW_MODEL_REVISION,
                    "quantization": "onnxruntime dynamic quantization, QInt8",
                    "local_file": "inference/text/models/onnx/model_quantized.onnx",
                },
            },
            "thresholds": {
                "flag_threshold": FLAG_THRESHOLD,
                "reject_threshold": REJECT_THRESHOLD,
                "fr_advantage_min_auc_delta": FR_ADVANTAGE_MIN_AUC_DELTA,
                "en_parity_max_auc_drop": EN_PARITY_MAX_AUC_DROP,
                "latency_p95_budget_ms": LATENCY_P95_BUDGET_MS,
            },
        },
        "results": results,
        "latency_ms": latency,
        "verdict": verdict,
    }

    RESULTS_PATH.write_text(json.dumps(output, indent=2) + "\n")
    print(f"\nwrote {RESULTS_PATH}")

    print("\n=== ROC-AUC ===")
    for model_name in ("old", "new"):
        for lang in LANGUAGES:
            print(f"{model_name:>4} / {lang}: {results[model_name][lang]['roc_auc']:.4f}")

    print("\n=== Verdict ===")
    for key, v in verdict.items():
        print(f"{key}: {v}")


if __name__ == "__main__":
    main()
