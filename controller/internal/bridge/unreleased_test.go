package bridge

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetUnreleasedData(t *testing.T) {
	ghSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/org/repo1/tags":
			writeJSON(w, []ghTag{{Name: "v1.0.0"}})
		case "/repos/org/repo1/compare/v1.0.0...main":
			writeJSON(w, ghCompare{
				AheadBy: 2,
				Commits: []struct {
					SHA    string `json:"sha"`
					Commit struct {
						Message string `json:"message"`
						Author  struct {
							Name string `json:"name"`
						} `json:"author"`
					} `json:"commit"`
				}{
					{SHA: "aaa1111222233334444", Commit: struct {
						Message string `json:"message"`
						Author  struct {
							Name string `json:"name"`
						} `json:"author"`
					}{Message: "feat: new thing\n\ndetails", Author: struct {
						Name string `json:"name"`
					}{Name: "Alice"}}},
					{SHA: "bbb5555666677778888", Commit: struct {
						Message string `json:"message"`
						Author  struct {
							Name string `json:"name"`
						} `json:"author"`
					}{Message: "fix: old thing", Author: struct {
						Name string `json:"name"`
					}{Name: "Bob"}}},
				},
			})
		case "/repos/org/repo2/tags":
			writeJSON(w, []ghTag{{Name: "v2.0.0"}})
		case "/repos/org/repo2/compare/v2.0.0...main":
			writeJSON(w, ghCompare{AheadBy: 0})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ghSrv.Close()

	client := newTestGitHubClient(ghSrv.URL, "")

	resp := GetUnreleasedData(context.Background(), UnreleasedConfig{
		GitHub: client,
		Repos: []RepoRef{
			{Owner: "org", Repo: "repo1"},
			{Owner: "org", Repo: "repo2"},
		},
		Version: "test-v1",
	})

	if resp.Bridge != "test-v1" {
		t.Errorf("got bridge=%q, want test-v1", resp.Bridge)
	}
	if len(resp.Repos) != 2 {
		t.Fatalf("got %d repos, want 2", len(resp.Repos))
	}

	// repo1: 2 unreleased commits.
	r1 := resp.Repos[0]
	if r1.Repo != "org/repo1" {
		t.Errorf("repo1: got repo=%q, want org/repo1", r1.Repo)
	}
	if r1.AheadBy != 2 {
		t.Errorf("repo1: got aheadBy=%d, want 2", r1.AheadBy)
	}
	if r1.LatestTag != "v1.0.0" {
		t.Errorf("repo1: got latestTag=%q, want v1.0.0", r1.LatestTag)
	}
	if len(r1.Commits) != 2 {
		t.Fatalf("repo1: got %d commits, want 2", len(r1.Commits))
	}
	// firstLine should strip multi-line commit messages.
	if r1.Commits[0].Message != "feat: new thing" {
		t.Errorf("repo1: commit[0] message=%q, want 'feat: new thing'", r1.Commits[0].Message)
	}

	// repo2: up to date.
	r2 := resp.Repos[1]
	if r2.AheadBy != 0 {
		t.Errorf("repo2: got aheadBy=%d, want 0", r2.AheadBy)
	}
}

func TestGetUnreleasedData_NoGitHub(t *testing.T) {
	resp := GetUnreleasedData(context.Background(), UnreleasedConfig{
		Repos:   []RepoRef{{Owner: "org", Repo: "r"}},
		Version: "v0",
	})
	if len(resp.Repos) != 1 {
		t.Fatalf("got %d repos, want 1", len(resp.Repos))
	}
	// With nil GitHub client, repos slice is allocated but empty.
	if resp.Repos[0].AheadBy != 0 {
		t.Errorf("expected zero-value repo entry")
	}
}

func TestGetUnreleasedData_WithControllerVersion(t *testing.T) {
	ctrlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			writeJSON(w, controllerVersionInfo{
				Version:    "v3.0.0",
				Commit:     "deadbeef12345678",
				AgentImage: "agent:latest",
				Namespace:  "gasboat",
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer ctrlSrv.Close()

	resp := GetUnreleasedData(context.Background(), UnreleasedConfig{
		ControllerURL: ctrlSrv.URL,
		Version:       "v1",
	})

	if resp.Cluster == nil {
		t.Fatal("expected cluster info")
	}
	if resp.Cluster.Version != "v3.0.0" {
		t.Errorf("cluster version=%q, want v3.0.0", resp.Cluster.Version)
	}
	if resp.Cluster.Namespace != "gasboat" {
		t.Errorf("cluster namespace=%q, want gasboat", resp.Cluster.Namespace)
	}
}

func TestHandleUnreleased(t *testing.T) {
	ghSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/org/repo/tags":
			writeJSON(w, []ghTag{{Name: "v1.0.0"}})
		case "/repos/org/repo/compare/v1.0.0...main":
			writeJSON(w, ghCompare{AheadBy: 1, Commits: []struct {
				SHA    string `json:"sha"`
				Commit struct {
					Message string `json:"message"`
					Author  struct {
						Name string `json:"name"`
					} `json:"author"`
				} `json:"commit"`
			}{{SHA: "abc1234", Commit: struct {
				Message string `json:"message"`
				Author  struct {
					Name string `json:"name"`
				} `json:"author"`
			}{Message: "fix: thing", Author: struct {
				Name string `json:"name"`
			}{Name: "Dev"}}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ghSrv.Close()

	handler := HandleUnreleased(UnreleasedConfig{
		GitHub:  newTestGitHubClient(ghSrv.URL, ""),
		Repos:   []RepoRef{{Owner: "org", Repo: "repo"}},
		Version: "test",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/unreleased", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type=%q, want application/json", ct)
	}

	var resp UnreleasedResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Bridge != "test" {
		t.Errorf("bridge=%q, want test", resp.Bridge)
	}
	if len(resp.Repos) != 1 {
		t.Fatalf("got %d repos, want 1", len(resp.Repos))
	}
	if resp.Repos[0].AheadBy != 1 {
		t.Errorf("aheadBy=%d, want 1", resp.Repos[0].AheadBy)
	}
}

func TestNewGitHubClientIfConfigured(t *testing.T) {
	logger := slog.Default()

	if c := NewGitHubClientIfConfigured("", nil, logger); c != nil {
		t.Error("expected nil with no token and no repos")
	}
	if c := NewGitHubClientIfConfigured("tok", nil, logger); c == nil {
		t.Error("expected non-nil with token")
	}
	if c := NewGitHubClientIfConfigured("", []RepoRef{{Owner: "o", Repo: "r"}}, logger); c == nil {
		t.Error("expected non-nil with repos")
	}
}
