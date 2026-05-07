package reasoner

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/eflint"
	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/repository"
)

// EflintReasoner implements the layered Reasoner interface on top of an
// eFLINT instance pool. Each evaluation acquires one pool entry that already
// has the Layer-1 interface policy loaded as its boot model; the layered
// evaluation logic itself lives in eflint_layered.go.
type EflintReasoner struct {
	pool      *eflint.InstancePool
	modelRepo repository.EflintModelRepository
	logger    *zap.Logger
}

// NewEflintReasoner creates a new eFLINT-based reasoner backed by an instance pool.
func NewEflintReasoner(pool *eflint.InstancePool, modelRepo repository.EflintModelRepository, logger *zap.Logger) *EflintReasoner {
	return &EflintReasoner{
		pool:      pool,
		modelRepo: modelRepo,
		logger:    logger,
	}
}

// Name returns the name of this reasoner.
func (r *EflintReasoner) Name() string {
	return "eflint"
}

// IsRunning checks if the eFLINT instance pool is operational.
func (r *EflintReasoner) IsRunning() bool {
	return r.pool.GetTargetSize() > 0
}

// ValidateAndPersistModel validates a raw eFLINT model by sending it to a
// pool instance (so syntactic errors and unbound references surface as
// SendPhrases errors) and, on success, persists it under
// /policyEnforcer/eflintModels/{organization}.
func (r *EflintReasoner) ValidateAndPersistModel(ctx context.Context, organization string, modelText string) error {
	entry, err := r.pool.Acquire()
	if err != nil {
		return fmt.Errorf("failed to acquire eFLINT instance: %w", err)
	}
	defer r.pool.Release(entry)

	if _, err := entry.Manager.SendPhrases(modelText); err != nil {
		r.logger.Error("invalid eFLINT specification", zap.Error(err))
		return fmt.Errorf("invalid eFLINT specification: %w", err)
	}

	if err := r.modelRepo.SaveEflintModel(organization, modelText); err != nil {
		r.logger.Error("failed to save eFLINT model", zap.Error(err))
		return fmt.Errorf("failed to save eFLINT model: %w", err)
	}

	return nil
}

// Compile-time check that EflintReasoner satisfies the layered Reasoner interface.
var _ Reasoner = (*EflintReasoner)(nil)
