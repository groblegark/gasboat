// Package reconciler — image digest tracking for automatic pod rolling.
//
// When agent pods use mutable tags (e.g., :nightly), the reconciler can't
// detect image changes by comparing tag strings alone. This file tracks
// registry digests over time: when a tag's digest changes in the registry,
// pods using that tag are flagged for recreation.
//
// Important: we compare registry digests against previous registry digests,
// never against pod-observed digests. Pod ImageID contains a platform-
// specific digest which differs from the manifest list digest returned by
// the registry for multi-arch images.
package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DigestConfirmThreshold is the number of consecutive registry checks that
// must return a new digest before it's accepted. Prevents transient registry
// responses from triggering unnecessary pod recreation.
const DigestConfirmThreshold = 2

// ImageDigestTracker polls the OCI registry and detects when a mutable tag
// (e.g., :nightly) starts pointing to a different image.
//
// It tracks two things per image tag:
//   - deployed: the registry digest at the time pods were last created/updated
//   - current:  the latest registry-confirmed digest
//
// When current != deployed, pods using that tag need recreation.
type ImageDigestTracker struct {
	mu sync.RWMutex

	// deployed is the registry digest that was current when pods were last
	// created or upgraded for this image tag.
	deployed map[string]string

	// current is the latest registry-confirmed digest for each image tag.
	current map[string]string

	// Pending confirmation state: a new candidate digest must be seen
	// DigestConfirmThreshold times before replacing current.
	pendingDigest   map[string]string
	pendingConfirms map[string]int

	logger *slog.Logger
	client *http.Client
}

// NewImageDigestTracker creates a new tracker.
func NewImageDigestTracker(logger *slog.Logger) *ImageDigestTracker {
	return &ImageDigestTracker{
		deployed:        make(map[string]string),
		current:         make(map[string]string),
		pendingDigest:   make(map[string]string),
		pendingConfirms: make(map[string]int),
		logger:          logger,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// HasDrift returns true if the image tag's registry digest has changed since
// pods were last deployed.
func (t *ImageDigestTracker) HasDrift(image string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	deployed := t.deployed[image]
	current := t.current[image]
	return deployed != "" && current != "" && deployed != current
}

// MarkDeployed records that pods for this image are now running the current
// registry digest. Call after pod recreation.
func (t *ImageDigestTracker) MarkDeployed(image string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if cur := t.current[image]; cur != "" {
		t.deployed[image] = cur
	}
}

// Seed records the initial registry digest for an image. Call on startup
// or when a new image tag is first seen. Sets both deployed and current
// to the same value (no drift on first observation).
func (t *ImageDigestTracker) Seed(image, digest string) {
	if digest == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, exists := t.deployed[image]; !exists {
		t.deployed[image] = digest
	}
	if _, exists := t.current[image]; !exists {
		t.current[image] = digest
	}
}

// RecordRegistryDigest records a digest obtained from a registry check.
// A new digest must be confirmed DigestConfirmThreshold times before it
// replaces the current known digest.
func (t *ImageDigestTracker) RecordRegistryDigest(image, digest string) {
	if digest == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	old := t.current[image]
	if old == digest {
		// Same as current — clear any pending candidate.
		delete(t.pendingDigest, image)
		delete(t.pendingConfirms, image)
		return
	}

	// Different digest. Track as pending candidate.
	if t.pendingDigest[image] == digest {
		t.pendingConfirms[image]++
	} else {
		t.pendingDigest[image] = digest
		t.pendingConfirms[image] = 1
	}

	if t.pendingConfirms[image] < DigestConfirmThreshold {
		t.logger.Info("registry digest change detected, awaiting confirmation",
			"image", image,
			"current", truncDigest(old),
			"candidate", truncDigest(digest),
			"confirms", t.pendingConfirms[image],
			"threshold", DigestConfirmThreshold)
		return
	}

	// Confirmed — update current digest.
	t.current[image] = digest
	delete(t.pendingDigest, image)
	delete(t.pendingConfirms, image)
	if old != "" {
		t.logger.Info("registry digest confirmed changed",
			"image", image,
			"old", truncDigest(old),
			"new", truncDigest(digest))
	}
}

// RefreshImages checks the registry for digest updates on all tracked images.
func (t *ImageDigestTracker) RefreshImages(ctx context.Context) {
	t.mu.RLock()
	images := make([]string, 0, len(t.deployed))
	for img := range t.deployed {
		images = append(images, img)
	}
	t.mu.RUnlock()

	for _, img := range images {
		digest, err := t.CheckRegistryDigest(ctx, img)
		if err != nil {
			t.logger.Debug("registry digest check failed",
				"image", img, "error", err)
			continue
		}
		t.RecordRegistryDigest(img, digest)
	}
}

// CheckRegistryDigest queries the OCI registry for the current digest of
// an image tag via the Docker Registry v2 manifest API.
func (t *ImageDigestTracker) CheckRegistryDigest(ctx context.Context, image string) (string, error) {
	repo, tag := parseImageRef(image)
	if repo == "" {
		return "", fmt.Errorf("invalid image reference: %s", image)
	}

	registry, path := splitRegistryPath(repo)
	if registry == "" || path == "" {
		return "", fmt.Errorf("cannot parse registry from image: %s", image)
	}

	token, err := t.getGHCRToken(ctx, path)
	if err != nil {
		return "", fmt.Errorf("getting auth token: %w", err)
	}

	manifestURL := fmt.Sprintf("https://%s/v2/%s/manifests/%s", registry, path, tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, manifestURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
	}, ", "))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("querying manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("manifest query returned %d", resp.StatusCode)
	}

	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		return "", fmt.Errorf("no Docker-Content-Digest header in response")
	}
	return digest, nil
}

// getGHCRToken gets an anonymous pull token from GHCR.
func (t *ImageDigestTracker) getGHCRToken(ctx context.Context, repo string) (string, error) {
	tokenURL := fmt.Sprintf("https://ghcr.io/token?scope=repository:%s:pull", repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)
	if err != nil {
		return "", err
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var tokenResp struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", err
	}
	return tokenResp.Token, nil
}

// parseImageRef splits "ghcr.io/org/repo:tag" into ("ghcr.io/org/repo", "tag").
func parseImageRef(image string) (string, string) {
	if strings.Contains(image, "@") {
		return "", "" // digest refs don't need tracking
	}
	parts := strings.SplitN(image, ":", 2)
	repo := parts[0]
	tag := "latest"
	if len(parts) == 2 {
		tag = parts[1]
	}
	return repo, tag
}

// splitRegistryPath splits "ghcr.io/org/repo" into ("ghcr.io", "org/repo").
func splitRegistryPath(repo string) (string, string) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

// truncDigest returns the first 12 chars of a digest for logging.
func truncDigest(digest string) string {
	d := strings.TrimPrefix(digest, "sha256:")
	if len(d) > 12 {
		return d[:12]
	}
	return d
}
