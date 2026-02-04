package policyenforcer

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/reasoner"
)

// -----------------------------------------------------------------------------
// Policy Enforcer Service
// -----------------------------------------------------------------------------

// Enforcer provides the core policy enforcement functionality.
// It uses a Reasoner interface to query allowed clauses and validate requests,
// making it independent of the underlying reasoning engine (eFLINT, Symboleo, etc.).
type Enforcer struct {
	reasoner reasoner.Reasoner
	logger   *zap.Logger
}

// NewEnforcer creates a new policy enforcer with the given reasoner.
func NewEnforcer(r reasoner.Reasoner, logger *zap.Logger) *Enforcer {
	return &Enforcer{
		reasoner: r,
		logger:   logger,
	}
}

// GetReasonerInfo returns information about the active reasoner.
func (e *Enforcer) GetReasonerInfo() ReasonerInfoResponse {
	return ReasonerInfoResponse{
		Name:    e.reasoner.Name(),
		Running: e.reasoner.IsRunning(),
	}
}

// IsRunning checks if the underlying reasoner is operational.
func (e *Enforcer) IsRunning() bool {
	return e.reasoner.IsRunning()
}

// -----------------------------------------------------------------------------
// Allowed Clauses Retrieval
// -----------------------------------------------------------------------------

// GetAllowedRequestTypes returns all request types allowed for a requester at an organization.
func (e *Enforcer) GetAllowedRequestTypes(ctx context.Context, organization, requester string) (*AllowedClausesResponse, error) {
	if !e.reasoner.IsRunning() {
		return nil, fmt.Errorf("reasoner is not running")
	}

	values, err := e.reasoner.GetAllowedRequestTypes(ctx, organization, requester)
	if err != nil {
		e.logger.Error("failed to get allowed request types",
			zap.String("organization", organization),
			zap.String("requester", requester),
			zap.Error(err),
		)
		return nil, err
	}

	return &AllowedClausesResponse{
		Organization: organization,
		Requester:    requester,
		Values:       values,
	}, nil
}

// GetAllowedDataSets returns all datasets allowed for a requester at an organization.
func (e *Enforcer) GetAllowedDataSets(ctx context.Context, organization, requester string) (*AllowedClausesResponse, error) {
	if !e.reasoner.IsRunning() {
		return nil, fmt.Errorf("reasoner is not running")
	}

	values, err := e.reasoner.GetAllowedDataSets(ctx, organization, requester)
	if err != nil {
		e.logger.Error("failed to get allowed data sets",
			zap.String("organization", organization),
			zap.String("requester", requester),
			zap.Error(err),
		)
		return nil, err
	}

	return &AllowedClausesResponse{
		Organization: organization,
		Requester:    requester,
		Values:       values,
	}, nil
}

// GetAllowedArchetypes returns all archetypes allowed for a requester at an organization.
func (e *Enforcer) GetAllowedArchetypes(ctx context.Context, organization, requester string) (*AllowedClausesResponse, error) {
	if !e.reasoner.IsRunning() {
		return nil, fmt.Errorf("reasoner is not running")
	}

	values, err := e.reasoner.GetAllowedArchetypes(ctx, organization, requester)
	if err != nil {
		e.logger.Error("failed to get allowed archetypes",
			zap.String("organization", organization),
			zap.String("requester", requester),
			zap.Error(err),
		)
		return nil, err
	}

	return &AllowedClausesResponse{
		Organization: organization,
		Requester:    requester,
		Values:       values,
	}, nil
}

// GetAllowedComputeProviders returns all compute providers allowed for a requester at an organization.
func (e *Enforcer) GetAllowedComputeProviders(ctx context.Context, organization, requester string) (*AllowedClausesResponse, error) {
	if !e.reasoner.IsRunning() {
		return nil, fmt.Errorf("reasoner is not running")
	}

	values, err := e.reasoner.GetAllowedComputeProviders(ctx, organization, requester)
	if err != nil {
		e.logger.Error("failed to get allowed compute providers",
			zap.String("organization", organization),
			zap.String("requester", requester),
			zap.Error(err),
		)
		return nil, err
	}

	return &AllowedClausesResponse{
		Organization: organization,
		Requester:    requester,
		Values:       values,
	}, nil
}

