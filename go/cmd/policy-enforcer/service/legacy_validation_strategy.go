package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/repository"
	"github.com/Jorrit05/DYNAMOS/pkg/api"
	"go.uber.org/zap"
)

// LegacyAgreementPhraseProvider is the AgreementPhraseProvider for stewards
// whose policy is stored as a legacy JSON agreement (api.Agreement) under
// /policyEnforcer/agreements/{steward}.
//
// On every evaluation, the JSON agreement is loaded from etcd and translated
// into a Layer-2 eFLINT phrase block (see TranslateLegacyAgreement) so the
// downstream reasoner sees a canonical layered specification regardless of the
// original storage format.
type LegacyAgreementPhraseProvider struct {
	agreementRepo repository.AgreementRepository
	logger        *zap.Logger
}

// NewLegacyAgreementPhraseProvider creates a new legacy phrase provider.
func NewLegacyAgreementPhraseProvider(
	agreementRepo repository.AgreementRepository,
	logger *zap.Logger,
) *LegacyAgreementPhraseProvider {
	return &LegacyAgreementPhraseProvider{
		agreementRepo: agreementRepo,
		logger:        logger,
	}
}

// Name returns the provider name.
func (p *LegacyAgreementPhraseProvider) Name() string {
	return "legacy"
}

// GetLayer2Phrases loads the JSON agreement from etcd and translates it.
func (p *LegacyAgreementPhraseProvider) GetLayer2Phrases(steward string) (string, bool, error) {
	agreement, found, err := p.agreementRepo.GetAgreement(steward)
	if err != nil {
		p.logger.Error("failed to retrieve legacy agreement",
			zap.String("steward", steward),
			zap.Error(err),
		)
		return "", false, fmt.Errorf("failed to retrieve legacy agreement for %s: %w", steward, err)
	}
	if !found {
		return "", false, nil
	}
	return TranslateLegacyAgreement(agreement), true, nil
}

// ValidateAndPersist validates a JSON legacy agreement and saves it.
func (p *LegacyAgreementPhraseProvider) ValidateAndPersist(ctx context.Context, steward string, payload []byte) error {
	var agreement api.Agreement
	if err := json.Unmarshal(payload, &agreement); err != nil {
		p.logger.Error("failed to unmarshal legacy agreement", zap.Error(err))
		return fmt.Errorf("invalid legacy agreement JSON: %w", err)
	}

	if agreement.Name != steward {
		p.logger.Error("agreement name mismatch",
			zap.String("expected", steward),
			zap.String("actual", agreement.Name),
		)
		return fmt.Errorf("agreement name mismatch: expected %s, got %s", steward, agreement.Name)
	}

	if err := p.agreementRepo.SaveAgreement(steward, &agreement); err != nil {
		p.logger.Error("failed to save legacy agreement", zap.Error(err))
		return fmt.Errorf("failed to save legacy agreement: %w", err)
	}

	return nil
}
