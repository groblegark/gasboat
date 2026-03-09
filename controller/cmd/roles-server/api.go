package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"

	"gasboat/controller/internal/beadsapi"
)

// RolesAPI serves the read-only roles API endpoints.
type RolesAPI struct {
	client *beadsapi.Client
	logger *slog.Logger
}

// NewRolesAPI creates a new roles API handler.
func NewRolesAPI(client *beadsapi.Client, logger *slog.Logger) *RolesAPI {
	return &RolesAPI{client: client, logger: logger}
}

// RegisterRoutes registers API routes on the given mux.
func (a *RolesAPI) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/roles", a.handleListRoles)
	mux.HandleFunc("GET /api/roles/{role}", a.handleGetRole)
	mux.HandleFunc("GET /api/config-beads", a.handleListConfigBeads)
	mux.HandleFunc("GET /api/advice", a.handleListAdvice)
	mux.HandleFunc("GET /api/projects", a.handleListProjects)
}

// roleInfo is the JSON representation of a role.
type roleInfo struct {
	Name         string       `json:"name"`
	ConfigBeads  []configBead `json:"config_beads"`
	AdviceBeads  []adviceBead `json:"advice_beads"`
	AgentCount   int          `json:"agent_count"`
	ActiveAgents []string     `json:"active_agents,omitempty"`
}

// configBead is a simplified config bead for the API response.
type configBead struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Labels []string `json:"labels"`
	Value  any      `json:"value,omitempty"`
}

// adviceBead is a simplified advice bead for the API response.
type adviceBead struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Labels      []string `json:"labels"`
	Description string   `json:"description,omitempty"`
	Status      string   `json:"status"`
}

// handleListRoles lists all known roles derived from config beads, advice beads,
// and active agent beads. A role is any unique "role:X" label found across
// config and advice beads.
func (a *RolesAPI) handleListRoles(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Fetch config beads, advice beads, and agents in parallel.
	var (
		configResult *beadsapi.ListBeadsResult
		adviceResult *beadsapi.ListBeadsResult
		agents       []beadsapi.AgentBead
		configErr    error
		adviceErr    error
		agentsErr    error
		wg           sync.WaitGroup
	)

	wg.Add(3)
	go func() {
		defer wg.Done()
		configResult, configErr = a.client.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
			Types: []string{"config"},
		})
	}()
	go func() {
		defer wg.Done()
		adviceResult, adviceErr = a.client.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
			Types:    []string{"advice"},
			Statuses: []string{"open", "in_progress"},
		})
	}()
	go func() {
		defer wg.Done()
		agents, agentsErr = a.client.ListAgentBeads(ctx)
	}()
	wg.Wait()

	if configErr != nil {
		a.logger.Error("failed to list config beads", "error", configErr)
		writeError(w, http.StatusInternalServerError, "failed to list config beads")
		return
	}
	if adviceErr != nil {
		a.logger.Error("failed to list advice beads", "error", adviceErr)
		writeError(w, http.StatusInternalServerError, "failed to list advice beads")
		return
	}
	if agentsErr != nil {
		a.logger.Error("failed to list agent beads", "error", agentsErr)
		writeError(w, http.StatusInternalServerError, "failed to list agent beads")
		return
	}

	// Extract unique roles from all label sources.
	roles := make(map[string]*roleInfo)
	ensureRole := func(name string) *roleInfo {
		ri, ok := roles[name]
		if !ok {
			ri = &roleInfo{Name: name}
			roles[name] = ri
		}
		return ri
	}

	// Always include "global" as a pseudo-role for global config.
	ensureRole("global")

	for _, b := range configResult.Beads {
		cb := toConfigBead(b)
		for _, label := range b.Labels {
			if name, ok := strings.CutPrefix(label, "role:"); ok {
				ri := ensureRole(name)
				ri.ConfigBeads = append(ri.ConfigBeads, cb)
			}
			if label == "global" {
				ri := ensureRole("global")
				ri.ConfigBeads = append(ri.ConfigBeads, cb)
			}
		}
	}

	for _, b := range adviceResult.Beads {
		ab := toAdviceBead(b)
		for _, label := range b.Labels {
			if name, ok := strings.CutPrefix(label, "role:"); ok {
				ri := ensureRole(name)
				ri.AdviceBeads = append(ri.AdviceBeads, ab)
			}
			if label == "global" {
				ri := ensureRole("global")
				ri.AdviceBeads = append(ri.AdviceBeads, ab)
			}
		}
	}

	for _, ag := range agents {
		if ag.Role != "" {
			ri := ensureRole(ag.Role)
			ri.AgentCount++
			ri.ActiveAgents = append(ri.ActiveAgents, ag.AgentName)
		}
	}

	// Sort roles by name.
	result := make([]roleInfo, 0, len(roles))
	for _, ri := range roles {
		result = append(result, *ri)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == "global" {
			return true
		}
		if result[j].Name == "global" {
			return false
		}
		return result[i].Name < result[j].Name
	})

	writeJSON(w, map[string]any{"roles": result})
}

