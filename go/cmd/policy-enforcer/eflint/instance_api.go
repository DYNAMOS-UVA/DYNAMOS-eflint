package eflint

import (
	"encoding/json"
	"net/http"

	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/httpapi"
	"go.uber.org/zap"
)

// -----------------------------------------------------------------------------
// Instance API Handler
// -----------------------------------------------------------------------------

// InstanceAPIHandler handles HTTP requests for eFLINT instance lifecycle management.
// It provides endpoints for starting, stopping, and sending commands to the eFLINT server.
type InstanceAPIHandler struct {
	manager *Manager
	logger  *zap.Logger
}

// NewInstanceAPIHandler creates a new instance API handler with the given manager and logger.
func NewInstanceAPIHandler(manager *Manager, logger *zap.Logger) *InstanceAPIHandler {
	return &InstanceAPIHandler{
		manager: manager,
		logger:  logger,
	}
}

// -----------------------------------------------------------------------------
// Request/Response Types
// -----------------------------------------------------------------------------

// StatusResponse represents the response for status-related endpoints.
type StatusResponse struct {
	Running       bool            `json:"running"`                  // Whether the instance is running
	Port          int             `json:"port,omitempty"`           // The port the instance is listening on
	ModelLocation string          `json:"model_location,omitempty"` // Path to the loaded model
	EflintStatus  json.RawMessage `json:"eflint_status,omitempty"`  // Status response from the eFLINT server
}

// StartRequest represents the request body for starting an instance.
type StartRequest struct {
	ModelLocation string `json:"model_location"`  // Path to the eFLINT model file
	Force         bool   `json:"force,omitempty"` // Force restart if already running
}

// CommandRequest represents the request body for sending a command.
// The Command field can be either:
// - A string containing the JSON command (for backward compatibility)
// - A JSON object that will be serialized before sending to eFLINT
type CommandRequest struct {
	Command json.RawMessage `json:"command"` // The JSON command to send to eFLINT (string or object)
}

// CommandResponse represents the response from a command execution.
type CommandResponse struct {
	Parsed json.RawMessage `json:"response"` // The parsed JSON response from eFLINT
}

// ErrorResponse represents an error response returned by the API.
type ErrorResponse struct {
	Error string `json:"error"` // Human-readable error message
}

// AllowedArchetypesResponse represents the response for querying allowed archetypes.
type AllowedArchetypesResponse struct {
	Organization string   `json:"organization"` // The organization/steward
	Requester    string   `json:"requester"`    // The user/requester
	Archetypes   []string `json:"archetypes"`   // List of allowed archetypes
}

// -----------------------------------------------------------------------------
// Utility Functions
// -----------------------------------------------------------------------------

// mustMarshal marshals a value to JSON, returning empty bytes on error.
// Used for simple string wrapping in error cases.
func mustMarshal(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}

// parseCommandToString converts a json.RawMessage command to a string suitable for eFLINT.
// It handles two cases:
//  1. The command is a JSON string (e.g., `"{"command": "status"}"`) - returns the unquoted string
//  2. The command is a JSON object (e.g., `{"command": "status"}`) - re-marshals to compact single-line JSON
//
// This allows clients to send commands either as:
//   - {"command": "{\"command\": \"phrase\", \"text\": \"+fact(\\\"val\\\").\"}"}  (string, double-escaping)
//   - {"command": {"command": "phrase", "text": "+fact(\"val\")."}}                (object, standard escaping)
//
// The object format is recommended because quotes in eFLINT phrases only need standard JSON escaping,
// whereas the string format requires double-escaping (escaping the escape characters).
//
// Note: Object commands are always re-marshaled to compact JSON (no newlines) because the eFLINT
// server expects single-line JSON input.
func parseCommandToString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}

	// Trim whitespace to check the first character
	trimmed := raw
	for len(trimmed) > 0 && (trimmed[0] == ' ' || trimmed[0] == '\t' || trimmed[0] == '\n' || trimmed[0] == '\r') {
		trimmed = trimmed[1:]
	}

	if len(trimmed) == 0 {
		return "", nil
	}

	// Check if the raw message is a JSON string (starts with a quote)
	if trimmed[0] == '"' {
		// It's a string - unmarshal to get the actual string value
		var str string
		if err := json.Unmarshal(raw, &str); err != nil {
			return "", err
		}
		return str, nil
	}

	// It's an object or other JSON value - unmarshal and re-marshal to ensure compact format
	// The eFLINT server expects single-line JSON without newlines
	var obj interface{}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", err
	}

	// Re-marshal with compact encoding (no newlines, no indentation)
	compactJSON, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}

	return string(compactJSON), nil
}

