package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// GitHubClient is a lightweight REST client for the GitHub API.
type GitHubClient struct {
	httpClient *http.Client
	baseURL    string // defaults to "https://api.github.com"
	token      string
	logger     *slog.Logger
}

// RepoRef identifies a GitHub repository.
type RepoRef struct {
	Owner string
	Repo  string
}

func (r RepoRef) String() string { return r.Owner + "/" + r.Repo }

// RepoUnreleased holds the result of checking unreleased commits for a repo.
type RepoUnreleased struct {
	Repo      RepoRef
	LatestTag string
	AheadBy   int
	Commits   []GitCommit
	Error     error
}

// GitCommit is a single commit from the GitHub compare API.
type GitCommit struct {
	SHA     string
	Message string
	Author  string
}

// ghTag is a tag from the GitHub tags API.
type ghTag struct {
	Name string `json:"name"`
}

// ghCompare is the response from the GitHub compare API.
type ghCompare struct {
	AheadBy int `json:"ahead_by"`
	Commits []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message string `json:"message"`
			Author  struct {
				Name string `json:"name"`
			} `json:"author"`
		} `json:"commit"`
	} `json:"commits"`
}

// NewGitHubClient creates a new GitHub REST API client.
// Token is optional; when set it is sent as a Bearer token for higher rate limits.
func NewGitHubClient(token string, logger *slog.Logger) *GitHubClient {
	return &GitHubClient{
		httpClient: &http.Client{Timeout: 15 * time.Second},
		baseURL:    "https://api.github.com",
		token:      token,
		logger:     logger,
	}
}

// GetLatestTag returns the most recent tag for the repo.
func (c *GitHubClient) GetLatestTag(ctx context.Context, repo RepoRef) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/tags?per_page=1", c.baseURL, repo.Owner, repo.Repo)
	var tags []ghTag
	if err := c.doJSON(ctx, url, &tags); err != nil {
		return "", err
	}
	if len(tags) == 0 {
		return "", fmt.Errorf("no tags found for %s", repo)
	}
	return tags[0].Name, nil
}

// CompareTagToHead compares a tag to the head of a branch.
func (c *GitHubClient) CompareTagToHead(ctx context.Context, repo RepoRef, tag, branch string) (*ghCompare, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/compare/%s...%s", c.baseURL, repo.Owner, repo.Repo, tag, branch)
	var result ghCompare
	if err := c.doJSON(ctx, url, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetUnreleased fetches unreleased commits for a repo (tag → branch head).
func (c *GitHubClient) GetUnreleased(ctx context.Context, repo RepoRef, branch string) *RepoUnreleased {
	result := &RepoUnreleased{Repo: repo}

	tag, err := c.GetLatestTag(ctx, repo)
	if err != nil {
		result.Error = fmt.Errorf("get latest tag: %w", err)
		return result
	}
	result.LatestTag = tag

	cmp, err := c.CompareTagToHead(ctx, repo, tag, branch)
	if err != nil {
		result.Error = fmt.Errorf("compare %s...%s: %w", tag, branch, err)
		return result
	}
	result.AheadBy = cmp.AheadBy

	// Cap at 10 commits for Slack block limits.
	limit := len(cmp.Commits)
	if limit > 10 {
		limit = 10
	}
	for _, c := range cmp.Commits[len(cmp.Commits)-limit:] {
		result.Commits = append(result.Commits, GitCommit{
			SHA:     c.SHA,
			Message: c.Commit.Message,
			Author:  c.Commit.Author.Name,
		})
	}
	return result
}

// doJSON performs a GET request and decodes the JSON response.
func (c *GitHubClient) doJSON(ctx context.Context, url string, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read GitHub response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("GitHub API %s returned %d: %s", url, resp.StatusCode, truncate(string(body), 256))
	}

	if err := json.Unmarshal(body, result); err != nil {
		return fmt.Errorf("decode GitHub response: %w", err)
	}
	return nil
}
