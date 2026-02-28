package secretreconciler

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"gasboat/controller/internal/beadsapi"
	"gasboat/controller/internal/config"
)

func newTestReconciler(objects ...runtime.Object) (*Reconciler, *dynamicfake.FakeDynamicClient) {
	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			externalSecretGVR: "ExternalSecretList",
		},
		objects...,
	)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	r := New(client, "test-ns", "secretstore", "ClusterSecretStore", "15m", logger)
	return r, client
}

func TestReconcile_CreatesExternalSecret(t *testing.T) {
	r, client := newTestReconciler()

	projects := map[string]config.ProjectCacheEntry{
		"myproject": {
			Secrets: []beadsapi.SecretEntry{
				{Env: "JIRA_EMAIL", Secret: "myproject-jira", Key: "email"},
				{Env: "JIRA_API_TOKEN", Secret: "myproject-jira", Key: "api-token"},
			},
		},
	}

	err := r.Reconcile(context.Background(), projects)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the ExternalSecret was created.
	actions := client.Actions()
	var creates []k8stesting.CreateAction
	for _, a := range actions {
		if ca, ok := a.(k8stesting.CreateAction); ok {
			creates = append(creates, ca)
		}
	}
	if len(creates) != 1 {
		t.Fatalf("expected 1 create action, got %d", len(creates))
	}

	obj := creates[0].GetObject().(*unstructured.Unstructured)
	if obj.GetName() != "myproject-jira" {
		t.Errorf("expected name myproject-jira, got %s", obj.GetName())
	}
	if obj.GetNamespace() != "test-ns" {
		t.Errorf("expected namespace test-ns, got %s", obj.GetNamespace())
	}

	// Check labels.
	labels := obj.GetLabels()
	if labels["gasboat.io/managed-by"] != "controller" {
		t.Errorf("expected managed-by label, got %v", labels)
	}
	if labels["gasboat.io/project"] != "myproject" {
		t.Errorf("expected project label myproject, got %v", labels)
	}

	// Check spec.data has 2 entries.
	spec, ok := obj.Object["spec"].(map[string]interface{})
	if !ok {
		t.Fatal("missing spec")
	}
	data, ok := spec["data"].([]interface{})
	if !ok {
		t.Fatal("missing spec.data")
	}
	if len(data) != 2 {
		t.Errorf("expected 2 data entries, got %d", len(data))
	}

	// Check secretStoreRef.
	storeRef, ok := spec["secretStoreRef"].(map[string]interface{})
	if !ok {
		t.Fatal("missing secretStoreRef")
	}
	if storeRef["name"] != "secretstore" {
		t.Errorf("expected store name secretstore, got %v", storeRef["name"])
	}
	if storeRef["kind"] != "ClusterSecretStore" {
		t.Errorf("expected store kind ClusterSecretStore, got %v", storeRef["kind"])
	}

	// Check remoteRef keys use gasboat/ prefix.
	for _, d := range data {
		entry := d.(map[string]interface{})
		remoteRef := entry["remoteRef"].(map[string]interface{})
		key := remoteRef["key"].(string)
		if key != "gasboat/myproject-jira" {
			t.Errorf("expected remoteRef key gasboat/myproject-jira, got %s", key)
		}
	}
}

func TestReconcile_SkipsExistingExternalSecret(t *testing.T) {
	existing := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "external-secrets.io/v1",
			"kind":       "ExternalSecret",
			"metadata": map[string]interface{}{
				"name":      "myproject-jira",
				"namespace": "test-ns",
			},
		},
	}
	r, client := newTestReconciler(existing)

	projects := map[string]config.ProjectCacheEntry{
		"myproject": {
			Secrets: []beadsapi.SecretEntry{
				{Env: "JIRA_EMAIL", Secret: "myproject-jira", Key: "email"},
			},
		},
	}

	err := r.Reconcile(context.Background(), projects)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should not have created anything (only get, no create).
	for _, a := range client.Actions() {
		if _, ok := a.(k8stesting.CreateAction); ok {
			t.Error("expected no create actions for existing ExternalSecret")
		}
	}
}

