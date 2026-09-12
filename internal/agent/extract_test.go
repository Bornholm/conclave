package agent

import (
	"encoding/json"
	"testing"
)

func TestExtractJSON(t *testing.T) {
	want := `{"schema_version":"1","x":1}`
	cases := map[string]string{
		"raw":        want,
		"claude":     `{"type":"result","result":"Sure!\n` + "```json\\n" + `{\"schema_version\":\"1\",\"x\":1}` + "\\n```" + `"}`,
		"structured": `{"type":"result","result":"text","structured_output":` + want + `}`,
		"ndjson":     `{"type":"step_start"}` + "\n" + `{"type":"text","part":"x"}` + "\n" + want + "\n",
		"fenced":     "Here you go:\n```json\n" + want + "\n```\nDone.",
		"prose":      "The report {\"nested\":{\"a\":\"}\"}} then " + want + " end",
		"escaped":    `noise {"schema_version":"1","x":1,"s":"a \"quoted\" }"} tail`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := ExtractJSON([]byte(in), "schema_version")
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal(out, &m); err != nil || m["schema_version"] != "1" {
				t.Errorf("got %s", out)
			}
		})
	}
	for name, in := range map[string]string{"empty": "", "prose": "nothing here", "broken": "{this is not", "other": `{"foo":1}`} {
		if _, err := ExtractJSON([]byte(in), "schema_version"); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
