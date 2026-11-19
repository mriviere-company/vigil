# vigil FR/EN Toxicity Benchmark

This benchmark is the kill-switch for one strategy bet: **does swapping the
text service's model from `Xenova/toxic-bert` (English-only) to
`unitary/multilingual-toxic-xlm-roberta` (7 languages) deliver a material
French advantage, without giving up English quality or blowing the
latency budget?**

The verdict thresholds below (§ Verdict) were fixed *before* `run_benchmark.py`
was ever pointed at real data — as a working procedure — and were not
revisited after seeing the numbers. An honesty caveat about that claim
itself: the thresholds and the results landed in the **same commit**, so
"defined before running" is asserted here, not reconstructable from git
history. What *is* independently checkable: two of the three values (FR
advantage ≥ +0.05 AUC, EN parity within −0.02 AUC) are the task brief's own
suggested numbers, written before any benchmark code existed; only the
100ms latency budget is this benchmark's own choice. If the result had been
negative, that would have been reported as the finding — it happens not to
be, and the numbers below are dramatic enough that they are worth being
skeptical of (see § Known biases before taking the delta at face value).

Reproduce with:

```bash
cd benchmarks
uv sync
uv run python run_benchmark.py
```

Requires the NEW model to already be exported (`inference/text/models/onnx/
model_quantized.onnx` + `tokenizer.json` — see `inference/text/scripts/
fetch_model.py`). Everything else (dataset, OLD model) downloads on first
run into `benchmarks/data/` (gitignored). Results are written to
`benchmarks/results/fr_en_benchmark.json` (committed — that file is the
source of truth for every number in this README).

## What is compared

| | OLD | NEW |
|---|---|---|
| Model | `Xenova/toxic-bert` | `unitary/multilingual-toxic-xlm-roberta` |
| Revision | `65cbc260` | `4ad6f5c1` |
| Architecture | BERT-base, 6 toxicity-category logits (max-over-labels) | XLM-R base, 1 "toxic" logit |
| Format | onnxruntime int8 (as shipped by Xenova) | onnxruntime int8 (dynamic-quantized by us, see `inference/text/scripts/fetch_model.py`) |
| Languages trained on | English | English, French, Spanish, Italian, Portuguese, Turkish, Russian |

Both models are run over the **same** 1000 FR samples and the same 1000 EN
samples (seed 42) — a paired comparison, not independently-sampled sets for
each model.

The NEW model is loaded from the exact file the service itself loads
(`inference/text/models/onnx/model_quantized.onnx`), not re-exported or
re-quantized for the benchmark — the artifact under test is the artifact
that ships.

## Dataset choice

**Chosen: `textdetox/multilingual_toxicity_dataset`** (Hub revision
`0190754`), a balanced (2.5k toxic / 2.5k non-toxic per language) binary
toxicity-classification compilation built for the PAN/CLEF **TextDetox**
2024/2025 shared tasks (Dementieva et al., "Overview of the Multilingual
Text Detoxification Task at PAN 2024"). License: **OpenRAIL++** — permits
research/evaluation use (the restriction clauses target specific harmful
downstream *uses* of models/data, not benchmarking); publicly downloadable,
no gating, no account required.

We sample 1000 rows per language (500 toxic / 500 non-toxic, independently
drawn then shuffled, seed 42) from the `en` and `fr` splits — comfortably
above the ≥800/language floor.

**Alternatives investigated and why they were not used:**

- **Jigsaw Multilingual Toxic Comment Classification** (the competition
  brief explicitly suggested) — its real test set (with genuine French
  labels) is Kaggle-only: it requires a Kaggle account + API token to
  download. We searched the HF Hub for a mirror (`jigsaw-multilingual-
  toxic-comment-classification`, `jigsaw multilingual validation`, etc.)
  and found none — this dataset is an auth-walled source, not a freely
  accessible one, so we did not use it. This is worth stating plainly:
  the brief's first suggestion turned out to be inaccessible without a
  Kaggle login, which is exactly the kind of "auth wall" this task asked
  us to check for before silently substituting something else.
- **`google/civil_comments`** (CC0, no gate, EN-only) — accessible, but
  rejected as the EN reference specifically *because* Civil Comments is
  part of the NEW model's own training lineage (the Detoxify `multilingual`
  checkpoint was trained on "Wikipedia Comments + Civil Comments" per its
  model card). Using it as our EN test set would let the NEW model's own
  training data leak into the benchmark's EN column, artificially padding
  the "EN parity" result in the NEW model's favor — precisely the kind of
  cherry-pick this benchmark is supposed to catch, not commit.
- **MLMA** — the accessible HF mirrors we found (`arbml/MLMA_hate_speech_ar`,
  `nedjmaou/MLMA_hate_speech`) are Arabic-focused subsets of the original
  multi-lingual MLMA corpus; we did not find a clean, accessible FR subset
  of comparable size, so it was dropped in favor of textdetox's larger,
  cleaner, and already French-inclusive compilation.