// -----------------------------------------------------------------------------
// Handler Methods
// -----------------------------------------------------------------------------

// GetStatus returns the current status of the eFLINT instance.
// If the instance is running, it also sends a "status" command to the eFLINT server
// to retrieve detailed information about the current state.
// GET /eflint/status
func (h *InstanceAPIHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpapi.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	status := h.manager.Status()

	response := StatusResponse{
		Running:       status.Running,
		Port:          status.Port,
		ModelLocation: status.ModelLocation,
	}

	// If the instance is running, query the eFLINT server for its status
	if status.Running {
		eflintStatus, err := h.manager.GetEflintStatus()
		if err != nil {
			h.logger.Warn("failed to get eFLINT server status", zap.Error(err))
			// Continue without the eFLINT status - the instance might still be starting up
		} else if json.Valid([]byte(eflintStatus)) {
			response.EflintStatus = json.RawMessage(eflintStatus)
		}
	}

	httpapi.WriteJSON(w, http.StatusOK, response)
}

// Start starts the eFLINT instance with the given model.
// If an instance is already running and force=false, returns a conflict error.
// If force=true, the existing instance is stopped and a new one is started.
// POST /eflint/start
func (h *InstanceAPIHandler) Start(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpapi.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req StartRequest
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.ModelLocation == "" {
		httpapi.WriteError(w, http.StatusBadRequest, "model_location is required")
		return
	}

	// Check if instance is already running
	if h.manager.IsRunning() && !req.Force {
		httpapi.WriteError(w, http.StatusConflict, "instance already running, use force=true to restart")
		return
	}

	if err := h.manager.Start(req.ModelLocation); err != nil {
		h.logger.Error("failed to start instance", zap.Error(err))
		httpapi.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	status := h.manager.Status()
	httpapi.WriteJSON(w, http.StatusOK, StatusResponse{
		Running:       status.Running,
		Port:          status.Port,
		ModelLocation: status.ModelLocation,
	})
}

// Stop stops the running eFLINT instance.
// POST /eflint/stop
func (h *InstanceAPIHandler) Stop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpapi.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if err := h.manager.Stop(); err != nil {
		if err == ErrInstanceNotFound {
			httpapi.WriteError(w, http.StatusNotFound, "no instance running")
			return
		}
		h.logger.Error("failed to stop instance", zap.Error(err))
		httpapi.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	httpapi.WriteJSON(w, http.StatusOK, StatusResponse{Running: false})
}

// SendCommand sends a command to the eFLINT instance.
// POST /eflint/command
//
// The command field can be either:
//   - A string containing the JSON command: {"command": "{\"command\": \"status\"}"}
//   - A JSON object that will be serialized: {"command": {"command": "status"}}
func (h *InstanceAPIHandler) SendCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpapi.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req CommandRequest
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if len(req.Command) == 0 {
		httpapi.WriteError(w, http.StatusBadRequest, "command is required")
		return
	}

	// Convert the command to a string that can be sent to eFLINT
	commandStr, err := parseCommandToString(req.Command)
	if err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid command format: "+err.Error())
		return
	}

	response, err := h.manager.SendCommand(commandStr)
	if err != nil {
		if err == ErrInstanceNotFound {
			httpapi.WriteError(w, http.StatusNotFound, "no instance running")
			return
		}
		if err == ErrInstanceNotRunning {
			httpapi.WriteError(w, http.StatusServiceUnavailable, "instance is not running")
			return
		}
		h.logger.Error("failed to send command", zap.Error(err))
		httpapi.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Parse the response as JSON
	var parsed json.RawMessage
	if json.Valid([]byte(response)) {
		parsed = json.RawMessage(response)
	} else {
		parsed = json.RawMessage(`{"raw": ` + string(mustMarshal(response)) + `}`)
	}

	httpapi.WriteJSON(w, http.StatusOK, CommandResponse{
		Parsed: parsed,
	})
}

// NOTE: GetAllowedArchetypes and similar policy query methods have been moved to
// the /policy-enforcer API group. This provides a reasoner-agnostic interface that
// can work with different policy reasoning engines (eFLINT, Symboleo, JSON-based, etc.).
