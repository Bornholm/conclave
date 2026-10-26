package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/bornholm/conclave/internal/domain"
)

// ParseCommentReport decodes the comment a writer returned. A comment with
// no text is an error, one over maxBytes is cut with a warning.
func ParseCommentReport(raw []byte, maxBytes int) (*domain.CommentReport, []string, error) {
	data, err := ExtractJSON(raw, "schema_version")
	if err != nil {
		return nil, nil, err
	}
	var rep domain.CommentReport
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, nil, fmt.Errorf("decode comment: %w", err)
	}
	if rep.SchemaVersion != domain.CommentSchemaVersion {
		return nil, nil, fmt.Errorf("unsupported schema_version %q", rep.SchemaVersion)
	}
	rep.Body = strings.TrimSpace(rep.Body)
	if rep.Body == "" {
		return nil, nil, errors.New("the comment is empty")
	}
	rep.Language = truncate(strings.TrimSpace(rep.Language), 60)
	var warnings []string
	if maxBytes > 0 && len(rep.Body) > maxBytes {
		rep.Body = truncate(rep.Body, maxBytes)
		warnings = append(warnings, fmt.Sprintf("the comment was cut to %d bytes", maxBytes))
	}
	return &rep, warnings, nil
}
