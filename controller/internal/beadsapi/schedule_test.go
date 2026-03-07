package beadsapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListScheduleBeads_ParsesFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("type") != "schedule" {
			t.Errorf("expected type=schedule, got %q", r.URL.Query().Get("type"))
		}
		resp := listBeadsResponse{
			Beads: []beadJSON{
				{
					ID:    "kd-s1",
					Title: "Daily Report",
					Type:  "schedule",
					Fields: mustMarshal(map[string]string{
						"cron":     "0 0 * * *",
						"project":  "gasboat",
						"role":     "crew",
						"prompt":   "Generate daily report",
						"enabled":  "true",
						"timezone": "America/New_York",
					}),
				},
				{
					ID:    "kd-s2",
					Title: "Disabled Schedule",
					Type:  "schedule",
					Fields: mustMarshal(map[string]string{
						"cron":    "0 6 * * *",
						"project": "monorepo",
						"enabled": "false",
					}),
				},
				{
					ID:    "kd-s3",
					Title: "No Cron",
					Type:  "schedule",
					Fields: mustMarshal(map[string]string{
						"project": "gasboat",
						"enabled": "true",
					}),
				},
				{
					ID:    "kd-s4",
					Title: "Default Enabled",
					Type:  "schedule",
					Fields: mustMarshal(map[string]string{
						"cron":    "0 12 * * *",
						"project": "gasboat",
					}),
				},
			},
			Total: 4,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client, err := New(Config{HTTPAddr: srv.URL})
	if err != nil {
		t.Fatal(err)
	}

	beads, err := client.ListScheduleBeads(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	// kd-s3 should be skipped (no cron field).
	if len(beads) != 3 {
		t.Fatalf("expected 3 schedule beads (1 skipped for no cron), got %d", len(beads))
	}

	// First bead.
	if beads[0].ID != "kd-s1" {
		t.Errorf("expected ID kd-s1, got %q", beads[0].ID)
	}
	if beads[0].Cron != "0 0 * * *" {
		t.Errorf("expected cron '0 0 * * *', got %q", beads[0].Cron)
	}
	if beads[0].Project != "gasboat" {
		t.Errorf("expected project 'gasboat', got %q", beads[0].Project)
	}
	if !beads[0].Enabled {
		t.Error("expected bead to be enabled")
	}
	if beads[0].Timezone != "America/New_York" {
		t.Errorf("expected timezone 'America/New_York', got %q", beads[0].Timezone)
	}

	// Second bead: disabled.
	if beads[1].Enabled {
		t.Error("expected second bead to be disabled")
	}

	// Fourth bead (index 2): default enabled (empty enabled field).
	if !beads[2].Enabled {
		t.Error("expected bead with empty enabled field to be enabled by default")
	}
	if beads[2].Role != "crew" {
		t.Errorf("expected default role 'crew', got %q", beads[2].Role)
	}
}

func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