// handleGetRole returns the detailed configuration for a specific role,
// including its resolved config beads, advice beads, and active agents.
func (a *RolesAPI) handleGetRole(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	roleName := r.PathValue("role")

	var labelFilter []string
	if roleName == "global" {
		labelFilter = []string{"global"}
	} else {
		labelFilter = []string{"role:" + roleName}
	}

	// Fetch config beads, advice beads, and agents in parallel.
	var (
		configResult *beadsapi.ListBeadsResult
		adviceResult *beadsapi.ListBeadsResult
		agents       []beadsapi.AgentBead
		configErr    error
		adviceErr    error
		agentsErr    error
		wg           sync.WaitGroup
	)

	wg.Add(3)
	go func() {
		defer wg.Done()
		configResult, configErr = a.client.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
			Types:  []string{"config"},
			Labels: labelFilter,
		})
	}()
	go func() {
		defer wg.Done()
		adviceResult, adviceErr = a.client.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
			Types:    []string{"advice"},
			Statuses: []string{"open", "in_progress"},
			Labels:   labelFilter,
		})
	}()
	go func() {
		defer wg.Done()
		agents, agentsErr = a.client.ListAgentBeads(ctx)
	}()
	wg.Wait()

	if configErr != nil {
		a.logger.Error("failed to list config beads for role", "role", roleName, "error", configErr)
		writeError(w, http.StatusInternalServerError, "failed to list config beads")
		return
	}
	if adviceErr != nil {
		a.logger.Error("failed to list advice beads for role", "role", roleName, "error", adviceErr)
		writeError(w, http.StatusInternalServerError, "failed to list advice beads")
		return
	}
	if agentsErr != nil {
		a.logger.Error("failed to list agent beads", "error", agentsErr)
		writeError(w, http.StatusInternalServerError, "failed to list agent beads")
		return
	}

	configs := make([]configBead, 0, len(configResult.Beads))
	for _, b := range configResult.Beads {
		configs = append(configs, toConfigBead(b))
	}

	advices := make([]adviceBead, 0, len(adviceResult.Beads))
	for _, b := range adviceResult.Beads {
		advices = append(advices, toAdviceBead(b))
	}

	var activeAgents []string
	for _, ag := range agents {
		if ag.Role == roleName {
			activeAgents = append(activeAgents, ag.AgentName)
		}
	}

	ri := roleInfo{
		Name:         roleName,
		ConfigBeads:  configs,
		AdviceBeads:  advices,
		AgentCount:   len(activeAgents),
		ActiveAgents: activeAgents,
	}

	writeJSON(w, ri)
}

// handleListConfigBeads lists all config beads, optionally filtered by label.
func (a *RolesAPI) handleListConfigBeads(w http.ResponseWriter, r *http.Request) {
	q := beadsapi.ListBeadsQuery{
		Types: []string{"config"},
	}
	if labels := r.URL.Query().Get("labels"); labels != "" {
		q.Labels = strings.Split(labels, ",")
	}

	result, err := a.client.ListBeadsFiltered(r.Context(), q)
	if err != nil {
		a.logger.Error("failed to list config beads", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list config beads")
		return
	}

	beads := make([]configBead, 0, len(result.Beads))
	for _, b := range result.Beads {
		beads = append(beads, toConfigBead(b))
	}

	writeJSON(w, map[string]any{"config_beads": beads, "total": result.Total})
}

// handleListAdvice lists advice beads, optionally filtered by label.
func (a *RolesAPI) handleListAdvice(w http.ResponseWriter, r *http.Request) {
	q := beadsapi.ListBeadsQuery{
		Types:    []string{"advice"},
		Statuses: []string{"open", "in_progress"},
	}
	if labels := r.URL.Query().Get("labels"); labels != "" {
		q.Labels = strings.Split(labels, ",")
	}

	result, err := a.client.ListBeadsFiltered(r.Context(), q)
	if err != nil {
		a.logger.Error("failed to list advice beads", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list advice beads")
		return
	}

	beads := make([]adviceBead, 0, len(result.Beads))
	for _, b := range result.Beads {
		beads = append(beads, toAdviceBead(b))
	}

	writeJSON(w, map[string]any{"advice_beads": beads, "total": result.Total})
}

// handleListProjects lists all registered projects.
func (a *RolesAPI) handleListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := a.client.ListProjectBeads(r.Context())
	if err != nil {
		a.logger.Error("failed to list projects", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list projects")
		return
	}

	writeJSON(w, map[string]any{"projects": projects})
}

// toConfigBead converts a BeadDetail to a configBead response.
func toConfigBead(b *beadsapi.BeadDetail) configBead {
	cb := configBead{
		ID:     b.ID,
		Title:  b.Title,
		Labels: b.Labels,
	}
	if raw, ok := b.Fields["value"]; ok && raw != "" {
		var parsed any
		if json.Unmarshal([]byte(raw), &parsed) == nil {
			cb.Value = parsed
		} else {
			cb.Value = raw
		}
	}
	return cb
}

// toAdviceBead converts a BeadDetail to an adviceBead response.
func toAdviceBead(b *beadsapi.BeadDetail) adviceBead {
	return adviceBead{
		ID:          b.ID,
		Title:       b.Title,
		Labels:      b.Labels,
		Description: b.Description,
		Status:      b.Status,
	}
}

// writeJSON writes a JSON response with status 200.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
