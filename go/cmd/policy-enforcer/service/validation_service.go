package service

import (
	"context"
	"fmt"

	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/reasoner"
	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/repository"
	"github.com/Jorrit05/DYNAMOS/pkg/api"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
	"go.uber.org/zap"
)

// ValidationService orchestrates request-approval validation under the
// layered eFLINT design.
//
// A single evaluation now runs over all requested stewards on one eFLINT
// pool instance: per-steward Layer-2 phrases are gathered through
// AgreementPhraseProviders (eFLINT-format vs legacy-JSON), the Layer-2 shared
// rules come from EflintRulesRepository, and the reasoner builds Layer 3
// from the requester / requested-steward inputs. The resulting per-steward
// decisions are mapped back to the existing pb.ValidationResponse wire format
// so the orchestrator side does not need to change.
type ValidationService struct {
	providerConfigRepo repository.ProviderConfigRepository
	rulesRepo          repository.EflintRulesRepository
	legacyProvider     AgreementPhraseProvider
	eflintProvider     AgreementPhraseProvider // optional: nil if eFLINT-format provider not configured

	reasoner      reasoner.Reasoner
	authGenerator AuthTokenGenerator
	logger        *zap.Logger
}

// ValidationServiceConfig holds the configuration for creating a ValidationService.
type ValidationServiceConfig struct {
	ProviderConfigRepo repository.ProviderConfigRepository
	RulesRepo          repository.EflintRulesRepository
	LegacyProvider     AgreementPhraseProvider
	EflintProvider     AgreementPhraseProvider // optional
	Reasoner           reasoner.Reasoner
	AuthGenerator      AuthTokenGenerator
	Logger             *zap.Logger
}

// NewValidationServiceWithConfig creates a ValidationService with the given configuration.
func NewValidationServiceWithConfig(cfg ValidationServiceConfig) *ValidationService {
	return &ValidationService{
		providerConfigRepo: cfg.ProviderConfigRepo,
		rulesRepo:          cfg.RulesRepo,
		legacyProvider:     cfg.LegacyProvider,
		eflintProvider:     cfg.EflintProvider,
		reasoner:           cfg.Reasoner,
		authGenerator:      cfg.AuthGenerator,
		logger:             cfg.Logger,
	}
}

// ValidateRequest processes a request approval and returns a validation response.
func (s *ValidationService) ValidateRequest(ctx context.Context, request *pb.RequestApproval) *pb.ValidationResponse {
	s.logger.Debug("Starting request validation",
		zap.String("user", request.User.UserName),
		zap.Strings("dataProviders", request.DataProviders),
	)

	response := s.buildInitialResponse(request)

	// Phase 1: collect Layer-2 phrases per steward (no eFLINT calls yet).
	stewardPhrases, missingStewards := s.collectStewardPhrases(request.DataProviders)
	for _, missing := range missingStewards {
		response.InvalidDataproviders = append(response.InvalidDataproviders, missing)
		s.logger.Debug("steward has no agreement; marking invalid",
			zap.String("steward", missing),
		)
	}

	// Phase 2: gather Layer-2 shared rules.
	sharedRules, err := s.loadSharedRules()
	if err != nil {
		s.logger.Error("failed to load Layer-2 shared rules; aborting evaluation",
			zap.Error(err),
		)
		s.markAllInvalid(request.DataProviders, response, "shared rules unavailable")
		return response
	}

	// Phase 3: run a single layered evaluation across all requested stewards.
	if len(stewardPhrases) > 0 {
		eval, err := s.reasoner.EvaluateRequestApproval(ctx, reasoner.RequestApprovalParams{
			Requester:      request.User.UserName,
			Stewards:       request.DataProviders,
			SharedRules:    sharedRules,
			StewardPhrases: stewardPhrases,
		})
		if err != nil {
			s.logger.Error("eFLINT layered evaluation failed",
				zap.Error(err),
			)
			s.markAllInvalid(request.DataProviders, response, "reasoner error")
			return response
		}
		s.applyEvaluation(eval, request, response)
	}

	response.RequestApproved = len(response.ValidDataproviders) > 0
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
		Type:        "validationResponse",
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

	if len(request.Options) > 0 {
		response.Options = request.Options
	}

	return response
}

// collectStewardPhrases iterates over the requested data providers, resolves
// the steward's AgreementPhraseProvider from its ProviderValidationConfig,
// and returns the Layer-2 phrase block per steward. Stewards without an
// agreement (or with a retrieval error) are returned in the second slice so
// the caller can mark them invalid.
func (s *ValidationService) collectStewardPhrases(dataProviders []string) (map[string]string, []string) {
	phrases := make(map[string]string, len(dataProviders))
	var missing []string

	for _, steward := range dataProviders {
		provider := s.resolveProvider(steward)
		text, found, err := provider.GetLayer2Phrases(steward)
		if err != nil {
			s.logger.Warn("failed to load Layer-2 phrases for steward; treating as missing",
				zap.String("steward", steward),
				zap.String("provider", provider.Name()),
				zap.Error(err),
			)
			missing = append(missing, steward)
			continue
		}
		if !found {
			missing = append(missing, steward)
			continue
		}
		phrases[steward] = text
	}

	return phrases, missing
}

// loadSharedRules reads the Layer-2 shared rules from etcd. An empty result is
// allowed (the layered evaluation can still run against the per-steward
// phrases) but is logged as a warning since the system is not configured as
// expected.
func (s *ValidationService) loadSharedRules() (string, error) {
	if s.rulesRepo == nil {
		return "", nil
	}
	text, found, err := s.rulesRepo.GetSharedAgreementRules()
	if err != nil {
		return "", fmt.Errorf("retrieving shared rules: %w", err)
	}
	if !found {
		s.logger.Warn("Layer-2 shared rules are missing in etcd; query facts may not be derivable")
	}
	return text, nil
}

