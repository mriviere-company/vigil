package verdict

import "testing"

func TestClassify(t *testing.T) {
	c := New(0.85, 0.5)

	tests := []struct {
		name        string
		scores      map[string]float64
		wantVerdict Verdict
		wantScore   float64
	}{
		{name: "empty scores approved", scores: map[string]float64{}, wantVerdict: Approved, wantScore: 0},
		{name: "nil scores approved", scores: nil, wantVerdict: Approved, wantScore: 0},
		{name: "low scores approved", scores: map[string]float64{"toxicity": 0.02, "spam": 0.01}, wantVerdict: Approved, wantScore: 0.02},
		{name: "above flag flagged", scores: map[string]float64{"toxicity": 0.6}, wantVerdict: Flagged, wantScore: 0.6},
		{name: "above reject rejected", scores: map[string]float64{"toxicity": 0.95, "spam": 0.05}, wantVerdict: Rejected, wantScore: 0.95},
		{name: "max across categories drives verdict", scores: map[string]float64{"nsfw": 0.1, "violence": 0.9}, wantVerdict: Rejected, wantScore: 0.9},

		// Boundaries: strictly greater-than.
		{name: "exactly reject threshold is flagged", scores: map[string]float64{"toxicity": 0.85}, wantVerdict: Flagged, wantScore: 0.85},
		{name: "exactly flag threshold is approved", scores: map[string]float64{"toxicity": 0.5}, wantVerdict: Approved, wantScore: 0.5},
		{name: "just above reject", scores: map[string]float64{"toxicity": 0.8500001}, wantVerdict: Rejected, wantScore: 0.8500001},
		{name: "just above flag", scores: map[string]float64{"toxicity": 0.5000001}, wantVerdict: Flagged, wantScore: 0.5000001},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotV, gotS := c.Classify(tt.scores)
			if gotV != tt.wantVerdict {
				t.Errorf("verdict = %q, want %q", gotV, tt.wantVerdict)
			}
			if gotS != tt.wantScore {
				t.Errorf("score = %v, want %v", gotS, tt.wantScore)
			}
		})
	}
}

func TestClassifyCustomThresholds(t *testing.T) {
	c := New(0.9, 0.3)
	if v, _ := c.Classify(map[string]float64{"x": 0.4}); v != Flagged {
		t.Errorf("0.4 with flag=0.3 -> %q, want flagged", v)
	}
	if v, _ := c.Classify(map[string]float64{"x": 0.95}); v != Rejected {
		t.Errorf("0.95 with reject=0.9 -> %q, want rejected", v)
	}
}
