package service

import (
	"github.com/Jorrit05/DYNAMOS/pkg/api"
	"github.com/Jorrit05/DYNAMOS/pkg/lib"
)

// ValidationResult represents the result of validating a user against a data provider's agreement.
type ValidationResult struct {
	Steward              string
	IsValid              bool
	InvalidReason        string
	MatchedArchetypes    []string
	MatchedComputeProvs  []string
	UserRelation         *api.Relation
}

// AgreementValidator validates user access against agreements.
type AgreementValidator struct{}

// NewAgreementValidator creates a new AgreementValidator.
func NewAgreementValidator() *AgreementValidator {
	return &AgreementValidator{}
}

// ValidateUserAccess validates whether a user has access based on an agreement.
// It checks if the user exists in the agreement's relations and matches archetypes/compute providers.
func (v *AgreementValidator) ValidateUserAccess(agreement *api.Agreement, userName string) *ValidationResult {
	result := &ValidationResult{
		Steward: agreement.Name,
		IsValid: false,
	}

	// Check if user exists in agreement relations
	userRelation, exists := agreement.Relations[userName]
	if !exists {
		result.InvalidReason = "user not found in agreement relations"
		return result
	}
	result.UserRelation = &userRelation

	// Match user's allowed archetypes with agreement's supported archetypes
	matchedArchetypes, _ := lib.GetMatchedElements(userRelation.AllowedArchetypes, agreement.Archetypes)
	if len(matchedArchetypes) == 0 {
		result.InvalidReason = "no matching archetypes between user permissions and agreement"
		return result
	}
	result.MatchedArchetypes = matchedArchetypes

	// Match user's allowed compute providers with agreement's compute providers
	matchedCompute, _ := lib.GetMatchedElements(userRelation.AllowedComputeProviders, agreement.ComputeProviders)
	result.MatchedComputeProvs = matchedCompute

	result.IsValid = true
	return result
}
