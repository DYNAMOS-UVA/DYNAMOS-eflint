package service

import (
	"context"

	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/repository"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
	"go.uber.org/zap"
)

// ValidationService orchestrates the validation of request approvals.
type ValidationService struct {
	agreementRepo repository.AgreementRepository
	validator     *AgreementValidator
	authGenerator AuthTokenGenerator
	logger        *zap.Logger
}

// NewValidationService creates a new ValidationService with the given dependencies.
func NewValidationService(
	agreementRepo repository.AgreementRepository,
	validator *AgreementValidator,
	authGenerator AuthTokenGenerator,
	logger *zap.Logger,
) *ValidationService {
	return &ValidationService{
		agreementRepo: agreementRepo,
		validator:     validator,
		authGenerator: authGenerator,
		logger:        logger,
	}
}

// ValidateRequest processes a request approval and returns a validation response.
func (s *ValidationService) ValidateRequest(ctx context.Context, request *pb.RequestApproval) *pb.ValidationResponse {
	s.logger.Debug("Starting request validation",
		zap.String("user", request.User.UserName),
		zap.Strings("dataProviders", request.DataProviders),
	)

	// Build the initial response
	response := s.buildInitialResponse(request)

	// Validate agreements for all requested data providers
	validationResults := s.validateDataProviders(request.DataProviders, request.User.UserName)

	// Process validation results and update response
	s.processValidationResults(validationResults, request.User.UserName, response)

	// Determine if request is approved
	response.RequestApproved = len(response.ValidDataproviders) > 0

	// Generate auth token if approved
	if response.RequestApproved {
		response.Auth = s.authGenerator.GenerateToken()
	}

	s.logger.Info("Request validation completed",
		zap.String("user", request.User.UserName),
		zap.Bool("approved", response.RequestApproved),
		zap.Int("validProviders", len(response.ValidDataproviders)),
		zap.Int("invalidProviders", len(response.InvalidDataproviders)),
	)

	return response
}

// buildInitialResponse creates the initial ValidationResponse with base fields.
func (s *ValidationService) buildInitialResponse(request *pb.RequestApproval) *pb.ValidationResponse {
	response := &pb.ValidationResponse{
		Type:        MessageTypeValidationResponse,
		RequestType: request.Type,
		User: &pb.User{
			Id:       request.User.Id,
			UserName: request.User.UserName,
		},
		RequestApproved:      false,
		ValidArchetypes:      &pb.UserArchetypes{Archetypes: make(map[string]*pb.UserAllowedArchetypes)},
		Options:              make(map[string]bool),
		ValidDataproviders:   make(map[string]*pb.DataProvider),
		InvalidDataproviders: []string{},
	}

	// Copy options from request if present
	if request.Options != nil && len(request.Options) > 0 {
		response.Options = request.Options
	}

	return response
}

// validateDataProviders validates agreements for all requested data providers.
func (s *ValidationService) validateDataProviders(dataProviders []string, userName string) []*ValidationResult {
	results := make([]*ValidationResult, 0, len(dataProviders))

	for _, steward := range dataProviders {
		result := s.validateSingleProvider(steward, userName)
		results = append(results, result)
	}

	return results
}

// validateSingleProvider validates a single data provider's agreement for a user.
func (s *ValidationService) validateSingleProvider(steward, userName string) *ValidationResult {
	// Fetch agreement from repository
	agreement, found, err := s.agreementRepo.GetAgreement(steward)
	if err != nil {
		s.logger.Error("Failed to retrieve agreement",
			zap.String("steward", steward),
			zap.Error(err),
		)
		return &ValidationResult{
			Steward:       steward,
			IsValid:       false,
			InvalidReason: "error retrieving agreement",
		}
	}

	if !found {
		s.logger.Info("Agreement not found for steward",
			zap.String("steward", steward),
		)
		return &ValidationResult{
			Steward:       steward,
			IsValid:       false,
			InvalidReason: "agreement not found",
		}
	}

	// Validate user access against the agreement
	return s.validator.ValidateUserAccess(agreement, userName)
}

// processValidationResults updates the response based on validation results.
func (s *ValidationService) processValidationResults(
	results []*ValidationResult,
	userName string,
	response *pb.ValidationResponse,
) {
	for _, result := range results {
		if result.IsValid {
			s.addValidProvider(result, userName, response)
		} else {
			response.InvalidDataproviders = append(response.InvalidDataproviders, result.Steward)
			s.logger.Debug("Provider validation failed",
				zap.String("steward", result.Steward),
				zap.String("reason", result.InvalidReason),
			)
		}
	}
}

// addValidProvider adds a validated provider to the response.
func (s *ValidationService) addValidProvider(
	result *ValidationResult,
	userName string,
	response *pb.ValidationResponse,
) {
	// Set valid archetypes for user
	response.ValidArchetypes.UserName = userName
	response.ValidArchetypes.Archetypes[result.Steward] = &pb.UserAllowedArchetypes{
		Archetypes: result.MatchedArchetypes,
	}

	// Add to valid data providers
	response.ValidDataproviders[result.Steward] = &pb.DataProvider{
		Archetypes:       result.MatchedArchetypes,
		ComputeProviders: result.MatchedComputeProvs,
	}
}
