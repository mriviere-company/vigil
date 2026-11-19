# vigil — API Contract

This document is the source of truth for the vigil API. Changes require updating PLAN.md §3.

## Public Endpoints (Gateway)

### POST /v1/moderate/text
Moderate text content.

**Headers:**
- `X-API-Key: <key>`

**Request Body:**
```json
{
  "content": "string",
  "language": "fr|en (optional)"
}
```

**Response (200 OK):**
```json
{
  "id": "uuid",
  "verdict": "approved|flagged|rejected",
  "score": 0.97,
  "categories": {
    "toxicity": 0.02,
    "spam": 0.01
  },
  "latency_ms": 43
}
```

**Response (202 Accepted) - Degraded Mode (Circuit Open):**
```json
{
  "status": "pending",
  "retry_after": 30
}
```

This same body is also returned (still 202) when the downstream's in-flight
semaphore is saturated, but only if `BACKPRESSURE_MODE=pending`. The default,
`BACKPRESSURE_MODE=reject`, instead returns a 503 `overloaded` error (see the
Normalized Error Body section below) on saturation; the circuit-open case above
always returns 202 regardless of `BACKPRESSURE_MODE`.

**Errors:**
401 / 429 / 400 with normalized error body

### POST /v1/moderate/image
Moderate image content.

**Request Body:**
```json
{
  "image_base64": "..."
}
```
Or multipart form data: a single file field named `image` (`multipart/form-data`,
`Content-Type` header must carry that prefix). The gateway reads the file,
base64-encodes it, and forwards it the same way as the JSON form.

**Response Schema:**
Same as `/v1/moderate/text`, with categories = `{ "nsfw": ..., "violence": ... }`

### GET /v1/health
Gateway health + circuit states.

### GET /metrics
Prometheus metrics endpoint.

**Note:** Not publicly exposed via ALB (internal only).

## Statement of Reasons (DSA Article 17)

**What this is, and what it is not.** vigil is a content-moderation
*detection* tool. It never decides whether content is illegal or breaches a
platform's own terms, and it never takes enforcement action — a platform
integrating vigil remains the addressee of DSA Article 17 and stays
responsible for its own moderation decisions. What vigil adds is: for
every completed moderation decision, a machine-readable **statement-of-reasons
draft** shaped after Article 17 and the EU's Transparency Database schema,
plus a **tamper-evident record** of it in an append-only audit log. This is
evidence to help a platform meet an obligation it already has — it is not a
compliance guarantee, and it does not submit anything to the EU on the
platform's behalf.

**Disclaimer:** vigil does not make a platform "DSA-compliant." It emits
structured artifacts a platform can use, complete, and submit as part of its
*own* Article 17 process. Whether a given verdict, statement, or submission
satisfies Article 17 in a particular case is a legal determination for the
platform (and, where relevant, its counsel) to make — vigil has no view on
that question and asserts none.

### When it exists

A statement of reasons (SoR) is built for every **200** moderation response
(text and image). **202 pending**, any **4xx**, and **429** never produce one,
because vigil did not complete a decision in those cases — there is
nothing yet to give reasons for.

### Where it is exposed

1. **Audit log** — always, when the audit log is enabled (`AUDIT_LOG_PATH`
   set; see below). This is the durable copy.
2. **API response** — only when `SOR_INCLUDE_IN_RESPONSE=true` (default
   `false`). The default keeps the public response contract byte-identical to
   the shape documented above; opting in adds one field:

```json
{
  "id": "uuid",
  "verdict": "rejected",
  "score": 0.97,
  "categories": { "toxicity": 0.97, "spam": 0.01 },
  "latency_ms": 43,
  "statement_of_reasons": {
    "sor_version": "1",
    "decision_id": "uuid",
    "timestamp": "2026-07-11T14:30:00Z",
    "automated_detection": true,
    "automated_decision": "none",
    "content": {
      "type": "text",
      "sha256": "…",
      "size_bytes": 15,
      "language": "fr"
    },
    "detection": {
      "model_version": "multilingual-toxic-xlm-r-onnx-int8-1",
      "scores": { "toxicity": 0.97, "spam": 0.01 },
      "thresholds_in_force": { "flagged": 0.5, "rejected": 0.85 },
      "verdict": "rejected",
      "degraded": false
    },
    "platform_fields": {
      "decision_taken": null,
      "ground": null,
      "ground_reference": null,
      "redress": null
    }
  }
}
```

