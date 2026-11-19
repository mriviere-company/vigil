"""Loads the FR/EN benchmark samples.

Named `datasets_` (trailing underscore) to avoid shadowing the (not used
here, deliberately — see benchmarks/README.md "Dataset choices") HuggingFace
`datasets` library if it's ever added, and to avoid colliding with the
stdlib-adjacent name in editor autocomplete.
"""

from dataclasses import dataclass
from pathlib import Path

import numpy as np
import pyarrow.parquet as pq
from huggingface_hub import hf_hub_download

# textdetox/multilingual_toxicity_dataset: balanced (2.5k toxic / 2.5k
# non-toxic per language) binary toxicity data compiled for the PAN/CLEF
# TextDetox shared task. Pinned to a specific commit so re-running this
# script later reproduces the exact same rows even if upstream changes.
# See benchmarks/README.md for why this was chosen over the alternatives
# investigated (Jigsaw Multilingual's own test set is Kaggle-gated, i.e.
# behind an account+API-token wall; civil_comments is EN-only and part of
# the NEW model's own training data, which would bias an EN-vs-FR
# comparison in the NEW model's favor on the EN side specifically).
DATASET_REPO = "textdetox/multilingual_toxicity_dataset"
DATASET_REVISION = "01907546324b0330d2d8b7669648cc18823323e5"
DATASET_LICENSE = "openrail++"


@dataclass(frozen=True)
class LanguageSample:
    language: str
    texts: list[str]
    labels: np.ndarray  # 0/1, toxic=1


def _download_split(language: str, cache_dir: Path) -> Path:
    return Path(
        hf_hub_download(
            repo_id=DATASET_REPO,
            repo_type="dataset",
            filename=f"data/{language}-00000-of-00001.parquet",
            revision=DATASET_REVISION,
            local_dir=str(cache_dir),
        )
    )


def load_language_sample(
    language: str, n_per_language: int, seed: int, cache_dir: Path
) -> LanguageSample:
    """Returns a seeded, class-balanced sample of `n_per_language` rows.

    Balanced by construction (n//2 toxic + n//2 non-toxic, independently
    sampled then shuffled together) rather than a plain random draw from
    the already-balanced 2.5k/2.5k source split, so the exact balance is
    guaranteed and legible even if a future revision of the source data
    isn't perfectly 50/50.
    """
    path = _download_split(language, cache_dir)
    table = pq.read_table(path)
    texts = table.column("text").to_pylist()
    labels = np.array(table.column("toxic").to_pylist(), dtype=int)

    rng = np.random.default_rng(seed)
    half = n_per_language // 2

    toxic_idx = np.flatnonzero(labels == 1)
    nontoxic_idx = np.flatnonzero(labels == 0)
    if len(toxic_idx) < half or len(nontoxic_idx) < half:
        raise ValueError(
            f"{language}: not enough rows to sample {half}+{half} "
            f"(have {len(toxic_idx)} toxic, {len(nontoxic_idx)} non-toxic)"
        )

    chosen_toxic = rng.choice(toxic_idx, size=half, replace=False)
    chosen_nontoxic = rng.choice(nontoxic_idx, size=half, replace=False)
    chosen = np.concatenate([chosen_toxic, chosen_nontoxic])
    rng.shuffle(chosen)

    return LanguageSample(
        language=language,
        texts=[texts[i] for i in chosen],
        labels=labels[chosen],
    )
