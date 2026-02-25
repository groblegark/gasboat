package beadsapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// AddLabel adds a label to a bead.
func (c *Client) AddLabel(ctx context.Context, beadID, label string) error {
	body := map[string]string{"label": label}
	path := "/v1/beads/" + url.PathEscape(beadID) + "/labels"
	if err := c.doJSON(ctx, http.MethodPost, path, body, nil); err != nil {
		return fmt.Errorf("adding label %q to %s: %w", label, beadID, err)
	}
	return nil
}

// RemoveLabel removes a label from a bead.
func (c *Client) RemoveLabel(ctx context.Context, beadID, label string) error {
	path := "/v1/beads/" + url.PathEscape(beadID) + "/labels/" + url.PathEscape(label)
	if err := c.doJSON(ctx, http.MethodDelete, path, nil, nil); err != nil {
		return fmt.Errorf("removing label %q from %s: %w", label, beadID, err)
	}
	return nil
}
