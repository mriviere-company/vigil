"""Pure metric functions used by run_benchmark.py.

Kept separate from the orchestration script (data/model I/O) so the
threshold-based precision/recall logic — the part most likely to have an
off-by-one or an inverted comparison — is covered by fast, no-I/O unit
tests (see tests/test_metrics.py).
"""

from dataclasses import dataclass

import numpy as np
from sklearn.metrics import f1_score, precision_score, recall_score, roc_auc_score


@dataclass(frozen=True)
class ThresholdMetrics:
    threshold: float
    precision: float
    recall: float


def metrics_at_threshold(
    y_true: np.ndarray, y_score: np.ndarray, threshold: float
) -> ThresholdMetrics:
    """Precision/recall of "score > threshold" as the positive prediction.

    Strictly greater-than to match the gateway's own verdict logic exactly
    (gateway/internal/verdict/verdict.go: "Comparisons are strictly
    greater-than, so a score exactly equal to a threshold does not cross
    it") — this benchmark should measure precisely the operating points a
    deployer actually experiences, not an approximation of them.

    zero_division=0: if a threshold predicts zero positives (e.g. nothing
    scores > 0.85), precision is reported as 0.0 rather than raising —
    that's a meaningful result (the model never fires at that threshold on
    this sample), not an error.
    """
    y_pred = (y_score > threshold).astype(int)
    return ThresholdMetrics(
        threshold=threshold,
        precision=float(precision_score(y_true, y_pred, zero_division=0)),
        recall=float(recall_score(y_true, y_pred, zero_division=0)),
    )


@dataclass(frozen=True)
class LanguageMetrics:
    roc_auc: float
    f1_at_0_5: float
    at_flag_threshold: ThresholdMetrics  # score > 0.5 ("flagged" or worse)
    at_reject_threshold: ThresholdMetrics  # score > 0.85 ("rejected")


def compute_language_metrics(
    y_true: np.ndarray,
    y_score: np.ndarray,
    flag_threshold: float,
    reject_threshold: float,
) -> LanguageMetrics:
    # F1@0.5 is reported as its own fixed-point metric (independent of
    # flag_threshold, even though the two happen to share the same default
    # value in this system) since it's the conventional "default operating
    # point" metric most toxicity-model comparisons quote. ">" for the same
    # reason as metrics_at_threshold above: matches the gateway exactly.
    y_pred_at_0_5 = (y_score > 0.5).astype(int)
    return LanguageMetrics(
        roc_auc=float(roc_auc_score(y_true, y_score)),
        f1_at_0_5=float(f1_score(y_true, y_pred_at_0_5, zero_division=0)),
        at_flag_threshold=metrics_at_threshold(y_true, y_score, flag_threshold),
        at_reject_threshold=metrics_at_threshold(y_true, y_score, reject_threshold),
    )
