package domain

// CheckState is the outcome of one CI check, reduced to what decides a
// review.
type CheckState string

const (
	// CheckSuccess: the check passed, or ended neutral or skipped.
	CheckSuccess CheckState = "success"
	// CheckFailure: the check failed, errored, timed out or was cancelled.
	CheckFailure CheckState = "failure"
	// CheckPending: the check is queued or running, or a required check
	// has not reported yet.
	CheckPending CheckState = "pending"
)

// Check is one CI job or commit status reported on a commit.
type Check struct {
	Name string `json:"name"`
	// Source says where the forge reported it: a check run or a commit
	// status.
	Source string     `json:"source"`
	State  CheckState `json:"state"`
	// Conclusion is the forge's own word for the outcome: failure,
	// timed_out, cancelled, error…
	Conclusion string `json:"conclusion,omitempty"`
	URL        string `json:"url,omitempty"`
	// Summary is what the check said about itself: a check run's title and
	// summary, a commit status's description.
	Summary     string            `json:"summary,omitempty"`
	Annotations []CheckAnnotation `json:"annotations,omitempty"`
	// Blocking says the check decides the verdict: it is not ignored and,
	// when ci.required is set, it is one of them.
	Blocking bool `json:"blocking"`
}

// Check sources.
const (
	CheckSourceRun    = "check_run"
	CheckSourceStatus = "status"
)

// CheckAnnotation is a message a check attached to a line of a file.
type CheckAnnotation struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	Level     string `json:"level,omitempty"`
	Message   string `json:"message"`
}

// CIStatus is the state of the CI on the head a review reviewed.
type CIStatus struct {
	HeadSHA string  `json:"head_sha"`
	Checks  []Check `json:"checks"`
	// Error is set when the checks could not be read: the CI is then
	// unknown, and a review cannot approve on it.
	Error string `json:"error,omitempty"`
}

// State is the state of the blocking checks taken together: a failure
// when one failed, pending when one has not finished, success otherwise,
// no blocking check included.
func (s *CIStatus) State() CheckState {
	if s == nil {
		return CheckSuccess
	}
	if s.Error != "" {
		return CheckPending
	}
	state := CheckSuccess
	for _, c := range s.Checks {
		if !c.Blocking {
			continue
		}
		switch c.State {
		case CheckFailure:
			return CheckFailure
		case CheckPending:
			state = CheckPending
		}
	}
	return state
}

// Blocking returns the blocking checks in the given state.
func (s *CIStatus) Blocking(state CheckState) []Check {
	if s == nil {
		return nil
	}
	var out []Check
	for _, c := range s.Checks {
		if c.Blocking && c.State == state {
			out = append(out, c)
		}
	}
	return out
}