Using the **same** dataset family (same construction methodology, same
per-language size, same annotation era) for both EN and FR is also a
deliberate methodological choice: it isolates "does the model do worse on
French specifically" from "is this FR dataset just harder/easier to
classify than that EN dataset", which mixing dataset families would have
confounded.

## On not cherry-picking

This benchmark was run exactly **twice**, both with seed 42 and the same
1000/language samples, and both are disclosed here: the first run used a
`>=` comparison at the flag/reject thresholds; a self-review before writing
this report caught that the gateway's actual verdict logic
(`gateway/internal/verdict/verdict.go`) uses strict `>`, so `metrics.py` was
corrected and the benchmark re-run. The accuracy numbers below are
**bit-for-bit identical** between the two runs (continuous sigmoid outputs
essentially never land exactly on 0.5 or 0.85, so the `>` vs `>=` distinction
turned out not to matter numerically here) — only the two latency runs
differ, by ordinary run-to-run noise on a shared dev host, not by re-running
until a better number appeared. No sample, seed, threshold, or dataset was
changed after seeing a result.

## Known biases / limitations

- **Partial train/test overlap risk on English.** textdetox's `en` split
  is itself compiled from "Jigsaw" and "Unitary AI Toxicity Dataset"
  sources — the same lineage both models were trained on (toxic-bert on
  Jigsaw's Toxic Comment Classification Challenge; the multilingual model
  on Jigsaw Multilingual + Civil Comments). This likely inflates *both*
  models' absolute EN numbers somewhat (both may have seen similar
  distributions during training), but since it affects both models
  roughly symmetrically it should not bias the **OLD-vs-NEW EN delta**
  much — the EN parity check is a relative comparison, not an absolute
  quality claim.
- **Unattributed French source.** textdetox's own dataset card lists
  sources for every language *except* French (`French: []` — left empty in
  their README). We cannot independently verify whether the FR split
  itself overlaps with the multilingual model's own training data (its
  Jigsaw Multilingual Toxic Comment Classification training data did
  include real French Wikipedia/Civil Comments text). This is the single
  biggest reason to treat the +0.20 AUC delta below as directionally
  correct but not to over-claim the exact magnitude — a genuinely
  held-out French toxicity set might show a smaller (but per the
  precision/recall breakdown below, almost certainly still large) gap.
- **Binary labels only.** The source data is toxic/non-toxic, not the
  richer multi-attribute labels (severe_toxicity, identity_attack, etc.)
  either model can produce — only the single `toxicity`/`toxic` score
  used by the gateway's verdict logic is evaluated here, which matches
  what's actually deployed (`scores.spam` stays 0.0 either way, out of
  scope of this bet).
- **Latency measured on a dev workstation, not Fargate.** See § Latency.

## Metrics

For each (model, language): ROC-AUC (primary — threshold-independent,
matches "how good is the model's toxicity ranking overall"); F1 at the
0.5 cutoff; precision/recall at the two thresholds the gateway actually
uses in production (`docs/api.md`, `.env.example`, strictly `>`, matching
`gateway/internal/verdict/verdict.go` exactly): **flagged** (score > 0.5)
and **rejected** (score > 0.85).

### English

| Model | ROC-AUC | F1@0.5 | P@flag(0.5) | R@flag(0.5) | P@reject(0.85) | R@reject(0.85) |
|---|---|---|---|---|---|---|
| OLD (toxic-bert) | 0.9902 | 0.9537 | 0.9595 | 0.9480 | 0.9904 | 0.8280 |
| NEW (multilingual XLM-R) | 0.9952 | 0.9635 | 0.9774 | 0.9500 | 0.9975 | 0.8060 |
| **Δ (NEW − OLD)** | **+0.0049** | +0.0098 | +0.0179 | +0.0020 | +0.0071 | −0.0220 |

### French

| Model | ROC-AUC | F1@0.5 | P@flag(0.5) | R@flag(0.5) | P@reject(0.85) | R@reject(0.85) |
|---|---|---|---|---|---|---|
| OLD (toxic-bert) | 0.7831 | 0.1405 | 0.9268 | 0.0760 | 0.9500 | 0.0380 |
| NEW (multilingual XLM-R) | 0.9828 | 0.9248 | 0.9672 | 0.8860 | 0.9846 | 0.6380 |
| **Δ (NEW − OLD)** | **+0.1997** | +0.7844 | +0.0404 | +0.8100 | +0.0346 | +0.6000 |

The AUC gap (0.78 vs 0.98) understates how broken the OLD model is in
practice: at the gateway's actual `flagged` threshold, toxic-bert catches
**7.6%** of toxic French content (`R@flag(0.5) = 0.076`) — it isn't "worse
at French", it is functionally blind to it once you look at recall at the
production operating point rather than the threshold-independent AUC.
Precision stays deceptively high (0.93+) throughout for the OLD model on
French simply because it almost never predicts positive at all.

## Latency

