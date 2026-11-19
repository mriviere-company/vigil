// Package verdict maps inference scores to a moderation verdict using
// configurable thresholds (REJECT_THRESHOLD, FLAG_THRESHOLD). The rule (from
// docs/api.md): rejected if any score exceeds the reject threshold, flagged if
// any exceeds the flag threshold, otherwise approved. The reported score is the
// maximum across categories — the strongest signal that drove the decision.
package verdict

// Verdict is the moderation outcome.
type Verdict string

const (
	Approved Verdict = "approved"
	Flagged  Verdict = "flagged"
	Rejected Verdict = "rejected"
)

// Classifier applies fixed thresholds to score maps.
type Classifier struct {
	reject float64
	flag   float64
}

// New builds a Classifier. Thresholds are validated by config.Load (flag must
// not exceed reject), so New does not re-check.
func New(rejectThreshold, flagThreshold float64) *Classifier {
	return &Classifier{reject: rejectThreshold, flag: flagThreshold}
}

// Classify returns the verdict and the maximum score across categories. Empty
// scores yield (Approved, 0). Comparisons are strictly greater-than, so a score
// exactly equal to a threshold does not cross it (e.g. exactly 0.85 -> flagged,
// exactly 0.5 -> approved).
func (c *Classifier) Classify(scores map[string]float64) (Verdict, float64) {
	max := 0.0
	for _, v := range scores {
		if v > max {
			max = v
		}
	}
	switch {
	case max > c.reject:
		return Rejected, max
	case max > c.flag:
		return Flagged, max
	default:
		return Approved, max
	}
}