// resolveProvider determines which AgreementPhraseProvider to use for a
// steward. The provider config in etcd selects between eFLINT-format phrases
// and legacy JSON; in either case the result feeds the same canonical
// layered execution path.
func (s *ValidationService) resolveProvider(steward string) AgreementPhraseProvider {
	if s.providerConfigRepo == nil {
		return s.legacyProvider
	}

	config, found, err := s.providerConfigRepo.GetProviderConfig(steward)
	if err != nil {
		s.logger.Warn("failed to retrieve provider config; defaulting to legacy",
			zap.String("steward", steward),
			zap.Error(err),
		)
		return s.legacyProvider
	}
	if !found {
		return s.legacyProvider
	}

	if config.ValidationStrategy == api.ValidationStrategyEflint && s.eflintProvider != nil {
		return s.eflintProvider
	}
	return s.legacyProvider
}

// applyEvaluation maps the reasoner's per-steward decisions onto the
// pb.ValidationResponse. Stewards with permitted-at-steward = true land in
// ValidDataproviders together with their matched archetypes / compute
// providers; everyone else is added to InvalidDataproviders.
func (s *ValidationService) applyEvaluation(
	eval *reasoner.RequestApprovalResult,
	request *pb.RequestApproval,
	response *pb.ValidationResponse,
) {
	if eval == nil {
		return
	}
	response.ValidArchetypes.UserName = request.User.UserName
	for _, steward := range request.DataProviders {
		decision, ok := eval.PerSteward[steward]
		if !ok {
			continue
		}
		if !decision.Permitted {
			if !containsString(response.InvalidDataproviders, steward) {
				response.InvalidDataproviders = append(response.InvalidDataproviders, steward)
			}
			s.logger.Debug("steward marked invalid by reasoner",
				zap.String("steward", steward),
				zap.String("reason", decision.Reason),
			)
			continue
		}
		response.ValidArchetypes.Archetypes[steward] = &pb.UserAllowedArchetypes{
			Archetypes: decision.Archetypes,
		}
		response.ValidDataproviders[steward] = &pb.DataProvider{
			Archetypes:       decision.Archetypes,
			ComputeProviders: decision.ComputeProviders,
		}
	}
}

// markAllInvalid is invoked when an unrecoverable error short-circuits the
// evaluation (no shared rules, reasoner error). Every requested steward is
// marked invalid so the orchestrator does not see a partial success.
func (s *ValidationService) markAllInvalid(dataProviders []string, response *pb.ValidationResponse, reason string) {
	for _, steward := range dataProviders {
		if containsString(response.InvalidDataproviders, steward) {
			continue
		}
		response.InvalidDataproviders = append(response.InvalidDataproviders, steward)
		s.logger.Debug("steward marked invalid",
			zap.String("steward", steward),
			zap.String("reason", reason),
		)
	}
}

// ValidateAndPersistAgreement resolves the steward's provider and delegates
// validation + persistence to it.
func (s *ValidationService) ValidateAndPersistAgreement(ctx context.Context, steward string, payload []byte) error {
	provider := s.resolveProvider(steward)
	s.logger.Info("Validating and persisting agreement",
		zap.String("steward", steward),
		zap.String("provider", provider.Name()),
	)
	return provider.ValidateAndPersist(ctx, steward, payload)
}

// GetAllowedClausesForSteward loads the steward's Layer-2 phrases (via the
// configured provider) plus the Layer-2 shared rules, asks the reasoner to
// introspect the steward-supports-* / relation-allows-* facts, and returns
// the resulting StewardClauses snapshot. If `requester` is non-empty the
// returned clauses are narrowed to that requester's relation only. Returns
// (nil, nil) when the steward has no agreement registered.
//
// This is read-only: no Layer 3 is pushed and no Acts are fired, so the
// endpoint is safe to expose to a policy engineer.
func (s *ValidationService) GetAllowedClausesForSteward(ctx context.Context, steward, requester string) (*reasoner.StewardClauses, error) {
	if steward == "" {
		return nil, fmt.Errorf("steward is required")
	}

	provider := s.resolveProvider(steward)
	phrases, found, err := provider.GetLayer2Phrases(steward)
	if err != nil {
		s.logger.Warn("failed to load Layer-2 phrases for steward",
			zap.String("steward", steward),
			zap.String("provider", provider.Name()),
			zap.Error(err),
		)
		return nil, fmt.Errorf("loading Layer-2 phrases for %s: %w", steward, err)
	}
	if !found {
		return nil, nil
	}

	sharedRules, err := s.loadSharedRules()
	if err != nil {
		return nil, err
	}

	clauses, err := s.reasoner.IntrospectStewardClauses(ctx, reasoner.IntrospectStewardClausesParams{
		Steward:        steward,
		SharedRules:    sharedRules,
		StewardPhrases: phrases,
	})
	if err != nil {
		return nil, fmt.Errorf("introspecting steward clauses: %w", err)
	}

	if requester != "" && clauses != nil {
		filtered := make([]reasoner.RequesterClauses, 0, 1)
		for _, rel := range clauses.Relations {
			if rel.Requester == requester {
				filtered = append(filtered, rel)
				break
			}
		}
		clauses.Relations = filtered
	}

	return clauses, nil
}

func containsString(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}