// GetAllAllowedClauses returns all allowed clauses for a requester at an organization.
// This is more efficient than calling individual methods because it fetches facts
// from the reasoner only once.
func (e *Enforcer) GetAllAllowedClauses(ctx context.Context, organization, requester string) (*AllAllowedClausesResponse, error) {
	if !e.reasoner.IsRunning() {
		return nil, fmt.Errorf("reasoner is not running")
	}

	// Use the optimized method that fetches facts once
	clauses, err := e.reasoner.GetAllAllowedClauses(ctx, organization, requester)
	if err != nil {
		e.logger.Error("failed to get all allowed clauses",
			zap.String("organization", organization),
			zap.String("requester", requester),
			zap.Error(err),
		)
		return nil, err
	}

	return &AllAllowedClausesResponse{
		Organization:     organization,
		Requester:        requester,
		RequestTypes:     clauses.RequestTypes,
		DataSets:         clauses.DataSets,
		Archetypes:       clauses.Archetypes,
		ComputeProviders: clauses.ComputeProviders,
	}, nil
}

// -----------------------------------------------------------------------------
// Request Validation
// -----------------------------------------------------------------------------

// ValidateRequest checks if a specific request is allowed according to the policy.
func (e *Enforcer) ValidateRequest(ctx context.Context, params *ValidateRequestParams) (*ValidationResponse, error) {
	if !e.reasoner.IsRunning() {
		return nil, fmt.Errorf("reasoner is not running")
	}

	e.logger.Info("validating request",
		zap.String("organization", params.Organization),
		zap.String("requester", params.Requester),
		zap.String("request_type", params.RequestType),
		zap.String("data_set", params.DataSet),
		zap.String("archetype", params.Archetype),
		zap.String("compute_provider", params.ComputeProvider),
	)

	result, err := e.reasoner.IsRequestAllowed(ctx, params.ToReasonerParams())
	if err != nil {
		e.logger.Error("failed to validate request", zap.Error(err))
		return nil, err
	}

	response := &ValidationResponse{
		Allowed:         result.Allowed,
		Reason:          result.Reason,
		Organization:    params.Organization,
		Requester:       params.Requester,
		RequestType:     params.RequestType,
		DataSet:         params.DataSet,
		Archetype:       params.Archetype,
		ComputeProvider: params.ComputeProvider,
	}

	e.logger.Info("request validation complete",
		zap.Bool("allowed", response.Allowed),
		zap.String("reason", response.Reason),
	)

	return response, nil
}

// -----------------------------------------------------------------------------
// Availability (if supported by the reasoner)
// -----------------------------------------------------------------------------

// GetAvailableArchetypes returns archetypes available at an organization (not requester-specific).
// This only works if the underlying reasoner supports the AvailabilityProvider interface.
func (e *Enforcer) GetAvailableArchetypes(ctx context.Context, organization string) ([]string, error) {
	if !e.reasoner.IsRunning() {
		return nil, fmt.Errorf("reasoner is not running")
	}

	ap, ok := e.reasoner.(reasoner.AvailabilityProvider)
	if !ok {
		return nil, fmt.Errorf("reasoner does not support availability queries")
	}

	return ap.GetAvailableArchetypes(ctx, organization)
}

// GetAvailableComputeProviders returns compute providers available at an organization (not requester-specific).
// This only works if the underlying reasoner supports the AvailabilityProvider interface.
func (e *Enforcer) GetAvailableComputeProviders(ctx context.Context, organization string) ([]string, error) {
	if !e.reasoner.IsRunning() {
		return nil, fmt.Errorf("reasoner is not running")
	}

	ap, ok := e.reasoner.(reasoner.AvailabilityProvider)
	if !ok {
		return nil, fmt.Errorf("reasoner does not support availability queries")
	}

	return ap.GetAvailableComputeProviders(ctx, organization)
}
