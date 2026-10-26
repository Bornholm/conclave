package agent

import (
	"strings"
	"testing"
)

func TestParseCommentReport(t *testing.T) {
	rep, warnings, err := ParseCommentReport([]byte("Here it is:\n"+`{"schema_version":"1","language":"French","body":" Les changements sont approuvés. "}`), 100)
	if err != nil || len(warnings) != 0 || rep.Body != "Les changements sont approuvés." || rep.Language != "French" {
		t.Errorf("rep=%+v warnings=%v err=%v", rep, warnings, err)
	}
	rep, warnings, err = ParseCommentReport([]byte(`{"schema_version":"1","language":"English","body":"`+strings.Repeat("a", 50)+`"}`), 10)
	if err != nil || len(warnings) != 1 || len(rep.Body) > 10+len("…") {
		t.Errorf("cut: rep=%+v warnings=%v err=%v", rep, warnings, err)
	}
	for _, in := range []string{
		`{"schema_version":"1","language":"English","body":"  "}`,
		`{"schema_version":"2","language":"English","body":"x"}`,
		`no json`,
	} {
		if _, _, err := ParseCommentReport([]byte(in), 100); err == nil {
			t.Errorf("%s: accepted", in)
		}
	}
}
