package service

import (
	"context"
	"fmt"

	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/reasoner"
	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/repository"
	"go.uber.org/zap"
)

// EflintAgreementPhraseProvider is the AgreementPhraseProvider for stewards
// whose policy is stored as raw Layer-2 eFLINT phrases under
// /policyEnforcer/eflintModels/{steward}.
//
// GetLayer2Phrases simply returns the stored eFLINT text; ValidateAndPersist
// asks the reasoner to validate the syntax (by sending the phrases to a pool
// instance) and then stores them.
type EflintAgreementPhraseProvider struct {
	modelRepo repository.EflintModelRepository
	reasoner  reasoner.Reasoner
	logger    *zap.Logger
}

// NewEflintAgreementPhraseProvider creates a new eFLINT phrase provider.
func NewEflintAgreementPhraseProvider(
	modelRepo repository.EflintModelRepository,
	r reasoner.Reasoner,
	logger *zap.Logger,
) *EflintAgreementPhraseProvider {
	return &EflintAgreementPhraseProvider{
		modelRepo: modelRepo,
		reasoner:  r,
		logger:    logger,
	}
}

// Name returns the provider name.
func (p *EflintAgreementPhraseProvider) Name() string {
	return "eflint"
}

// GetLayer2Phrases retrieves the stored eFLINT phrase block for the steward.
func (p *EflintAgreementPhraseProvider) GetLayer2Phrases(steward string) (string, bool, error) {
	text, found, err := p.modelRepo.GetEflintModel(steward)
	if err != nil {
		p.logger.Error("failed to retrieve eFLINT model",
			zap.String("steward", steward),
			zap.Error(err),
		)
		return "", false, fmt.Errorf("failed to retrieve eFLINT model for %s: %w", steward, err)
	}
	return text, found, nil
}

// ValidateAndPersist asks the reasoner to validate the eFLINT phrases and
// then saves them to etcd.
func (p *EflintAgreementPhraseProvider) ValidateAndPersist(ctx context.Context, steward string, payload []byte) error {
	return p.reasoner.ValidateAndPersistModel(ctx, steward, string(payload))
}
