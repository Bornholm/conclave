package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bornholm/conclave/internal/domain"
)

// AnswerLimits bound one answer report.
type AnswerLimits struct {
	// MaxAnswerBytes caps the answer itself, which is a whole document and
	// legitimately larger than any other field.
	MaxAnswerBytes int
	MaxFieldBytes  int
	MaxKeyPoints   int
	MaxReferences  int
}

// DefaultAnswerLimits are used when none are given.
var DefaultAnswerLimits = AnswerLimits{
	MaxAnswerBytes: 64 << 10, MaxFieldBytes: DefaultLimits.MaxFieldBytes,
	MaxKeyPoints: 10, MaxReferences: 50,
}

func (l AnswerLimits) withDefaults() AnswerLimits {
	if l.MaxAnswerBytes <= 0 {
		l.MaxAnswerBytes = DefaultAnswerLimits.MaxAnswerBytes
	}
	if l.MaxFieldBytes <= 0 {
		l.MaxFieldBytes = DefaultAnswerLimits.MaxFieldBytes
	}
	if l.MaxKeyPoints <= 0 {
		l.MaxKeyPoints = DefaultAnswerLimits.MaxKeyPoints
	}
	if l.MaxReferences <= 0 {
		l.MaxReferences = DefaultAnswerLimits.MaxReferences
	}
	return l
}

// ParseAnswerReport decodes and validates the answer one agent gave. An
// answer with no text is an error: everything else in the report describes
// an answer that is not there.
func ParseAnswerReport(raw []byte, agentID string, lim AnswerLimits) (*domain.AnswerReport, []string, error) {
	data, err := ExtractJSON(raw, "schema_version")
	if err != nil {
		return nil, nil, err
	}
	var rep domain.AnswerReport
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, nil, fmt.Errorf("decode answer: %w", err)
	}
	if rep.SchemaVersion != domain.AnswerSchemaVersion {
		return nil, nil, fmt.Errorf("unsupported schema_version %q", rep.SchemaVersion)
	}
	if rep.Reviewer.ID != agentID {
		return nil, nil, fmt.Errorf("reviewer id %q does not match agent %q", rep.Reviewer.ID, agentID)
	}
	if rep.Confidence < 0 || rep.Confidence > 1 {
		return nil, nil, fmt.Errorf("confidence %v out of [0,1]", rep.Confidence)
	}
	lim = lim.withDefaults()

	rep.Answer = truncate(strings.TrimSpace(rep.Answer), lim.MaxAnswerBytes)
	if rep.Answer == "" {
		return nil, nil, fmt.Errorf("the report holds no answer")
	}
	var warnings []string
	rep.KeyPoints, warnings = NormalizeKeyPoints(rep.KeyPoints, lim, "")
	var refWarnings []string
	rep.References, refWarnings = NormalizeAnswerReferences(rep.References, lim, "")
	warnings = append(warnings, refWarnings...)
	rep.Caveats = normalizeLines(rep.Caveats, lim.MaxFieldBytes)
	rep.OpenQuestions = normalizeLines(rep.OpenQuestions, lim.MaxFieldBytes)
	return &rep, warnings, nil
}

// NormalizeKeyPoints trims the key points and caps how many are kept.
func NormalizeKeyPoints(points []string, lim AnswerLimits, prefix string) ([]string, []string) {
	lim = lim.withDefaults()
	var warnings []string
	out := normalizeLines(points, lim.MaxFieldBytes)
	if len(out) > lim.MaxKeyPoints {
		warnings = append(warnings, fmt.Sprintf("%sdropped key points beyond the limit of %d", prefix, lim.MaxKeyPoints))
		out = out[:lim.MaxKeyPoints]
	}
	return out, warnings
}

// NormalizeAnswerReferences trims the references of an answer and caps how
// many are kept. A source stays free text: with a project it is a repository
// path, without one it is a document, a command or a URL, and rejecting what
// does not look like a path would drop the second kind.
func NormalizeAnswerReferences(refs []domain.AnswerReference, lim AnswerLimits, prefix string) ([]domain.AnswerReference, []string) {
	lim = lim.withDefaults()
	var out []domain.AnswerReference
	var warnings []string
	for _, r := range refs {
		if len(out) >= lim.MaxReferences {
			warnings = append(warnings, fmt.Sprintf("%sdropped references beyond the limit of %d", prefix, lim.MaxReferences))
			break
		}
		r.Source = strings.TrimSpace(r.Source)
		r.Note = truncate(strings.TrimSpace(r.Note), lim.MaxFieldBytes)
		if r.Source == "" {
			continue
		}
		r.Source = truncate(r.Source, 512)
		if r.Line < 0 {
			r.Line = 0
		}
		out = append(out, r)
	}
	return out, warnings
}

// NormalizeAnswerLines is the exported form used to validate the lead output.
func NormalizeAnswerLines(lines []string, max int) []string { return normalizeLines(lines, max) }
