package repository

import (
	"encoding/json"
	"fmt"

	"github.com/Jorrit05/DYNAMOS/pkg/api"
	"github.com/Jorrit05/DYNAMOS/pkg/etcd"
	clientv3 "go.etcd.io/etcd/client/v3"
)

const agreementKeyPrefix = "/policyEnforcer/agreements/"

// EtcdAgreementRepository implements AgreementRepository using etcd as the backend.
type EtcdAgreementRepository struct {
	client *clientv3.Client
}

// NewEtcdAgreementRepository creates a new EtcdAgreementRepository.
func NewEtcdAgreementRepository(client *clientv3.Client) *EtcdAgreementRepository {
	return &EtcdAgreementRepository{
		client: client,
	}
}

// GetAgreement retrieves an agreement for a specific data steward from etcd.
func (r *EtcdAgreementRepository) GetAgreement(steward string) (*api.Agreement, bool, error) {
	key := agreementKeyPrefix + steward
	output, err := etcd.GetValueFromEtcd(r.client, key)
	if err != nil {
		return nil, false, fmt.Errorf("error retrieving agreement from etcd for steward %s: %w", steward, err)
	}

	if output == "" {
		return nil, false, nil
	}

	var agreement api.Agreement
	if err := json.Unmarshal([]byte(output), &agreement); err != nil {
		return nil, false, fmt.Errorf("error unmarshalling agreement for steward %s: %w", steward, err)
	}

	return &agreement, true, nil
}
