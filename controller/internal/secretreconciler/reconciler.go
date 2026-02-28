// Package secretreconciler reconciles ExternalSecret CRDs from project bead secrets.
//
// When a project bead declares per-project secrets ({env, secret, key} entries),
// this reconciler ensures a matching ExternalSecret CRD exists so that the
// external-secrets-operator provisions the K8s Secret from AWS Secrets Manager.
//
// Naming convention:
//   - K8s Secret name must start with "{project}-" (prefix enforcement)
//   - AWS Secrets Manager path: "gasboat/{k8s-secret-name}"
//
// The reconciler groups multiple env var entries that share the same K8s Secret
// name into a single ExternalSecret with multiple data keys.
package secretreconciler

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"gasboat/controller/internal/config"
)

var externalSecretGVR = schema.GroupVersionResource{
	Group:    "external-secrets.io",
	Version:  "v1",
	Resource: "externalsecrets",
}

// Reconciler ensures ExternalSecret CRDs exist for project bead secrets.
type Reconciler struct {
	dynClient       dynamic.Interface
	namespace       string
	storeName       string
	storeKind       string
	refreshInterval string
	logger          *slog.Logger
}

// New creates a new ExternalSecret reconciler.
func New(dynClient dynamic.Interface, namespace, storeName, storeKind, refreshInterval string, logger *slog.Logger) *Reconciler {
	return &Reconciler{
		dynClient:       dynClient,
		namespace:       namespace,
		storeName:       storeName,
		storeKind:       storeKind,
		refreshInterval: refreshInterval,
		logger:          logger,
	}
}

// secretGroup collects all keys that belong to the same K8s Secret.
type secretGroup struct {
	project    string
	secretName string
	keys       []keyMapping
}

// keyMapping maps an ExternalSecret data entry: secretKey in the K8s Secret
// to a remote ref property in AWS Secrets Manager.
type keyMapping struct {
	secretKey string // key within the K8s Secret (e.g., "email", "api-token")
	property  string // property within the AWS SM secret (same as secretKey)
}

// Reconcile ensures ExternalSecrets exist for all valid project bead secrets.
// It groups secret entries by K8s Secret name, validates prefix naming, and
// creates or updates ExternalSecret CRDs as needed.
func (r *Reconciler) Reconcile(ctx context.Context, projects map[string]config.ProjectCacheEntry) error {
	groups := r.buildSecretGroups(projects)

	client := r.dynClient.Resource(externalSecretGVR).Namespace(r.namespace)

	var errs []error
	for _, g := range groups {
		exists, err := r.externalSecretExists(ctx, client, g.secretName)
		if err != nil {
			errs = append(errs, fmt.Errorf("checking ExternalSecret %s: %w", g.secretName, err))
			continue
		}
		if exists {
			r.logger.Debug("ExternalSecret already exists, skipping",
				"name", g.secretName, "project", g.project)
			continue
		}

		obj := r.buildExternalSecret(g)
		if _, err := client.Create(ctx, obj, metav1.CreateOptions{}); err != nil {
			errs = append(errs, fmt.Errorf("creating ExternalSecret %s: %w", g.secretName, err))
			continue
		}
		r.logger.Info("created ExternalSecret",
			"name", g.secretName, "project", g.project, "keys", len(g.keys))
	}

	if len(errs) > 0 {
		return fmt.Errorf("secret reconciliation had %d errors: %w", len(errs), errs[0])
	}
	return nil
}

// buildSecretGroups groups project bead secrets by K8s Secret name,
// skipping entries that fail prefix validation.
func (r *Reconciler) buildSecretGroups(projects map[string]config.ProjectCacheEntry) []secretGroup {
	// Map: secretName -> secretGroup
	groupMap := make(map[string]*secretGroup)

	for projectName, entry := range projects {
		for _, s := range entry.Secrets {
			if !strings.HasPrefix(s.Secret, projectName+"-") {
				r.logger.Warn("skipping secret with invalid prefix",
					"secret", s.Secret, "project", projectName,
					"expected_prefix", projectName+"-")
				continue
			}

			g, ok := groupMap[s.Secret]
			if !ok {
				g = &secretGroup{
					project:    projectName,
					secretName: s.Secret,
				}
				groupMap[s.Secret] = g
			}
			// Deduplicate keys — multiple env vars may reference the same
			// K8s Secret key (e.g., GITLAB_TOKEN and GLAB_TOKEN both use
			// key "token" from the same secret).
			dup := false
			for _, k := range g.keys {
				if k.secretKey == s.Key {
					dup = true
					break
				}
			}
			if !dup {
				g.keys = append(g.keys, keyMapping{
					secretKey: s.Key,
					property:  s.Key,
				})
			}
		}
	}

	groups := make([]secretGroup, 0, len(groupMap))
	for _, g := range groupMap {
		groups = append(groups, *g)
	}
	return groups
}

// externalSecretExists checks if an ExternalSecret with the given name exists.
func (r *Reconciler) externalSecretExists(ctx context.Context, client dynamic.ResourceInterface, name string) (bool, error) {
	_, err := client.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		// Check if it's a "not found" error.
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// isNotFound returns true if the error is a K8s "not found" error.
func isNotFound(err error) bool {
	// k8s.io/apimachinery/pkg/api/errors would give us a typed check,
	// but we can check the error string to avoid an extra import.
	// The dynamic client returns StatusError with reason NotFound.
	return strings.Contains(err.Error(), "not found") ||
		strings.Contains(err.Error(), "NotFound")
}

// buildExternalSecret constructs an unstructured ExternalSecret CRD.
func (r *Reconciler) buildExternalSecret(g secretGroup) *unstructured.Unstructured {
	// Build data entries for each key mapping.
	data := make([]interface{}, 0, len(g.keys))
	for _, k := range g.keys {
		data = append(data, map[string]interface{}{
			"secretKey": k.secretKey,
			"remoteRef": map[string]interface{}{
				"key":      "gasboat/" + g.secretName,
				"property": k.property,
			},
		})
	}

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "external-secrets.io/v1",
			"kind":       "ExternalSecret",
			"metadata": map[string]interface{}{
				"name":      g.secretName,
				"namespace": r.namespace,
				"labels": map[string]interface{}{
					"gasboat.io/managed-by": "controller",
					"gasboat.io/project":    g.project,
				},
			},
			"spec": map[string]interface{}{
				"refreshInterval": r.refreshInterval,
				"secretStoreRef": map[string]interface{}{
					"kind": r.storeKind,
					"name": r.storeName,
				},
				"target": map[string]interface{}{
					"name":           g.secretName,
					"creationPolicy": "Owner",
				},
				"data": data,
			},
		},
	}
	return obj
}
