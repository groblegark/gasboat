package beadsapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Comment represents a bead comment.
type Comment struct {
	ID        string    `json:"id"`
	Author    string    `json:"author"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

// AddComment adds a comment to a bead.
func (c *Client) AddComment(ctx context.Context, beadID, author, text string) error {
	body := map[string]string{
		"author": author,
		"text":   text,
	}
	path := "/v1/beads/" + url.PathEscape(beadID) + "/comments"
	if err := c.doJSON(ctx, http.MethodPost, path, body, nil); err != nil {
		return fmt.Errorf("adding comment to %s: %w", beadID, err)
	}
	return nil
}

// GetComments returns all comments on a bead.
func (c *Client) GetComments(ctx context.Context, beadID string) ([]Comment, error) {
	var resp struct {
		Comments []Comment `json:"comments"`
	}
	path := "/v1/beads/" + url.PathEscape(beadID) + "/comments"
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, fmt.Errorf("getting comments for %s: %w", beadID, err)
	}
	return resp.Comments, nil
}
