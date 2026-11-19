// Package sor builds vigil's per-decision statement-of-reasons artifact: a
// machine-readable record of what an automated moderation decision detected,
// shaped after — and field-aligned where sensible with — the EU Digital
// Services Act's Transparency Database statement-of-reasons schema (see
// docs/api.md for the field-by-field mapping and the sources consulted).
//
// Framing (also stated in docs/api.md): vigil is a detection tool. It
// never decides whether content is illegal or breaches a platform's own
// terms, and it never takes enforcement action — a platform integrating
// vigil remains the addressee of Article 17 and stays responsible for its
// own decisions. This package only emits the structured evidence a platform's
// own Article 17 statement would reference, plus explicit named placeholders
// (PlatformFields) for the parts only the platform can supply.
package sor

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// Version is the schema version stamped into every SoR (sor_version). Bump it
// if the shape below changes in a way a consumer would need to branch on.
const Version = "1"

// AutomatedDecisionNone is the fixed value of AutomatedDecision: vigil
// detects but never decides, so this never varies per request. See docs/api.md
// for how this maps onto the EC schema's automated_decision enum (it does not
// map 1:1 — the platform's own process determines that value, not vigil's).
const AutomatedDecisionNone = "none"

// SoR is the statement-of-reasons artifact attached to one completed
// moderation decision. Only built for 200 responses (text + image) — no SoR
// exists for 202 pending, 4xx, or 429 outcomes, because no decision was made
// (see docs/api.md).
type SoR struct {
	SoRVersion string `json:"sor_version"`
	DecisionID string `json:"decision_id"`
	// Timestamp is RFC3339, UTC.
	Timestamp string `json:"timestamp"`
	// AutomatedDetection is always true: this artifact only exists because an
	// automated system flagged something.
	AutomatedDetection bool `json:"automated_detection"`
	// AutomatedDecision is always AutomatedDecisionNone: vigil never
	// enforces; see the constant's doc.
	AutomatedDecision string         `json:"automated_decision"`
	Content           Content        `json:"content"`
	Detection         Detection      `json:"detection"`
	PlatformFields    PlatformFields `json:"platform_fields"`
}

// Content describes the moderated content without ever retaining it: type, a
// content-addressable hash, and size. vigil never stores or logs raw
// request content — sha256+size is the only representation that survives
// past the request (see gateway/internal/audit's package doc for why).
type Content struct {
	Type      string  `json:"type"` // "text" | "image"
	SHA256    string  `json:"sha256"`
	SizeBytes int     `json:"size_bytes"`
	Language  *string `json:"language"` // nullable; from the request, text only
}

// Thresholds are the reject/flag cutoffs in force when the decision was made.
// They are configurable (REJECT_THRESHOLD / FLAG_THRESHOLD), so a decision
// from last month may have used different thresholds than today's — stamping
// them per-decision keeps that auditable.
type Thresholds struct {
	Flagged  float64 `json:"flagged"`
	Rejected float64 `json:"rejected"`
}

// Detection is what vigil's model actually observed.
type Detection struct {
	ModelVersion      string             `json:"model_version"`
	Scores            map[string]float64 `json:"scores"`
	ThresholdsInForce Thresholds         `json:"thresholds_in_force"`
	Verdict           string             `json:"verdict"`
	// Degraded is always false: a SoR is only ever built on the 200 success
	// path (see the SoR doc comment), never for a degraded-mode response.
	Degraded bool `json:"degraded"`
}

// PlatformFields are explicit named nulls: the parts of an Article 17
// statement of reasons that only the platform acting on vigil's verdict
// can supply (what action it actually took, and on what legal/contractual
// ground). Nulls-with-names, rather than field absence, so an integrator sees
// exactly what Article 17 still requires of them — see docs/api.md's mapping
// table for the EC schema fields these correspond to.
type PlatformFields struct {
	DecisionTaken   *string `json:"decision_taken"`
	Ground          *string `json:"ground"` // "terms_of_service" | "law", platform-supplied
	GroundReference *string `json:"ground_reference"`
	Redress         *string `json:"redress"`
}

// Input is what the caller (the gateway's moderation handler) supplies to
// build one SoR.
type Input struct {
	DecisionID   string
	Now          time.Time
	ContentType  string // "text" | "image"
	RawContent   []byte // never retained beyond this call — see Content's doc
	Language     string // "" => Content.Language is null
	ModelVersion string
	Scores       map[string]float64
	Reject       float64
	Flag         float64
	Verdict      string
}

// New builds a SoR for one completed decision. in.DecisionID should be the
// same id returned in the API response's "id" field (docs/api.md) so the two
// artifacts are joinable.
func New(in Input) SoR {
	sum := sha256.Sum256(in.RawContent)

	var language *string
	if in.Language != "" {
		l := in.Language
		language = &l
	}

	return SoR{
		SoRVersion:         Version,
		DecisionID:         in.DecisionID,
		Timestamp:          in.Now.UTC().Format(time.RFC3339),
		AutomatedDetection: true,
		AutomatedDecision:  AutomatedDecisionNone,
		Content: Content{
			Type:      in.ContentType,
			SHA256:    hex.EncodeToString(sum[:]),
			SizeBytes: len(in.RawContent),
			Language:  language,
		},
		Detection: Detection{
			ModelVersion:      in.ModelVersion,
			Scores:            in.Scores,
			ThresholdsInForce: Thresholds{Flagged: in.Flag, Rejected: in.Reject},
			Verdict:           in.Verdict,
			Degraded:          false,
		},
		PlatformFields: PlatformFields{}, // all nil => explicit JSON nulls
	}
}
