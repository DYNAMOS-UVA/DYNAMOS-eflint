package reasoner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/eflint"
)

// -----------------------------------------------------------------------------
// eFLINT Reasoner Implementation
// -----------------------------------------------------------------------------

// EflintReasoner implements the Reasoner interface using an eFLINT server.
// It translates Reasoner API calls into eFLINT commands and parses the responses.
type EflintReasoner struct {
	manager *eflint.Manager
	logger  *zap.Logger
}

// NewEflintReasoner creates a new eFLINT-based reasoner.
func NewEflintReasoner(manager *eflint.Manager, logger *zap.Logger) *EflintReasoner {
	return &EflintReasoner{
		manager: manager,
		logger:  logger,
	}
}

// Name returns the name of this reasoner.
func (r *EflintReasoner) Name() string {
	return "eflint"
}

// IsRunning checks if the eFLINT server is running.
func (r *EflintReasoner) IsRunning() bool {
	return r.manager.IsRunning()
}

// -----------------------------------------------------------------------------
// Facts Retrieval
// -----------------------------------------------------------------------------

// FetchFacts retrieves all facts from the eFLINT server.
// This can be used to fetch facts once and then filter them multiple times
// without making repeated calls to the eFLINT server.
func (r *EflintReasoner) FetchFacts(ctx context.Context) ([]eflintFact, error) {
	response, err := r.manager.SendCommand(`{"command": "facts"}`)
	if err != nil {
		return nil, fmt.Errorf("failed to get facts from eFLINT: %w", err)
	}

	facts, err := parseFactsResponse(response)
	if err != nil {
		return nil, fmt.Errorf("failed to parse facts response: %w", err)
	}

	return facts, nil
}

// -----------------------------------------------------------------------------
// Allowed Clauses Retrieval
// -----------------------------------------------------------------------------

// GetAllowedRequestTypes returns all request types allowed for a requester at an organization.
func (r *EflintReasoner) GetAllowedRequestTypes(ctx context.Context, organization, requester string) ([]string, error) {
	facts, err := r.FetchFacts(ctx)
	if err != nil {
		return nil, err
	}
	return r.filterAllowedClauses(facts, "allowed-request-type", "request-type", organization, requester), nil
}

// GetAllowedDataSets returns all datasets allowed for a requester at an organization.
func (r *EflintReasoner) GetAllowedDataSets(ctx context.Context, organization, requester string) ([]string, error) {
	facts, err := r.FetchFacts(ctx)
	if err != nil {
		return nil, err
	}
	return r.filterAllowedClauses(facts, "allowed-data-set", "data-set", organization, requester), nil
}

// GetAllowedArchetypes returns all archetypes allowed for a requester at an organization.
func (r *EflintReasoner) GetAllowedArchetypes(ctx context.Context, organization, requester string) ([]string, error) {
	facts, err := r.FetchFacts(ctx)
	if err != nil {
		return nil, err
	}
	return r.filterAllowedClauses(facts, "allowed-archetype", "archetype", organization, requester), nil
}

// GetAllowedComputeProviders returns all compute providers allowed for a requester at an organization.
func (r *EflintReasoner) GetAllowedComputeProviders(ctx context.Context, organization, requester string) ([]string, error) {
	facts, err := r.FetchFacts(ctx)
	if err != nil {
		return nil, err
	}
	return r.filterAllowedClauses(facts, "allowed-compute-provider", "compute-provider", organization, requester), nil
}

// GetAllAllowedClauses returns all allowed clauses for a requester at an organization.
// This is more efficient than calling the individual methods because it only fetches
// facts from the eFLINT server once.
func (r *EflintReasoner) GetAllAllowedClauses(ctx context.Context, organization, requester string) (*AllAllowedClauses, error) {
	// Fetch facts once
	facts, err := r.FetchFacts(ctx)
	if err != nil {
		return nil, err
	}

	// Filter all clause types from the same facts
	return &AllAllowedClauses{
		RequestTypes:     r.filterAllowedClauses(facts, "allowed-request-type", "request-type", organization, requester),
		DataSets:         r.filterAllowedClauses(facts, "allowed-data-set", "data-set", organization, requester),
		Archetypes:       r.filterAllowedClauses(facts, "allowed-archetype", "archetype", organization, requester),
		ComputeProviders: r.filterAllowedClauses(facts, "allowed-compute-provider", "compute-provider", organization, requester),
	}, nil
}

// filterAllowedClauses filters pre-fetched facts for allowed clauses.
// This is a pure function that doesn't make any network calls.
func (r *EflintReasoner) filterAllowedClauses(
	facts []eflintFact,
	factType string, // e.g., "allowed-archetype"
	valueFactType string, // e.g., "archetype"
	organization string,
	requester string,
) []string {
	var values []string
	for _, fact := range facts {
		if fact.FactType == factType && len(fact.Arguments) >= 3 {
			// Arguments: [0]=organization, [1]=requester, [2]=value
			if fact.Arguments[0].FactType == "organization" &&
				fact.Arguments[0].Value == organization &&
				fact.Arguments[1].FactType == "requester" &&
				fact.Arguments[1].Value == requester &&
				fact.Arguments[2].FactType == valueFactType {
				values = append(values, fact.Arguments[2].Value)
			}
		}
	}
	return values
}

// -----------------------------------------------------------------------------
// Request Validation
// -----------------------------------------------------------------------------

