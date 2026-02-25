package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestJiraSync_MRLink(t *testing.T) {
	var (
		mu           sync.Mutex
		commentAdded bool
		linkAdded    bool
		commentBody  string
		linkURL      string
	)

	jiraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "POST" && r.URL.Path == "/rest/api/3/issue/PE-7001/comment":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if doc, ok := body["body"].(map[string]any); ok {
				if content, ok := doc["content"].([]any); ok && len(content) > 0 {
					if para, ok := content[0].(map[string]any); ok {
						if pc, ok := para["content"].([]any); ok && len(pc) > 0 {
							if tn, ok := pc[0].(map[string]any); ok {
								commentBody, _ = tn["text"].(string)
							}
						}
					}
				}
			}
			commentAdded = true
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"1"}`)
		case r.Method == "POST" && r.URL.Path == "/rest/api/3/issue/PE-7001/remotelink":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if obj, ok := body["object"].(map[string]any); ok {
				linkURL, _ = obj["url"].(string)
			}
			linkAdded = true
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":1}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer jiraServer.Close()

	s := NewJiraSync(JiraSyncConfig{Jira: newTestJiraClient(jiraServer.URL), Logger: slog.Default()})
	event := marshalSSEBeadPayload(BeadEvent{
		ID: "bd-task-1", Type: "task", Title: "[PE-7001] Fix upload error",
		Labels: []string{"source:jira", "jira:PE-7001"},
		Fields: map[string]string{"jira_key": "PE-7001", "mr_url": "https://gitlab.com/PiHealth/CoreFICS/monorepo/-/merge_requests/123"},
	})
	s.handleUpdated(context.Background(), event)

	mu.Lock()
	defer mu.Unlock()
	if !commentAdded {
		t.Fatal("expected JIRA comment")
	}
	if commentBody != "Automated MR created: https://gitlab.com/PiHealth/CoreFICS/monorepo/-/merge_requests/123" {
		t.Errorf("comment body: %s", commentBody)
	}
	if !linkAdded {
		t.Fatal("expected JIRA remote link")
	}
	if linkURL != "https://gitlab.com/PiHealth/CoreFICS/monorepo/-/merge_requests/123" {
		t.Errorf("link URL: %s", linkURL)
	}
}

func TestJiraSync_SkipNonJira(t *testing.T) {
	s := NewJiraSync(JiraSyncConfig{Jira: newTestJiraClient("https://example.com"), Logger: slog.Default()})
	event := marshalSSEBeadPayload(BeadEvent{
		ID: "bd-task-2", Type: "task", Title: "Regular task",
		Labels: []string{"source:manual"},
		Fields: map[string]string{"mr_url": "https://gitlab.com/example/mr/1"},
	})
	s.handleUpdated(context.Background(), event)
}

func TestJiraSync_Closed(t *testing.T) {
	var (
		mu              sync.Mutex
		commentAdded    bool
		transitionCalls int
	)
	jiraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "POST" && r.URL.Path == "/rest/api/3/issue/DEVOPS-42/comment":
			commentAdded = true
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"1"}`)
		case r.Method == "GET" && r.URL.Path == "/rest/api/3/issue/DEVOPS-42/transitions":
			resp := map[string]any{"transitions": []map[string]any{
				{"id": "31", "name": "In Progress", "to": map[string]string{"name": "In Progress"}},
				{"id": "41", "name": "Review", "to": map[string]string{"name": "Review"}},
			}}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		case r.Method == "POST" && r.URL.Path == "/rest/api/3/issue/DEVOPS-42/transitions":
			transitionCalls++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer jiraServer.Close()

	s := NewJiraSync(JiraSyncConfig{Jira: newTestJiraClient(jiraServer.URL), Logger: slog.Default()})
	event := marshalSSEBeadPayload(BeadEvent{
		ID: "bd-task-3", Type: "task", Title: "[DEVOPS-42] Fix CI pipeline",
		Labels: []string{"source:jira", "jira:DEVOPS-42"},
		Fields: map[string]string{"jira_key": "DEVOPS-42"},
	})
	s.handleClosed(context.Background(), event)

	mu.Lock()
	defer mu.Unlock()
	if !commentAdded {
		t.Fatal("expected JIRA closing comment")
	}
	if transitionCalls != 1 {
		t.Errorf("expected 1 transition call, got %d", transitionCalls)
	}
}

func TestAdfToMarkdown(t *testing.T) {
	tests := []struct {
		name, input, expected string
	}{
		{"empty", "", ""},
		{"paragraph", `{"version":1,"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Hello world"}]}]}`, "Hello world"},
		{"heading", `{"version":1,"type":"doc","content":[{"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"Title"}]}]}`, "## Title"},
		{"bullet list", `{"version":1,"type":"doc","content":[{"type":"bulletList","content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"Item A"}]}]},{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"Item B"}]}]}]}]}`, "- Item A\n- Item B"},
		{"code block", `{"version":1,"type":"doc","content":[{"type":"codeBlock","attrs":{"language":"go"},"content":[{"type":"text","text":"fmt.Println()"}]}]}`, "```go\nfmt.Println()\n```"},
		{"bold+italic", `{"version":1,"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"bold","marks":[{"type":"strong"}]},{"type":"text","text":" and "},{"type":"text","text":"italic","marks":[{"type":"em"}]}]}]}`, "**bold** and _italic_"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := adfToMarkdown(json.RawMessage(tt.input)); got != tt.expected {
				t.Errorf("adfToMarkdown():\n  got:  %q\n  want: %q", got, tt.expected)
			}
		})
	}
}
