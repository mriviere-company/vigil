package sor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNewTextWithLanguage(t *testing.T) {
	now := time.Date(2026, 7, 11, 14, 30, 0, 0, time.UTC)
	s := New(Input{
		DecisionID:   "dec-1",
		Now:          now,
		ContentType:  "text",
		RawContent:   []byte("contenu haineux"),
		Language:     "fr",
		ModelVersion: "stub-1",
		Scores:       map[string]float64{"toxicity": 0.95, "spam": 0.05},
		Reject:       0.85,
		Flag:         0.5,
		Verdict:      "rejected",
	})

	if s.SoRVersion != Version {
		t.Errorf("SoRVersion = %q, want %q", s.SoRVersion, Version)
	}
	if s.DecisionID != "dec-1" {
		t.Errorf("DecisionID = %q, want dec-1", s.DecisionID)
	}
	if s.Timestamp != "2026-07-11T14:30:00Z" {
		t.Errorf("Timestamp = %q, want RFC3339 UTC", s.Timestamp)
	}
	if !s.AutomatedDetection {
		t.Error("AutomatedDetection = false, want true (always)")
	}
	if s.AutomatedDecision != AutomatedDecisionNone {
		t.Errorf("AutomatedDecision = %q, want %q", s.AutomatedDecision, AutomatedDecisionNone)
	}

	if s.Content.Type != "text" {
		t.Errorf("Content.Type = %q, want text", s.Content.Type)
	}
	wantSum := sha256.Sum256([]byte("contenu haineux"))
	if s.Content.SHA256 != hex.EncodeToString(wantSum[:]) {
		t.Errorf("Content.SHA256 = %q, want sha256 of raw content", s.Content.SHA256)
	}
	if s.Content.SizeBytes != len("contenu haineux") {
		t.Errorf("Content.SizeBytes = %d, want %d", s.Content.SizeBytes, len("contenu haineux"))
	}
	if s.Content.Language == nil || *s.Content.Language != "fr" {
		t.Errorf("Content.Language = %v, want &\"fr\"", s.Content.Language)
	}

	if s.Detection.ModelVersion != "stub-1" {
		t.Errorf("Detection.ModelVersion = %q", s.Detection.ModelVersion)
	}
	if s.Detection.Scores["toxicity"] != 0.95 || s.Detection.Scores["spam"] != 0.05 {
		t.Errorf("Detection.Scores = %v", s.Detection.Scores)
	}
	if s.Detection.ThresholdsInForce.Flagged != 0.5 || s.Detection.ThresholdsInForce.Rejected != 0.85 {
		t.Errorf("Detection.ThresholdsInForce = %+v", s.Detection.ThresholdsInForce)
	}
	if s.Detection.Verdict != "rejected" {
		t.Errorf("Detection.Verdict = %q", s.Detection.Verdict)
	}
	if s.Detection.Degraded {
		t.Error("Detection.Degraded = true, want false (only built on success path)")
	}

	if s.PlatformFields.DecisionTaken != nil || s.PlatformFields.Ground != nil ||
		s.PlatformFields.GroundReference != nil || s.PlatformFields.Redress != nil {
		t.Errorf("PlatformFields should be all nil (platform must supply), got %+v", s.PlatformFields)
	}
}

func TestNewImageNoLanguage(t *testing.T) {
	s := New(Input{
		DecisionID:  "dec-2",
		Now:         time.Now(),
		ContentType: "image",
		RawContent:  []byte{0xde, 0xad, 0xbe, 0xef},
		Language:    "", // no language concept for images
		Scores:      map[string]float64{"nsfw": 0.1},
		Verdict:     "approved",
	})
	if s.Content.Type != "image" {
		t.Errorf("Content.Type = %q, want image", s.Content.Type)
	}
	if s.Content.Language != nil {
		t.Errorf("Content.Language = %v, want nil for image", s.Content.Language)
	}
	if s.Content.SizeBytes != 4 {
		t.Errorf("Content.SizeBytes = %d, want 4", s.Content.SizeBytes)
	}
}

// TestPlatformFieldsSerializeAsExplicitNulls proves the "nulls-with-names"
// design: the platform_fields keys are present in the JSON with value null,
// never absent, so an integrator sees exactly what Article 17 still requires
// of them.
func TestPlatformFieldsSerializeAsExplicitNulls(t *testing.T) {
	s := New(Input{DecisionID: "d", Now: time.Now(), ContentType: "text", RawContent: []byte("hi"), Verdict: "approved"})
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `"platform_fields":{"decision_taken":null,"ground":null,"ground_reference":null,"redress":null}`
	if !strings.Contains(string(b), want) {
		t.Errorf("json = %s\nmissing %s", b, want)
	}
}

// TestContentLanguageNullJSON proves a missing language serializes as an
// explicit JSON null (not an omitted key), matching the "nullable" contract.
func TestContentLanguageNullJSON(t *testing.T) {
	s := New(Input{DecisionID: "d", Now: time.Now(), ContentType: "image", RawContent: []byte{1, 2}, Verdict: "approved"})
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"language":null`) {
		t.Errorf("json = %s\nmissing language:null", b)
	}
}