Single-thread (`intra_op_num_threads=1`, `inter_op_num_threads=1` —
approximating the ~0.25–1 vCPU Fargate task size in CLAUDE.md rather than
this host's 16 logical cores), 20 warmup calls + 200 timed calls per model,
mixed FR/EN inputs cycled from the same samples used above.

Host: `Intel(R) Core(TM) i9-9900KF CPU @ 3.60GHz` (dev workstation, WSL2)
— **not** the actual Fargate CPU. This is a documented limitation: cloud
vCPU allocations at 0.25–1 vCPU are typically slower and noisier than a
dedicated desktop core, so treat these as a lower bound on real p95, not a
production guarantee.

| Model | mean (ms) | p95 (ms) |
|---|---|---|
| OLD int8 (toxic-bert) | 36.3 | 54.1 |
| NEW int8 (multilingual XLM-R) | 40.0 | 64.8 |
| NEW fp32 (multilingual XLM-R, pre-quantization) | 81.6 | 108.4 |

(Run-to-run variance is visible here: an earlier run of this same script
measured 58.6/63.1/98.8ms respectively for the three rows above — this
dev host is not an isolated benchmarking rig. The qualitative story is
stable across both runs, which is what matters. Note that the commit
message which first introduced these results quotes that earlier run's
latency figures, 63.1/98.8ms, while the committed
`results/fr_en_benchmark.json` holds the second run's, 64.8/108.4ms — the
JSON is authoritative for every number in this README.)

Quantization roughly halves p95 latency for the new model, and on this
run the **fp32** export's p95 (108.4ms) is already *over* the 100ms
single-inference budget defined below — reinforcing that int8 quantization
here is not just a memory-footprint requirement (per CLAUDE.md's 1GB
Fargate task) but a latency one too: shipping fp32 would likely have
failed the latency verdict outright. The quantized NEW model is only
~10ms slower at p95 than the OLD model it replaces — a small, acceptable
cost for a wholesale accuracy jump on French.

## Verdict

Thresholds (defined before running, in `run_benchmark.py`):

1. **Material FR advantage**: NEW's FR ROC-AUC ≥ OLD's FR ROC-AUC + **0.05**.
2. **EN parity**: NEW's EN ROC-AUC ≥ OLD's EN ROC-AUC − **0.02** (i.e. no
   more than a 0.02 AUC regression allowed).
3. **Acceptable latency**: NEW (int8)'s single-inference p95 ≤ **100ms**
   (half the 200ms end-to-end gateway budget in CLAUDE.md, leaving
   headroom for auth/rate-limiting/circuit-breaker/proxy overhead and
   network hops that this single-inference measurement does not include).

| # | Question | Numbers | Result |
|---|---|---|---|
| 1 | Material FR advantage? | ΔAUC = **+0.1997** (≥ 0.05 required) | **YES** |
| 2 | EN parity? | ΔAUC = **+0.0049** (≥ −0.02 required) | **YES** (NEW is actually slightly better on EN, not just non-worse) |
| 3 | Acceptable latency? | p95 = **64.8ms** (≤ 100ms required) | **YES, on dev hardware** — see below |

The three YES results are not equally strong, and it's worth ranking them
honestly:

- **The FR accuracy gain is the validated win.** It is enormous at the
  production operating points (F1@0.5 on French: 0.14 → 0.92), holds under
  the paired-sample design, and would survive even a substantially less
  favorable dataset (see § Known biases on the FR provenance caveat).
- **EN parity holds, with one visible trade-off**: while AUC, F1@0.5 and
  flag-threshold metrics all improve slightly, recall at the *reject*
  threshold regresses from 0.828 to 0.806 (−0.022) — the NEW model is a
  bit more conservative about pushing English toxicity above 0.85, so
  marginally more toxic EN content lands in `flagged` (human-review) rather
  than `rejected` (auto-block). At these thresholds that is a verdict-mix
  shift, not missed detection, and the pre-declared parity criterion (AUC)
  passes with room — but it is a real regression a deployer relying on
  auto-rejection would notice, so it belongs in the verdict, not just the
  table.
- **The latency YES is the softest of the three.** 64.8ms p95 was measured
  single-threaded on an i9-9900KF desktop core, which is materially faster
  per-core than the 0.25–1 vCPU Fargate allocations this service actually
  targets. Read it as "within budget on dev hardware; treat as a lower
  bound, pending a measurement on Fargate" — not as a demonstrated
  production p95. The relative comparisons (int8 ≈ old model + ~10ms; fp32
  ≈ 1.7× int8 and over budget even on this fast host) are the robust part.

**All three criteria pass as defined, and the multilingual-model bet is
validated by this benchmark**: toxic-bert is functionally non-functional on
French at the gateway's actual `flagged`/`rejected` thresholds (7.6%/3.8%
recall), while the multilingual model recovers most of that missed
detection (88.6%/63.8% recall) without sacrificing English quality on this
data. See § Known biases above for the caveats on the FR data's provenance
before treating +0.1997 as an exact, held-out-clean number — the
qualitative conclusion ("worth shipping") is robust to those caveats; the
precise magnitude is not guaranteed to replicate on a fully independent
French test set.
