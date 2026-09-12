package forge

import (
	"reflect"
	"testing"

	"github.com/bornholm/conclave/internal/domain"
)

func TestFilterLabels(t *testing.T) {
	in := []domain.Label{
		{Name: "type/bug", Description: ""},
		{Name: "type/enhancement", Description: "new behaviour"},
		{Name: "area/proxy"},
		{Name: "wontfix"},
		{Name: "good first issue"},
	}
	got := FilterLabels(in, []string{"type/*", "area/*"}, []string{"area/proxy"}, map[string]string{"type/bug": "something is broken"})
	if want := []string{"type/bug", "type/enhancement"}; !reflect.DeepEqual(LabelNames(got), want) {
		t.Fatalf("got %v want %v", LabelNames(got), want)
	}
	if got[0].Description != "something is broken" || got[1].Description != "new behaviour" {
		t.Errorf("descriptions: %+v", got)
	}
	// No include pattern keeps everything but the exclusions.
	all := FilterLabels(in, nil, []string{"wontfix", "good*"}, nil)
	if want := []string{"area/proxy", "type/bug", "type/enhancement"}; !reflect.DeepEqual(LabelNames(all), want) {
		t.Errorf("got %v want %v", LabelNames(all), want)
	}
	if k := KnownLabels(got); k["TYPE/BUG"] != "" || k["type/bug"] != "type/bug" {
		t.Errorf("known: %v", k)
	}
}
