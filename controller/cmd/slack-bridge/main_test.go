package main

import (
	"testing"

	"gasboat/controller/internal/bridge"
)

func TestParseRepoList(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []bridge.RepoRef
	}{
		{
			name:  "simple repos",
			input: "org/repo1,org/repo2",
			want: []bridge.RepoRef{
				{Owner: "org", Repo: "repo1"},
				{Owner: "org", Repo: "repo2"},
			},
		},
		{
			name:  "external deps with ~ext suffix",
			input: "org/repo1,org/repo2~ext",
			want: []bridge.RepoRef{
				{Owner: "org", Repo: "repo1"},
				{Owner: "org", Repo: "repo2", External: true},
			},
		},
		{
			name:  "default config",
			input: "groblegark/gasboats,groblegark/kbeads~ext,groblegark/coop~ext",
			want: []bridge.RepoRef{
				{Owner: "groblegark", Repo: "gasboats"},
				{Owner: "groblegark", Repo: "kbeads", External: true},
				{Owner: "groblegark", Repo: "coop", External: true},
			},
		},
		{
			name:  "spaces trimmed",
			input: " org/repo1 , org/repo2~ext ",
			want: []bridge.RepoRef{
				{Owner: "org", Repo: "repo1"},
				{Owner: "org", Repo: "repo2", External: true},
			},
		},
		{
			name:  "empty entries skipped",
			input: "org/repo1,,org/repo2",
			want: []bridge.RepoRef{
				{Owner: "org", Repo: "repo1"},
				{Owner: "org", Repo: "repo2"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRepoList(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d repos, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i].Owner != tt.want[i].Owner {
					t.Errorf("[%d] Owner=%q, want %q", i, got[i].Owner, tt.want[i].Owner)
				}
				if got[i].Repo != tt.want[i].Repo {
					t.Errorf("[%d] Repo=%q, want %q", i, got[i].Repo, tt.want[i].Repo)
				}
				if got[i].External != tt.want[i].External {
					t.Errorf("[%d] External=%v, want %v", i, got[i].External, tt.want[i].External)
				}
			}
		})
	}
}