### Field semantics

| Field | Meaning |
|---|---|
| `sor_version` | Schema version of this artifact (independent of the DSA/EC schema version) |
| `decision_id` | Same value as the response's `id` — joins the two artifacts |
| `timestamp` | RFC3339, UTC — when vigil completed the decision |
| `automated_detection` | Always `true`: this artifact only exists because an automated system flagged something |
| `automated_decision` | Always `"none"`: vigil detects, it never decides or enforces (see the disclaimer above) |
| `content.type` | `"text"` or `"image"` |
| `content.sha256`, `content.size_bytes` | Content-addressable hash and size of the **moderated content only** — never the content itself. This is a hard invariant, not a config option: see the Audit Log section's data-minimization note |
| `content.language` | From the request, when given (text only); `null` otherwise |
| `detection.model_version`, `detection.scores`, `detection.verdict` | What vigil's model observed and concluded |
| `detection.thresholds_in_force` | The `REJECT_THRESHOLD`/`FLAG_THRESHOLD` in effect for *this* decision (they are configurable, so a decision from last month may have used different values) |
| `detection.degraded` | Always `false` — a SoR is only ever built on the 200 success path, never for a degraded (202) response |
| `platform_fields.*` | Explicit **named nulls** — see below |

### `platform_fields`: nulls with names, on purpose

`decision_taken`, `ground`, `ground_reference`, and `redress` are always
present in the JSON with value `null`. vigil cannot populate them — only
the platform, having decided what to actually do about the content, can. The
keys are still there, rather than omitted, so an integrator building on this
artifact sees at a glance exactly what Article 17 still requires *of them*:
what enforcement action was taken, on what ground (`"terms_of_service"` or
`"law"`), a reference to that ground (e.g. a URL to the relevant clause or
statute), and what redress is available to the recipient.

### EC Transparency Database field mapping

Researched against the DSA Transparency Database's public API documentation
(`https://transparency.dsa.ec.europa.eu/page/api-documentation` and
`.../page/additional-explanation-for-statement-attributes`, July 2026) so a
platform mapping this artifact onto a real submission has minimal
translation. **vigil does not submit to this database — that remains the
platform's own integration, out of scope here.**

