package main

import (
	"fmt"
	"io/ioutil"
	"path/filepath"

	"github.com/Jorrit05/DYNAMOS/pkg/api"
	"github.com/Jorrit05/DYNAMOS/pkg/etcd"
	"github.com/Jorrit05/DYNAMOS/pkg/lib"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
	"go.uber.org/zap"
)

func registerPolicyEnforcerConfiguration() {
	logger.Debug("Start registerPolicyEnforcerConfiguration")
	// Load request types
	var requestsTypes []api.RequestType
	lib.UnmarshalJsonFile(requestTypeConfigLocation, &requestsTypes)

	for _, requestType := range requestsTypes {
		etcd.SaveStructToEtcd[api.RequestType](etcdClient, fmt.Sprintf("/requestTypes/%s", requestType.Name), requestType)
	}

	// Load archetypes
	var archeTypes []api.Archetype
	lib.UnmarshalJsonFile(archetypeConfigLocation, &archeTypes)

	for _, archeType := range archeTypes {
		etcd.SaveStructToEtcd[api.Archetype](etcdClient, fmt.Sprintf("/archetypes/%s", archeType.Name), archeType)
	}

	// Load labels and allowedOutputs (microservice.json)
	var microservices []api.MicroserviceMetadata

	lib.UnmarshalJsonFile(microserviceMetadataConfigLocation, &microservices)

	for _, microservice := range microservices {
		etcd.SaveStructToEtcd[api.MicroserviceMetadata](etcdClient, fmt.Sprintf("/microservices/%s/chainMetadata", microservice.Name), microservice)
	}

	// Load agreemnents  (agreemnents.json)
	var agreements []api.Agreement

	lib.UnmarshalJsonFile(agreementsConfigLocation, &agreements)

	for _, agreement := range agreements {
		etcd.SaveStructToEtcd[api.Agreement](etcdClient, fmt.Sprintf("/policyEnforcer/agreements/%s", agreement.Name), agreement)
	}

	// Load agreemnents  (agreemnents.json)
	var datasets []*pb.Dataset

	lib.UnmarshalJsonFile(dataSetConfigLocation, &datasets)

	for _, dataset := range datasets {
		etcd.SaveStructToEtcd[*pb.Dataset](etcdClient, fmt.Sprintf("/datasets/%s", dataset.Name), dataset)
	}

	// Load   optional_microservices.json
	var optionalServices []api.OptionalServices

	lib.UnmarshalJsonFile(optionalMSConfigLocation, &optionalServices)

	for _, services := range optionalServices {
		for k, msList := range services.Types {
			for _, ms := range msList {
				key := fmt.Sprintf("/agents/%s/requestType/%s/%s ", services.DataSteward, k, ms)
				etcd.PutValueToEtcd(etcdClient, key, ms)
			}
		}
	}

	// Load eFLINT layered specifications.
	//
	// File-name routing convention:
	//   01_interface_policy.eflint   -> /policyEnforcer/eflintLayer1/interface
	//                                    (informational; the policy-enforcer
	//                                    binary embeds Layer 1 too)
	//   02_agreement_rules.eflint    -> /policyEnforcer/eflintRules/shared
	//                                    (Layer 2 shared rules)
	//   <steward>.eflint             -> /policyEnforcer/eflintModels/<steward>
	//                                    (Layer 2 per-steward agreement)
	logger.Debug("Loading eFLINT models from directory", zap.String("directory", eflintModelsDirectory))
	eflintFiles, err := ioutil.ReadDir(eflintModelsDirectory)
	if err != nil {
		logger.Error("Failed to read eFLINT models directory", zap.Error(err))
	} else {
		for _, file := range eflintFiles {
			if file.IsDir() || filepath.Ext(file.Name()) != ".eflint" {
				continue
			}
			filePath := filepath.Join(eflintModelsDirectory, file.Name())
			content, readErr := ioutil.ReadFile(filePath)
			if readErr != nil {
				logger.Error("Failed to read eFLINT model file", zap.String("file", file.Name()), zap.Error(readErr))
				continue
			}
			modelName := file.Name()[:len(file.Name())-len(".eflint")]

			var key string
			switch modelName {
			case "01_interface_policy":
				key = "/policyEnforcer/eflintLayer1/interface"
			case "02_agreement_rules":
				key = "/policyEnforcer/eflintRules/shared"
			default:
				key = fmt.Sprintf("/policyEnforcer/eflintModels/%s", modelName)
			}
			etcd.PutValueToEtcd(etcdClient, key, string(content))
			logger.Debug("Loaded eFLINT spec", zap.String("name", modelName), zap.String("etcdKey", key))
		}
	}

	// Load provider configs (provider_configs.json)
	// Note: eFLINT states are bootstrapped on-demand by the policy-enforcer,
	// not by the orchestrator, since the eflint-server binary is only available
	// in the policy-enforcer container.
	var providerConfigs []api.ProviderValidationConfig

	lib.UnmarshalJsonFile(providerConfigsLocation, &providerConfigs)

	for _, config := range providerConfigs {
		etcd.SaveStructToEtcd[api.ProviderValidationConfig](etcdClient, fmt.Sprintf("/policyEnforcer/configs/%s", config.Name), config)
	}
}
