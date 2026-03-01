package bridge

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"gasboat/controller/internal/beadsapi"
)

// DecisionAPIClient is the subset of beadsapi.Client used by the decisions web API.
type DecisionAPIClient interface {
	ListDecisions(ctx context.Context, status string, limit int) ([]beadsapi.DecisionDetail, error)
	GetDecision(ctx context.Context, decisionID string) (*beadsapi.DecisionDetail, error)
	ResolveDecision(ctx context.Context, decisionID string, req beadsapi.ResolveDecisionRequest) error
	CancelDecision(ctx context.Context, decisionID, reason, canceledBy string) error
}

// DecisionAPI serves the decisions web UI API endpoints.
type DecisionAPI struct {
	client DecisionAPIClient
	logger *slog.Logger
}

// NewDecisionAPI creates the decisions API handler.
func NewDecisionAPI(client DecisionAPIClient, logger *slog.Logger) *DecisionAPI {
	return &DecisionAPI{client: client, logger: logger}
}

// RegisterRoutes registers decision API routes on the given mux.
func (a *DecisionAPI) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/decisions", a.handleList)
	mux.HandleFunc("/api/decisions/", a.handleByID)
}

// handleList handles GET /api/decisions.
func (a *DecisionAPI) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	status := r.URL.Query().Get("status")
	decisions, err := a.client.ListDecisions(r.Context(), status, 50)
	if err != nil {
		a.logger.Error("failed to list decisions", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to list decisions")
		return
	}

	writeJSONResponse(w, map[string]any{"decisions": decisions})
}

// handleByID routes /api/decisions/{id} and /api/decisions/{id}/{action}.
func (a *DecisionAPI) handleByID(w http.ResponseWriter, r *http.Request) {
	// Parse path: /api/decisions/{id}[/{action}]
	path := strings.TrimPrefix(r.URL.Path, "/api/decisions/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "missing decision ID", http.StatusBadRequest)
		return
	}

	id := parts[0]
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}

	switch action {
	case "":
		a.handleShow(w, r, id)
	case "resolve":
		a.handleResolve(w, r, id)
	case "dismiss":
		a.handleDismiss(w, r, id)
	default:
		http.NotFound(w, r)
	}
}

// handleShow handles GET /api/decisions/{id}.
func (a *DecisionAPI) handleShow(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	detail, err := a.client.GetDecision(r.Context(), id)
	if err != nil {
		a.logger.Error("failed to get decision", "id", id, "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to get decision")
		return
	}

	writeJSONResponse(w, detail)
}

// resolveRequest is the JSON body for POST /api/decisions/{id}/resolve.
type resolveRequest struct {
	Chosen      string `json:"chosen"`
	Rationale   string `json:"rationale"`
	RespondedBy string `json:"respondedBy"`
}

// handleResolve handles POST /api/decisions/{id}/resolve.
func (a *DecisionAPI) handleResolve(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req resolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Chosen == "" {
		writeJSONError(w, http.StatusBadRequest, "chosen is required")
		return
	}

	respondedBy := req.RespondedBy
	if respondedBy == "" {
		respondedBy = "web-ui"
	}

	err := a.client.ResolveDecision(r.Context(), id, beadsapi.ResolveDecisionRequest{
		SelectedOption: req.Chosen,
		ResponseText:   req.Rationale,
		RespondedBy:    respondedBy,
	})
	if err != nil {
		a.logger.Error("failed to resolve decision", "id", id, "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to resolve decision")
		return
	}

	a.logger.Info("decision resolved via web UI", "id", id, "chosen", req.Chosen)
	writeJSONResponse(w, map[string]string{"status": "resolved"})
}

// dismissRequest is the JSON body for POST /api/decisions/{id}/dismiss.
type dismissRequest struct {
	Reason    string `json:"reason"`
	CanceledBy string `json:"canceledBy"`
}

// handleDismiss handles POST /api/decisions/{id}/dismiss.
func (a *DecisionAPI) handleDismiss(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req dismissRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	canceledBy := req.CanceledBy
	if canceledBy == "" {
		canceledBy = "web-ui"
	}

	err := a.client.CancelDecision(r.Context(), id, req.Reason, canceledBy)
	if err != nil {
		a.logger.Error("failed to dismiss decision", "id", id, "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to dismiss decision")
		return
	}

	a.logger.Info("decision dismissed via web UI", "id", id, "reason", req.Reason)
	writeJSONResponse(w, map[string]string{"status": "dismissed"})
}

// writeJSONResponse writes a JSON response with status 200.
func writeJSONResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// writeJSONError writes a JSON error response.
func writeJSONError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
