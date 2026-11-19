"""Unit tests for the pure metric helpers — no model, no dataset I/O."""

import numpy as np
import pytest

from metrics import compute_language_metrics, metrics_at_threshold


def test_perfect_separation_gives_auc_of_one() -> None:
    y_true = np.array([0, 0, 1, 1])
    y_score = np.array([0.1, 0.2, 0.8, 0.9])

    metrics = compute_language_metrics(y_true, y_score, flag_threshold=0.5, reject_threshold=0.85)

    assert metrics.roc_auc == pytest.approx(1.0)


def test_random_guessing_gives_auc_near_half() -> None:
    y_true = np.array([0, 1, 0, 1])
    y_score = np.array([0.5, 0.5, 0.5, 0.5])

    metrics = compute_language_metrics(y_true, y_score, flag_threshold=0.5, reject_threshold=0.85)

    assert metrics.roc_auc == pytest.approx(0.5)


def test_metrics_at_threshold_does_not_count_score_equal_to_threshold_as_positive() -> None:
    # Strictly ">", not ">=": a score exactly at the threshold must NOT
    # predict positive, matching the gateway's own boundary exactly
    # (gateway/internal/verdict/verdict.go: "a score exactly equal to a
    # threshold does not cross it").
    y_true = np.array([1, 0])
    y_score = np.array([0.85, 0.10])

    result = metrics_at_threshold(y_true, y_score, threshold=0.85)

    # The one positive-labeled sample sits exactly at the threshold, so it
    # is NOT predicted positive -> the model "fires" on nothing -> recall 0,
    # and (zero_division=0) precision reported as 0.0 too.
    assert result.precision == 0.0
    assert result.recall == 0.0


def test_metrics_at_threshold_counts_score_above_threshold_as_positive() -> None:
    y_true = np.array([1, 0])
    y_score = np.array([0.86, 0.10])

    result = metrics_at_threshold(y_true, y_score, threshold=0.85)

    assert result.precision == pytest.approx(1.0)
    assert result.recall == pytest.approx(1.0)


def test_metrics_at_threshold_handles_no_positive_predictions() -> None:
    # Nothing clears the 0.85 bar: precision must be reported as 0.0
    # instead of raising (sklearn's default divide-by-zero behavior).
    y_true = np.array([1, 0])
    y_score = np.array([0.10, 0.05])

    result = metrics_at_threshold(y_true, y_score, threshold=0.85)

    assert result.precision == 0.0
    assert result.recall == 0.0


def test_f1_at_0_5_is_independent_of_flag_threshold_argument() -> None:
    y_true = np.array([0, 0, 1, 1])
    y_score = np.array([0.3, 0.6, 0.4, 0.9])

    default_flag = compute_language_metrics(
        y_true, y_score, flag_threshold=0.5, reject_threshold=0.85
    )
    custom_flag = compute_language_metrics(
        y_true, y_score, flag_threshold=0.7, reject_threshold=0.85
    )

    assert default_flag.f1_at_0_5 == pytest.approx(custom_flag.f1_at_0_5)
    # But the flag-threshold-specific precision/recall does change.
    assert default_flag.at_flag_threshold.threshold == 0.5
    assert custom_flag.at_flag_threshold.threshold == 0.7


def test_higher_reject_threshold_never_increases_recall() -> None:
    y_true = np.array([1, 1, 1, 0])
    y_score = np.array([0.6, 0.8, 0.95, 0.3])

    at_flag = metrics_at_threshold(y_true, y_score, threshold=0.5)
    at_reject = metrics_at_threshold(y_true, y_score, threshold=0.85)

    assert at_reject.recall <= at_flag.recall
