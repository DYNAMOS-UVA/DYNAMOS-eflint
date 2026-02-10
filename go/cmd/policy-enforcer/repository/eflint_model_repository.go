package repository

import (
	"errors"
	"fmt"

	"github.com/Jorrit05/DYNAMOS/pkg/etcd"
	clientv3 "go.etcd.io/etcd/client/v3"
)

const eflintModelKeyPrefix = "/policyEnforcer/eflintModels/"

// EflintModelRepository defines the interface for retrieving eFLINT specification text.
// The raw eFLINT file content is stored in etcd by the orchestrator at startup
// (see orchestrator/etcd_config.go) and retrieved here for use with the phrases command.
type EflintModelRepository interface {
	// GetEflintModel retrieves the raw eFLINT specification text for a provider.
	// The modelName typically corresponds to the provider/organization name (e.g., "VU", "UVA").
	// Returns the specification text and a boolean indicating if it was found.
	GetEflintModel(modelName string) (string, bool, error)
}

// EtcdEflintModelRepository implements EflintModelRepository using etcd as the backend.
type EtcdEflintModelRepository struct {
	client *clientv3.Client
}

// NewEtcdEflintModelRepository creates a new EtcdEflintModelRepository.
func NewEtcdEflintModelRepository(client *clientv3.Client) *EtcdEflintModelRepository {
	return &EtcdEflintModelRepository{
		client: client,
	}
}

// GetEflintModel retrieves the raw eFLINT specification text for a provider from etcd.
// The specification is stored at /policyEnforcer/eflintModels/{modelName} by the orchestrator.
func (r *EtcdEflintModelRepository) GetEflintModel(modelName string) (string, bool, error) {
	key := eflintModelKeyPrefix + modelName
	output, err := etcd.GetValueFromEtcd(r.client, key)
	if err != nil {
		var notFound *etcd.ErrKeyNotFound
		if errors.As(err, &notFound) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("error retrieving eFLINT model from etcd for %s: %w", modelName, err)
	}

	if output == "" {
		return "", false, nil
	}

	return output, true, nil
}
