package gitrepo

import "testing"

func TestParseRemoteURL(t *testing.T) {
	cases := map[string][3]string{
		"git@github.com:owner/repo.git":                 {"github.com", "owner", "repo"},
		"https://github.com/owner/repo.git":             {"github.com", "owner", "repo"},
		"ssh://git@git.example.com:2222/owner/repo.git": {"git.example.com", "owner", "repo"},
		"https://git.example.com/gitea/owner/repo":      {"git.example.com", "owner", "repo"},
		"HTTPS://GitHub.com/Owner/Repo":                 {"github.com", "Owner", "Repo"},
	}
	for raw, want := range cases {
		got, err := ParseRemoteURL(raw)
		if err != nil {
			t.Errorf("%s: %v", raw, err)
			continue
		}
		if got.Host != want[0] || got.Owner != want[1] || got.Name != want[2] {
			t.Errorf("%s: got %+v", raw, got)
		}
	}
	for _, bad := range []string{"", "https://github.com/repo", "nonsense", "/local/path"} {
		if _, err := ParseRemoteURL(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}