func TestReconcile_SkipsInvalidPrefix(t *testing.T) {
	r, client := newTestReconciler()

	projects := map[string]config.ProjectCacheEntry{
		"myproject": {
			Secrets: []beadsapi.SecretEntry{
				{Env: "JIRA_EMAIL", Secret: "pihealth-jira", Key: "email"},
			},
		},
	}

	err := r.Reconcile(context.Background(), projects)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should not have created anything — prefix mismatch.
	for _, a := range client.Actions() {
		if _, ok := a.(k8stesting.CreateAction); ok {
			t.Error("expected no create actions for secret with invalid prefix")
		}
	}
	// Also no get calls since the entry was skipped before checking existence.
	for _, a := range client.Actions() {
		if ga, ok := a.(k8stesting.GetAction); ok {
			t.Errorf("expected no get actions, got get for %s", ga.GetName())
		}
	}
	_ = client // suppress unused
}

func TestReconcile_GroupsMultipleKeysIntoSingleExternalSecret(t *testing.T) {
	r, client := newTestReconciler()

	projects := map[string]config.ProjectCacheEntry{
		"myproject": {
			Secrets: []beadsapi.SecretEntry{
				{Env: "DB_USER", Secret: "myproject-db", Key: "username"},
				{Env: "DB_PASS", Secret: "myproject-db", Key: "password"},
				{Env: "DB_HOST", Secret: "myproject-db", Key: "host"},
			},
		},
	}

	err := r.Reconcile(context.Background(), projects)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have exactly 1 create (grouped into a single ExternalSecret).
	var creates []k8stesting.CreateAction
	for _, a := range client.Actions() {
		if ca, ok := a.(k8stesting.CreateAction); ok {
			creates = append(creates, ca)
		}
	}
	if len(creates) != 1 {
		t.Fatalf("expected 1 create action (grouped), got %d", len(creates))
	}

	obj := creates[0].GetObject().(*unstructured.Unstructured)
	spec := obj.Object["spec"].(map[string]interface{})
	data := spec["data"].([]interface{})
	if len(data) != 3 {
		t.Errorf("expected 3 data entries in grouped ExternalSecret, got %d", len(data))
	}
}

func TestReconcile_DeduplicatesSameKey(t *testing.T) {
	r, client := newTestReconciler()

	// GITLAB_TOKEN and GLAB_TOKEN both reference the same secret with the same key.
	projects := map[string]config.ProjectCacheEntry{
		"myproject": {
			Secrets: []beadsapi.SecretEntry{
				{Env: "GITLAB_TOKEN", Secret: "myproject-gitlab-token", Key: "token"},
				{Env: "GLAB_TOKEN", Secret: "myproject-gitlab-token", Key: "token"},
			},
		},
	}

	err := r.Reconcile(context.Background(), projects)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var creates []k8stesting.CreateAction
	for _, a := range client.Actions() {
		if ca, ok := a.(k8stesting.CreateAction); ok {
			creates = append(creates, ca)
		}
	}
	if len(creates) != 1 {
		t.Fatalf("expected 1 create action, got %d", len(creates))
	}

	obj := creates[0].GetObject().(*unstructured.Unstructured)
	spec := obj.Object["spec"].(map[string]interface{})
	data := spec["data"].([]interface{})
	if len(data) != 1 {
		t.Errorf("expected 1 data entry (deduped), got %d", len(data))
	}
}

func TestReconcile_MultipleProjects(t *testing.T) {
	r, client := newTestReconciler()

	projects := map[string]config.ProjectCacheEntry{
		"alpha": {
			Secrets: []beadsapi.SecretEntry{
				{Env: "TOKEN", Secret: "alpha-creds", Key: "token"},
			},
		},
		"beta": {
			Secrets: []beadsapi.SecretEntry{
				{Env: "TOKEN", Secret: "beta-creds", Key: "token"},
			},
		},
	}

	err := r.Reconcile(context.Background(), projects)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should create 2 ExternalSecrets (one per project).
	var creates []k8stesting.CreateAction
	for _, a := range client.Actions() {
		if ca, ok := a.(k8stesting.CreateAction); ok {
			creates = append(creates, ca)
		}
	}
	if len(creates) != 2 {
		t.Fatalf("expected 2 create actions, got %d", len(creates))
	}
}