// IsRequestAllowed checks if a specific request is permitted according to the eFLINT policy.
// It uses the "enabled" command on the submit-request act to determine if the request is allowed.
func (r *EflintReasoner) IsRequestAllowed(ctx context.Context, params RequestParams) (*RequestValidationResult, error) {
	// Build the eFLINT "enabled" command with a properly structured VALUE
	// This checks if the submit-request action is enabled with the given parameters
	cmd := map[string]interface{}{
		"command": "enabled",
		"value": map[string]interface{}{
			"fact-type": "submit-request",
			"value": []map[string]interface{}{
				{"fact-type": "req", "value": params.Requester},
				{"fact-type": "org", "value": params.Organization},
				{"fact-type": "rtype", "value": params.RequestType},
				{"fact-type": "dataset", "value": params.DataSet},
				{"fact-type": "arch", "value": params.Archetype},
				{"fact-type": "provider", "value": params.ComputeProvider},
			},
		},
	}

	cmdJSON, err := json.Marshal(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal command: %w", err)
	}

	response, err := r.manager.SendCommand(string(cmdJSON))
	if err != nil {
		return nil, fmt.Errorf("failed to query eFLINT: %w", err)
	}

	r.logger.Debug("eFLINT enabled query response",
		zap.String("command", string(cmdJSON)),
		zap.String("response", response),
	)

	// Parse the response and include raw response for debugging
	result, err := r.parseValidationResponse(response, params)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// parseValidationResponse parses the eFLINT response for an "enabled" query.
// The enabled command returns a Status response with query-results containing "success" if enabled.
func (r *EflintReasoner) parseValidationResponse(response string, params RequestParams) (*RequestValidationResult, error) {
	var resp struct {
		Response     string   `json:"response"`
		QueryResults []string `json:"query-results"` // eFLINT returns "success" when enabled
		Errors       []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"errors"`
		Violations []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"violations"`
	}

	if err := json.Unmarshal([]byte(response), &resp); err != nil {
		return nil, fmt.Errorf("failed to parse eFLINT response: %w", err)
	}

	// Check if the enabled query succeeded
	// The query-results array contains "success" when the action is enabled
	isEnabled := len(resp.QueryResults) > 0 && strings.EqualFold(resp.QueryResults[0], "success")

	result := &RequestValidationResult{
		Allowed: isEnabled && len(resp.Violations) == 0 && len(resp.Errors) == 0,
	}

	// Build reason from errors or violations
	var reasons []string
	for _, err := range resp.Errors {
		reasons = append(reasons, err.Message)
	}
	for _, v := range resp.Violations {
		reasons = append(reasons, v.Message)
	}

	if len(reasons) > 0 {
		result.Reason = strings.Join(reasons, "; ")
	} else if result.Allowed {
		result.Reason = "Request is permitted by the agreement"
	} else {
		result.Reason = "Request is not permitted by the agreement"
	}

	return result, nil
}

// -----------------------------------------------------------------------------
// Availability Provider Implementation
// -----------------------------------------------------------------------------

// GetAvailableArchetypes returns archetypes available at an organization.
func (r *EflintReasoner) GetAvailableArchetypes(ctx context.Context, organization string) ([]string, error) {
	facts, err := r.FetchFacts(ctx)
	if err != nil {
		return nil, err
	}
	return r.filterAvailableFacts(facts, "available-archetype", "archetype", organization), nil
}

// GetAvailableComputeProviders returns compute providers available at an organization.
func (r *EflintReasoner) GetAvailableComputeProviders(ctx context.Context, organization string) ([]string, error) {
	facts, err := r.FetchFacts(ctx)
	if err != nil {
		return nil, err
	}
	return r.filterAvailableFacts(facts, "available-compute-provider", "compute-provider", organization), nil
}

// filterAvailableFacts filters pre-fetched facts for available resources at an organization.
// This is a pure function that doesn't make any network calls.
func (r *EflintReasoner) filterAvailableFacts(
	facts []eflintFact,
	factType string,
	valueFactType string,
	organization string,
) []string {
	var values []string
	for _, fact := range facts {
		if fact.FactType == factType && len(fact.Arguments) >= 2 {
			// Arguments: [0]=organization, [1]=value
			if fact.Arguments[0].FactType == "organization" &&
				fact.Arguments[0].Value == organization &&
				fact.Arguments[1].FactType == valueFactType {
				values = append(values, fact.Arguments[1].Value)
			}
		}
	}
	return values
}

// -----------------------------------------------------------------------------
// Helper Types and Functions
// -----------------------------------------------------------------------------

// eflintFact represents a fact from the eFLINT facts response.
type eflintFact struct {
	FactType   string `json:"fact-type"`
	TaggedType string `json:"tagged-type"`
	Arguments  []struct {
		FactType string `json:"fact-type"`
		Value    string `json:"value"`
	} `json:"arguments"`
}

// parseFactsResponse parses the JSON response from an eFLINT "facts" command.
func parseFactsResponse(response string) ([]eflintFact, error) {
	var factsResponse struct {
		Values []eflintFact `json:"values"`
	}

	if err := json.Unmarshal([]byte(response), &factsResponse); err != nil {
		return nil, err
	}

	return factsResponse.Values, nil
}

// Ensure EflintReasoner implements the interfaces
var _ Reasoner = (*EflintReasoner)(nil)
var _ AvailabilityProvider = (*EflintReasoner)(nil)
