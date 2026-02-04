package policyenforcerhttp

import (
	"net/http"

	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/httpapi"
	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/policyenforcer"
	"go.uber.org/zap"
)

// HTTPHandler handles HTTP requests for the policy enforcer API.
// It provides REST endpoints for querying allowed clauses and validating requests.
type HTTPHandler struct {
	enforcer *policyenforcer.Enforcer
	logger   *zap.Logger
}

// NewHTTPHandler creates a new HTTP handler for the policy enforcer.
func NewHTTPHandler(enforcer *policyenforcer.Enforcer, logger *zap.Logger) *HTTPHandler {
	return &HTTPHandler{
		enforcer: enforcer,
		logger:   logger,
	}
}

// GetReasonerInfo returns information about the active reasoner.
// GET /policy-enforcer/info
func (h *HTTPHandler) GetReasonerInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpapi.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	info := h.enforcer.GetReasonerInfo()
	httpapi.WriteJSON(w, http.StatusOK, info)
}

// GetAllowedRequestTypes returns all request types allowed for a requester at an organization.
// GET /policy-enforcer/allowed-request-types?organization=VU&requester=user@example.com
func (h *HTTPHandler) GetAllowedRequestTypes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpapi.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	organization, requester, ok := h.parseOrgRequester(w, r)
	if !ok {
		return
	}

	result, err := h.enforcer.GetAllowedRequestTypes(r.Context(), organization, requester)
	if err != nil {
		h.handleError(w, err)
		return
	}

	httpapi.WriteJSON(w, http.StatusOK, result)
}

// GetAllowedDataSets returns all datasets allowed for a requester at an organization.
// GET /policy-enforcer/allowed-data-sets?organization=VU&requester=user@example.com
func (h *HTTPHandler) GetAllowedDataSets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpapi.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	organization, requester, ok := h.parseOrgRequester(w, r)
	if !ok {
		return
	}

	result, err := h.enforcer.GetAllowedDataSets(r.Context(), organization, requester)
	if err != nil {
		h.handleError(w, err)
		return
	}

	httpapi.WriteJSON(w, http.StatusOK, result)
}

// GetAllowedArchetypes returns all archetypes allowed for a requester at an organization.
// GET /policy-enforcer/allowed-archetypes?organization=VU&requester=user@example.com
func (h *HTTPHandler) GetAllowedArchetypes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpapi.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	organization, requester, ok := h.parseOrgRequester(w, r)
	if !ok {
		return
	}

	result, err := h.enforcer.GetAllowedArchetypes(r.Context(), organization, requester)
	if err != nil {
		h.handleError(w, err)
		return
	}

	httpapi.WriteJSON(w, http.StatusOK, result)
}

// GetAllowedComputeProviders returns all compute providers allowed for a requester at an organization.
// GET /policy-enforcer/allowed-compute-providers?organization=VU&requester=user@example.com
func (h *HTTPHandler) GetAllowedComputeProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpapi.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	organization, requester, ok := h.parseOrgRequester(w, r)
	if !ok {
		return
	}

	result, err := h.enforcer.GetAllowedComputeProviders(r.Context(), organization, requester)
	if err != nil {
		h.handleError(w, err)
		return
	}

	httpapi.WriteJSON(w, http.StatusOK, result)
}

// GetAllAllowedClauses returns all allowed clauses for a requester at an organization.
// GET /policy-enforcer/allowed-clauses?organization=VU&requester=user@example.com
func (h *HTTPHandler) GetAllAllowedClauses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpapi.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	organization, requester, ok := h.parseOrgRequester(w, r)
	if !ok {
		return
	}

	result, err := h.enforcer.GetAllAllowedClauses(r.Context(), organization, requester)
	if err != nil {
		h.handleError(w, err)
		return
	}

	httpapi.WriteJSON(w, http.StatusOK, result)
}

// ValidateRequest checks if a specific request is allowed.
// POST /policy-enforcer/validate
// Body: { "organization": "VU", "requester": "user@example.com", "request_type": "sqlDataRequest", ... }
func (h *HTTPHandler) ValidateRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpapi.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var params policyenforcer.ValidateRequestParams
	if err := httpapi.DecodeJSON(r, &params); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate required fields
	if params.Organization == "" {
		httpapi.WriteError(w, http.StatusBadRequest, "organization is required")
		return
	}
	if params.Requester == "" {
		httpapi.WriteError(w, http.StatusBadRequest, "requester is required")
		return
	}
	if params.RequestType == "" {
		httpapi.WriteError(w, http.StatusBadRequest, "request_type is required")
		return
	}
	if params.DataSet == "" {
		httpapi.WriteError(w, http.StatusBadRequest, "data_set is required")
		return
	}
	if params.Archetype == "" {
		httpapi.WriteError(w, http.StatusBadRequest, "archetype is required")
		return
	}
	if params.ComputeProvider == "" {
		httpapi.WriteError(w, http.StatusBadRequest, "compute_provider is required")
		return
	}

	result, err := h.enforcer.ValidateRequest(r.Context(), &params)
	if err != nil {
		h.handleError(w, err)
		return
	}

	httpapi.WriteJSON(w, http.StatusOK, result)
}

// GetAvailableArchetypes returns archetypes available at an organization (not requester-specific).
// GET /policy-enforcer/available-archetypes?organization=VU
func (h *HTTPHandler) GetAvailableArchetypes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpapi.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	organization := r.URL.Query().Get("organization")
	if organization == "" {
		httpapi.WriteError(w, http.StatusBadRequest, "organization parameter is required")
		return
	}

	values, err := h.enforcer.GetAvailableArchetypes(r.Context(), organization)
	if err != nil {
		h.handleError(w, err)
		return
	}

	httpapi.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"organization": organization,
		"archetypes":   values,
	})
}

// GetAvailableComputeProviders returns compute providers available at an organization (not requester-specific).
// GET /policy-enforcer/available-compute-providers?organization=VU
func (h *HTTPHandler) GetAvailableComputeProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpapi.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	organization := r.URL.Query().Get("organization")
	if organization == "" {
		httpapi.WriteError(w, http.StatusBadRequest, "organization parameter is required")
		return
	}

	values, err := h.enforcer.GetAvailableComputeProviders(r.Context(), organization)
	if err != nil {
		h.handleError(w, err)
		return
	}

	httpapi.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"organization":      organization,
		"compute_providers": values,
	})
}

// -----------------------------------------------------------------------------
// Helper Methods
// -----------------------------------------------------------------------------

// parseOrgRequester extracts and validates organization and requester query parameters.
func (h *HTTPHandler) parseOrgRequester(w http.ResponseWriter, r *http.Request) (organization, requester string, ok bool) {
	organization = r.URL.Query().Get("organization")
	requester = r.URL.Query().Get("requester")

	if organization == "" {
		httpapi.WriteError(w, http.StatusBadRequest, "organization parameter is required")
		return "", "", false
	}
	if requester == "" {
		httpapi.WriteError(w, http.StatusBadRequest, "requester parameter is required")
		return "", "", false
	}

	return organization, requester, true
}

// handleError converts service errors to appropriate HTTP responses.
func (h *HTTPHandler) handleError(w http.ResponseWriter, err error) {
	// Check if reasoner is not running
	if !h.enforcer.IsRunning() {
		httpapi.WriteError(w, http.StatusServiceUnavailable, "reasoner is not running")
		return
	}

	h.logger.Error("policy enforcer error", zap.Error(err))
	httpapi.WriteError(w, http.StatusInternalServerError, err.Error())
}
