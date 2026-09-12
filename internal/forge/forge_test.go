package forge

import (
	"reflect"
	"testing"

	"github.com/bornholm/conclave/internal/domain"
)

func TestIssueReferences(t *testing.T) {
	repo := domain.Repository{Owner: "acme", Name: "proj"}
	text := "Fixes #12, closes acme/proj#34 and resolves other/repo#56.\nFixed: #12 Resolve #78\ntracked separately (#16), see https://x/y#90 and C#5 but not word#7"
	got := IssueReferences(text, repo)
	if want := []int64{12, 34, 78, 16}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}
