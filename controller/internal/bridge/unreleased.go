package bridge

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
)

// UnreleasedResponse is the JSON response for the /api/unreleased endpoint.
type UnreleasedResponse struct {
	Repos   []UnreleasedRepo        `json:"repos"`
	Cluster *controllerVersionInfo  `json:"cluster,omitempty"`
	Bridge  string                  `json:"bridge"`
}

// UnreleasedRepo holds unreleased commit info for a single repository.
type UnreleasedRepo struct {
	Repo      string           `json:"repo"`
	LatestTag string           `json:"latestTag,omitempty"`
	AheadBy   int              `json:"aheadBy"`
	Commits   []UnreleasedCommit `json:"commits,omitempty"`
	Error     string           `json:"error,omitempty"`
}

// UnreleasedCommit is a single commit in the unreleased response.
type UnreleasedCommit struct {
	SHA     string `json:"sha"`
	Message string `json:"message"`
	Author  string `json:"author"`
}

// UnreleasedConfig holds the dependencies needed to serve unreleased data.
type UnreleasedConfig struct {
	GitHub        *GitHubClient
	Repos         []RepoRef
	ControllerURL string
	Version       string
}

// GetUnreleasedData fetches unreleased commits across all tracked repos and
// the controller version info. It returns a JSON-serializable response.
func GetUnreleasedData(ctx context.Context, cfg UnreleasedConfig) *UnreleasedResponse {
	resp := &UnreleasedResponse{
		Repos:  make([]UnreleasedRepo, len(cfg.Repos)),
		Bridge: cfg.Version,
	}

	var wg sync.WaitGroup

	// Fetch all repos concurrently (requires GitHub client).
	if cfg.GitHub != nil {
		for i, repo := range cfg.Repos {
			wg.Add(1)
			go func(idx int, r RepoRef) {
				defer wg.Done()
				result := cfg.GitHub.GetUnreleased(ctx, r, "main")
				entry := UnreleasedRepo{
					Repo:      r.String(),
					LatestTag: result.LatestTag,
					AheadBy:   result.AheadBy,
				}
				if result.Error != nil {
					entry.Error = result.Error.Error()
				}
				for _, c := range result.Commits {
					entry.Commits = append(entry.Commits, UnreleasedCommit{
						SHA:     c.SHA,
						Message: firstLine(c.Message),
						Author:  c.Author,
					})
				}
				resp.Repos[idx] = entry
			}(i, repo)
		}
	}

	// Fetch controller version concurrently.
	if cfg.ControllerURL != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp.Cluster = fetchControllerVersion(ctx, cfg.ControllerURL)
		}()
	}

	wg.Wait()
	return resp
}

// NewGitHubClientIfConfigured creates a GitHubClient when a token or repos are
// provided. Returns nil otherwise (matching Bot constructor behaviour).
func NewGitHubClientIfConfigured(token string, repos []RepoRef, logger *slog.Logger) *GitHubClient {
	if token != "" || len(repos) > 0 {
		return NewGitHubClient(token, logger)
	}
	return nil
}

// HandleUnreleased returns an http.HandlerFunc that serves unreleased data as JSON.
func HandleUnreleased(cfg UnreleasedConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := GetUnreleasedData(r.Context(), cfg)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(data)
	}
}