| vigil field | EC Transparency DB attribute | Mapping notes |
|---|---|---|
| `decision_id` | `puid` | Close to 1:1: `puid` must be alphanumeric/hyphen/underscore, ≤500 chars — vigil's UUIDv4 already satisfies that charset |
| `automated_detection` (bool `true`) | `automated_detection` (`"Yes"`/`"No"`) | 1:1 in meaning; platform translates `true` → `"Yes"` |
| `automated_decision` (`"none"`) | `automated_decision` (`AUTOMATED_DECISION_FULLY` \| `_PARTIALLY` \| `_NOT_AUTOMATED`) | **Not** a value-for-value mapping. vigil never decides, so `"none"` doesn't correspond to one fixed EC value — whether the platform's own downstream action was manual, partly, or fully automated is a fact about the platform's process, not vigil's |
| `content.type` (`"text"`\|`"image"`) | `content_type` (array, e.g. `CONTENT_TYPE_TEXT`) | 1:1 per value; EC's field is an array (supports multiple content types per statement), vigil's is singular (one call, one type) |
| `content.language` | `content_language` (ISO 639-1, **uppercase**) | 1:1 concept; vigil emits lowercase (`"fr"`), EC expects uppercase (`"FR"`) |
| `content.sha256`, `content.size_bytes` | *(no equivalent)* | EC's only content-identifier field, `content_id`, is documented as accepting product identifiers (currently just EAN-13), not a generic content hash. This is a vigil-only, additive field for evidence integrity and data minimization — there is nothing to map it to |
| `detection.model_version`, `detection.scores` | *(no equivalent)* | Signal-level evidence; the EC schema is decision-outcome-oriented, not model-score-oriented. Useful as supporting material for the platform's own free-text `decision_facts` (required, ≤5000 chars), but not itself a schema attribute |
| `platform_fields.decision_taken` | one of `decision_visibility` / `decision_account` / `decision_monetary` / `decision_provision` (EC requires at least one) | Platform-supplied: vigil never enforces, so it has no view on what the platform actually did |
| `platform_fields.ground` | `decision_ground` (`DECISION_GROUND_ILLEGAL_CONTENT` \| `DECISION_GROUND_INCOMPATIBLE_CONTENT`) | Same two-way choice, different spelling (`"law"` / `"terms_of_service"` here); platform translates |
| `platform_fields.ground_reference` | `decision_ground_reference_url` | 1:1 |
| `platform_fields.redress` | *(no equivalent found in the Transparency DB's documented attribute list)* | Article 17(3)(f) of the DSA text requires informing the recipient of redress options in the statement given to *them* — that obligation exists independent of whether the EC's aggregate database ingests a matching field. Kept as a named placeholder for that reason; not verified against exhaustive EC documentation beyond the two pages cited above |
| *(not represented in this artifact)* | `category` (EC **required**); `category_addition`, `category_specification` (optional) | The DSA's substantive category taxonomy (e.g. "illegal or harmful speech", "protection of minors") is the platform's own qualification of *why*, orthogonal to vigil's toxicity/spam/NSFW signal. vigil does not assert a DSA category and has no field for one — this is entirely the platform's to determine and supply |

## Audit Log

An append-only, tamper-evident log of moderation decisions: one JSON line per
decision, hash-chained. The precise integrity guarantee: **modification** of
any hashed field, **reordering**, **interior deletion**, and **prefix
deletion** (removing the oldest records — caught because the first record
must be the `seq` 1 / `prev_hash: "genesis"` anchor) are all detectable from
the file alone. What the file alone can **not** prove is **tail truncation**:
cutting records off the *end* leaves a shorter but internally valid chain,
and no single-file scheme can distinguish "the log simply ends here" from
"the newest records were removed." An operator who needs that property should
record the latest `record_hash` out of band (a high-water-mark — even a line
in a ticket) and compare it against the file's last record. See
`gateway/internal/audit`'s package doc for the full design rationale
(interview material, per this repo's standards); this section is the
operational summary.

**Enable it:** set `AUDIT_LOG_PATH` to a writable file path (default empty =
disabled — no file is created or written to unless this is set).

**Record shape** (one JSON object per line):

```json
{
  "seq": 1,
  "prev_hash": "genesis",
  "timestamp": "2026-07-11T14:30:00Z",
  "request_id": "…",
  "api_key_hash": "…",
  "endpoint": "/v1/moderate/text",
  "statement_of_reasons": { "...": "the full SoR object above" },
  "record_hash": "…"
}
```

- `seq` — monotonic position within this file (gapless, starts at 1).
- `prev_hash` — the previous record's `record_hash`, or the literal string
  `"genesis"` for the first record a given process writes.
- `api_key_hash` — SHA-256 (hex) of the caller's API key. **The raw key never
  appears in the log** — this is a hard invariant enforced in code, not a
  configuration choice.
- `record_hash` — SHA-256 (hex) of the record's canonical form: its own JSON
  encoding via Go's `encoding/json`, computed over every field above *except*
  `record_hash` itself. Determinism relies on two `encoding/json` behaviors
  this project treats as load-bearing: struct fields are always emitted in a
  fixed declaration order, and map keys (the one map-typed field reachable
  here, `detection.scores`) are always emitted in sorted order. See
  `gateway/internal/audit/record.go`'s `canonicalHash` for the exact
  implementation this describes. Note: because the hash covers the canonical
  form rather than the literal bytes on disk, cosmetically reformatting a
  line's JSON (whitespace, key order, added unknown keys) does not break
  verification — the guarantee is meaningful-field integrity, not
  byte-for-byte line integrity.

**No raw content, ever.** A record never contains the moderated text or
image — only `content.sha256` + `content.size_bytes` inside the embedded SoR.
There is no configuration option to log raw content: an audit log able to
retain moderated text or images would itself become a data-retention
liability, which runs against the data-minimization principle this feature
exists to support in the first place. This is a refusal, not a deferred
feature.

**Verify the chain:**

```sh
go run ./cmd/audit-verify <path-to-audit.jsonl>
```

Prints `OK <n> records`, or the first broken link (tampered record, a
`prev_hash`/`seq` mismatch, or a first record that is not the genesis
anchor — the prefix-deletion case) and stops — one edit invalidates every
record after it, so there is nothing further to check once one break is
found. Remember the tail-truncation caveat above: a clean `OK` proves the
records *present* are intact and complete back to genesis, not that no
records were cut off the end.

**Concurrency and loss.** A bounded, capacity-1024 channel feeds a single
writer goroutine (flushing after every record; no `fsync`, so a crash can
still lose the last OS-buffered write — an accepted trade-off against paying
a disk-sync cost on the request-serving path). If the channel is ever full,
the record is **dropped** rather than blocking the moderation request:
availability of the API wins over completeness of the audit trail. Each drop
increments `gateway_audit_dropped_total` and logs an error, so the trade-off's
cost is visible, not silent. A record lost on the write path instead (hash,
marshal, write, or flush failure — e.g. disk full) increments
`gateway_audit_write_errors_total`, kept separate so a failing disk is
distinguishable from load-induced queue overflow. `gateway_audit_records_total`
counts successful appends. Graceful shutdown (`SIGINT`/`SIGTERM`) drains any
already-queued records before the process exits.

**Rotation is out of scope.** Because the log file is opened once per process
and written to by inode (not by re-resolving its path), an external `mv` of
the file is transparent to a running process — its writes keep landing in the
moved file, no signal needed. To start a genuinely fresh file at the
configured path, move the old one and then restart the process: a new process
always begins a fresh chain (`seq` 1, `prev_hash: "genesis"`), so each file is
independently self-verifying and `cmd/audit-verify` never attempts to link
across files.

## Internal Endpoints (Python Inference Services)

### POST /predict
Get prediction scores from the inference service.

**Request Body:**
```json
{
  "content": "string"
}
```
or
```json
{
  "image_base64": "string"
}
```

**Response (200 OK):**
```json
{
  "scores": { ... },
  "model_version": "..."
}
```

### GET /health
Inference service health check.

**Response (200 OK):**
```json
{
  "status": "ok",
  "model_loaded": true
}
```

## Normalized Error Body

Every non-2xx response the gateway produces itself (401, 400, 404, 405, 429,
500, 502, 503) uses this single envelope, so clients parse one error shape and
never see a downstream framework's error body (e.g. FastAPI's `detail`) leak
through:

```json
{
  "error": {
    "code": "<machine_code>",
    "message": "<human-readable>",
    "request_id": "<X-Request-ID of the request>"
  }
}
```

`code` is a stable, machine-readable string from a closed set:

| HTTP status | `code` | When |
|---|---|---|
| 401 | `unauthorized` | Missing or unknown `X-API-Key` |
| 400 | `bad_request` | Invalid request body, or a downstream 4xx (e.g. inference 422 validation) re-normalized — the gateway owns its error surface |
| 429 | `rate_limited` | Per-key req/s + burst exceeded (includes `Retry-After` header) |
| 429 | `quota_exceeded` | Per-key monthly quota exhausted |
| 503 | `overloaded` | Backpressure: the downstream's in-flight semaphore is saturated |
| 502 | `upstream_error` | Downstream 5xx after retries, timeout, or network failure |
| 500 | `internal_error` | Unexpected gateway panic (recovered) |
| 404 | `not_found` | Unknown route |
| 405 | `method_not_allowed` | Wrong method for a known route |

Note: a downstream **4xx** (inference validation) is mapped to **400** with this
body; a downstream **5xx** after retries, a timeout, or a network error becomes
**502**; an **open circuit** returns **202** with the degraded-mode body above
(not this error envelope).

## Verdict Rule (Gateway, Configurable)

- `rejected` if any score > 0.85
- `flagged` if any score > 0.5
- `approved` otherwise

## Rate Limiting Tiers (Multi-Tenancy)

| Tier | Limit | Burst |
|---|---|---|
| free | 1,000 req/month, 2 req/s | 5 |
| pro | 100,000 req/month, 20 req/s | 50 |

API keys are stored in config/env for this project (no DB — assumed, documented choice; replaceable by DynamoDB).

**Documented Limitation:** Without a DB, *monthly* quotas are in-memory counters (reset on restart) — only the rate (req/s + burst) is actually guaranteed.
