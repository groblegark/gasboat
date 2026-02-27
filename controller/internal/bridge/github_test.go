package bridge

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestGitHubClient creates a GitHubClient pointing at a test server.
func newTestGitHubClient(baseURL, token string) *GitHubClient {
	return &GitHubClient{
		httpClient: http.DefaultClient,
		baseURL:    baseURL,
		token:      token,
		logger:     slog.Default(),
	}
}

func TestGetLatestTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/org/repo/tags" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]ghTag{{Name: "v1.2.3"}})
	}))
	defer srv.Close()

	client := newTestGitHubClient(srv.URL, "")
	tag, err := client.GetLatestTag(context.Background(), RepoRef{Owner: "org", Repo: "repo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tag != "v1.2.3" {
		t.Errorf("got tag %q, want v1.2.3", tag)
	}
}

func TestGetLatestTag_NoTags(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]ghTag{})
	}))
	defer srv.Close()

	client := newTestGitHubClient(srv.URL, "")
	_, err := client.GetLatestTag(context.Background(), RepoRef{Owner: "org", Repo: "repo"})
	if err == nil {
		t.Fatal("expected error for no tags")
	}
}

func TestCompareTagToHead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/org/repo/compare/v1.0.0...main" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ghCompare{
			AheadBy: 3,
			Commits: []struct {
				SHA    string `json:"sha"`
				Commit struct {
					Message string `json:"message"`
					Author  struct {
						Name string `json:"name"`
					} `json:"author"`
				} `json:"commit"`
			}{
				{SHA: "abc1234567890", Commit: struct {
					Message string `json:"message"`
					Author  struct {
						Name string `json:"name"`
					} `json:"author"`
				}{Message: "fix: bug", Author: struct {
					Name string `json:"name"`
				}{Name: "Alice"}}},
			},
		})
	}))
	defer srv.Close()

	client := newTestGitHubClient(srv.URL, "")
	cmp, err := client.CompareTagToHead(context.Background(), RepoRef{Owner: "org", Repo: "repo"}, "v1.0.0", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmp.AheadBy != 3 {
		t.Errorf("got AheadBy=%d, want 3", cmp.AheadBy)
	}
	if len(cmp.Commits) != 1 {
		t.Errorf("got %d commits, want 1", len(cmp.Commits))
	}
}

func TestCompareTagToHead_Identical(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ghCompare{AheadBy: 0})
	}))
	defer srv.Close()

	client := newTestGitHubClient(srv.URL, "")
	cmp, err := client.CompareTagToHead(context.Background(), RepoRef{Owner: "org", Repo: "repo"}, "v1.0.0", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmp.AheadBy != 0 {
		t.Errorf("got AheadBy=%d, want 0", cmp.AheadBy)
	}
}

func TestGetUnreleased(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/org/repo/tags":
			json.NewEncoder(w).Encode([]ghTag{{Name: "v2.0.0"}})
		case "/repos/org/repo/compare/v2.0.0...main":
			json.NewEncoder(w).Encode(ghCompare{
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
					{SHA: "aaa1111", Commit: struct {
						Message string `json:"message"`
						Author  struct {
							Name string `json:"name"`
						} `json:"author"`
					}{Message: "feat: new thing", Author: struct {
						Name string `json:"name"`
					}{Name: "Bob"}}},
					{SHA: "bbb2222", Commit: struct {
						Message string `json:"message"`
						Author  struct {
							Name string `json:"name"`
						} `json:"author"`
					}{Message: "fix: old thing", Author: struct {
						Name string `json:"name"`
					}{Name: "Carol"}}},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := newTestGitHubClient(srv.URL, "")
	result := client.GetUnreleased(context.Background(), RepoRef{Owner: "org", Repo: "repo"}, "main")

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.LatestTag != "v2.0.0" {
		t.Errorf("got tag %q, want v2.0.0", result.LatestTag)
	}
	if result.AheadBy != 2 {
		t.Errorf("got AheadBy=%d, want 2", result.AheadBy)
	}
	if len(result.Commits) != 2 {
		t.Errorf("got %d commits, want 2", len(result.Commits))
	}
}

func TestAuthHeader(t *testing.T) {
	t.Run("with token", func(t *testing.T) {
		var gotAuth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]ghTag{{Name: "v1.0.0"}})
		}))
		defer srv.Close()

		client := newTestGitHubClient(srv.URL, "ghp_secret123")
		_, _ = client.GetLatestTag(context.Background(), RepoRef{Owner: "o", Repo: "r"})

		if gotAuth != "Bearer ghp_secret123" {
			t.Errorf("got Authorization=%q, want Bearer ghp_secret123", gotAuth)
		}
	})

	t.Run("without token", func(t *testing.T) {
		var gotAuth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]ghTag{{Name: "v1.0.0"}})
		}))
		defer srv.Close()

		client := newTestGitHubClient(srv.URL, "")
		_, _ = client.GetLatestTag(context.Background(), RepoRef{Owner: "o", Repo: "r"})

		if gotAuth != "" {
			t.Errorf("got Authorization=%q, want empty", gotAuth)
		}
	})
}

func TestShortSHA(t *testing.T) {
	if got := shortSHA("abc1234567890"); got != "abc1234" {
		t.Errorf("shortSHA(long) = %q, want abc1234", got)
	}
	if got := shortSHA("abc"); got != "abc" {
		t.Errorf("shortSHA(short) = %q, want abc", got)
	}
}

func TestFirstLine(t *testing.T) {
	if got := firstLine("line1\nline2"); got != "line1" {
		t.Errorf("firstLine(multi) = %q, want line1", got)
	}
	if got := firstLine("single"); got != "single" {
		t.Errorf("firstLine(single) = %q, want single", got)
	}
}
