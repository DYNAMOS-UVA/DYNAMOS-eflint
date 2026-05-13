package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Jorrit05/DYNAMOS/pkg/api"
	"github.com/Jorrit05/DYNAMOS/pkg/etcd"
	"github.com/Jorrit05/DYNAMOS/pkg/lib"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
	"github.com/google/uuid"
)

// /agents/jobs/SURF/jorrit.stutterheim@cloudnation.nl/jorrit-stutterheim-43ea82da
// {"archetype_id":"dataThroughTtp","request_type":"sqlDataRequest","role":"computeProvider","user":{"id":"12324","user_name":"jorrit.stutterheim@cloudnation.nl"},"data_providers":["UVA"],"destination_queue":"SURF-in","job_name":"jorrit-stutterheim-43ea82da","local_job_name":"jorrit-stutterheim-43ea82dasurf1"}
// /agents/jobs/SURF/queueInfo/jorrit-stutterheim-43ea82dasurf1
// jorrit-stutterheim-43ea82dasurf1
// /agents/jobs/UVA/jorrit.stutterheim@cloudnation.nl/jorrit-stutterheim-43ea82da
// {"archetype_id":"dataThroughTtp","request_type":"sqlDataRequest","role":"dataProvider","user":{"id":"12324","user_name":"jorrit.stutterheim@cloudnation.nl"},"destination_queue":"UVA-in","job_name":"jorrit-stutterheim-43ea82da","local_job_name":"jorrit-stutterheim-43ea82dauva1"}
// /agents/jobs/UVA/queueInfo/jorrit-stutterheim-43ea82dauva1
// jorrit-stutterheim-43ea82dauva1

func deleteJobInfo(jobNames []string, userName string, changedAgreementName string) {
	ctx := context.Background()
	// get all online agents
	var agents *lib.AgentDetails
	key := "/agents/online/"
	activeAgents, err := etcd.GetPrefixListEtcd(etcdClient, key, agents)

	if err != nil {
		logger.Sugar().Warnf("error get agents: %v", err)
	}

	for _, job := range jobNames {

		for _, agent := range activeAgents {
			jobInfoKey := fmt.Sprintf("/agents/jobs/%s/%s/%s", agent.Name, userName, job)

			resp, err := etcdClient.Get(ctx, jobInfoKey)
			if err != nil {
				logger.Sugar().Errorf("error getting value from etcd: %v", err)
			}

			if len(resp.Kvs) == 0 {
				continue
			}

			compositionRequest := &pb.CompositionRequest{}
			err = json.Unmarshal(resp.Kvs[0].Value, compositionRequest)
			if err != nil {
				logger.Sugar().Errorf("failed to unmarshal JSON: %v", err)
				return
			}

			key := fmt.Sprintf("/agents/jobs/%s/queueInfo/%s", agent.Name, compositionRequest.LocalJobName)
			_, err = etcdClient.Delete(ctx, key)
			if err != nil {
				logger.Sugar().Errorf("failed to delete key: %v", err)
				continue
			}

		}
	}
}

// checkAllJobs re-evaluates running jobs for every steward that has at least
// one /agents/jobs/<steward>/... entry. Used after a shared-rules update,
// which affects derivations for every agreement.
func checkAllJobs() {
	rootKey := "/agents/jobs/"
	keys, err := etcd.GetFullKeysFromPrefix(etcdClient, rootKey, etcd.WithMaxElapsedTime(2*time.Second))
	if err != nil {
		logger.Sugar().Warnf("error listing job keys for global re-evaluation: %v", err)
		return
	}

	stewards := make(map[string]struct{})
	for _, k := range keys {
		trimmed := strings.TrimPrefix(k, rootKey)
		if trimmed == "" {
			continue
		}
		parts := strings.SplitN(trimmed, "/", 2)
		stewards[parts[0]] = struct{}{}
	}

	if len(stewards) == 0 {
		logger.Debug("no active stewards with running jobs; nothing to re-evaluate")
		return
	}

	for steward := range stewards {
		checkJobs(steward)
	}
}

func checkJobs(agreementName string) {
	key := fmt.Sprintf("/agents/jobs/%s/", agreementName)
	jobKeys, err := etcd.GetFullKeysFromPrefix(etcdClient, key, etcd.WithMaxElapsedTime(2*time.Second))
	if err != nil {
		logger.Sugar().Warnf("error get jobs: %v", err)
	}

	userJobs := make(map[string][]string)
	for _, k := range jobKeys {
		parts := strings.Split(k, "/")
		if len(parts) >= 6 {
			userName := parts[4]
			if userName == "queueInfo" {
				continue
			}
			jobName := parts[5]
			userJobs[userName] = append(userJobs[userName], jobName)
		}
	}

	logger.Sugar().Debugf("checkJobs: agreement=%q found %d user(s) with active jobs", agreementName, len(userJobs))
	for userName, jobNames := range userJobs {
		logger.Sugar().Debugf("checkJobs: user=%q jobs(%d)=%v", userName, len(jobNames), jobNames)
		if len(jobNames) == 0 {
			logger.Debug("no active jobs for this user")
			continue
		}
		evaluateArchetypeInActiveJobs(jobNames, agreementName, userName, c)
	}
}

func evaluateArchetypeInActiveJobs(jobNames []string, agreementName string, relationName string, c pb.RabbitMQClient) {
	logger.Debug("starting evaluateArchetypeInActiveJobs")
	ctx := context.Background()

	for _, job := range jobNames {

		jobInfoKey := fmt.Sprintf("/agents/jobs/%s/%s/%s", agreementName, relationName, job)

		resp, err := etcdClient.Get(ctx, jobInfoKey)
		if err != nil {
			logger.Sugar().Errorf("error getting value from etcd: %v", err)
			continue
		}

		if len(resp.Kvs) == 0 {
			logger.Warn("this should not happen")
			continue
		}

		currentRegisteredJob := &pb.CompositionRequest{}
		err = json.Unmarshal(resp.Kvs[0].Value, currentRegisteredJob)
		if err != nil {
			logger.Sugar().Errorf("error unmarshalling jobinfo: %v", err)
		}

		policyUpdate := &pb.PolicyUpdate{
			Type:            "policyUpdate",
			User:            &pb.User{Id: relationName, UserName: relationName},
			RequestMetadata: &pb.RequestMetadata{DestinationQueue: "policyEnforcer-in"},
		}

		correlationId := uuid.New().String()
		policyUpdate.RequestMetadata.CorrelationId = correlationId

		agentsWithThisJob := make(map[string]*pb.CompositionRequest)

		ctx = getJobAcrossAgents(ctx, agentsWithThisJob, job, relationName)

		for k, v := range agentsWithThisJob {
			if v.Role == "all" || v.Role == "dataProvider" {
				policyUpdate.DataProviders = append(policyUpdate.DataProviders, k)
			}
		}

		policyUpdateMutex.Lock()
		policyUpdateMap[policyUpdate.RequestMetadata.CorrelationId] = agentsWithThisJob
		policyUpdateMutex.Unlock()
		c.SendPolicyUpdate(ctx, policyUpdate)
	}
}

// deleteJobAcrossAgents removes every etcd entry (job info + queueInfo) for the
// given job across all agents that were holding it. agentsWithThisJob is the
// same map that was built by getJobAcrossAgents so it already contains both
// JobName and LocalJobName per agent.
func deleteJobAcrossAgents(ctx context.Context, agentsWithThisJob map[string]*pb.CompositionRequest, userName string) {
	for agent, jobData := range agentsWithThisJob {
		jobKey := fmt.Sprintf("/agents/jobs/%s/%s/%s", agent, userName, jobData.JobName)
		if _, err := etcdClient.Delete(ctx, jobKey); err != nil {
			logger.Sugar().Warnf("deleteJobAcrossAgents: error deleting job key %s: %v", jobKey, err)
		} else {
			logger.Sugar().Debugf("deleteJobAcrossAgents: deleted job key %s", jobKey)
		}

		queueInfoKey := fmt.Sprintf("/agents/jobs/%s/queueInfo/%s", agent, jobData.LocalJobName)
		if _, err := etcdClient.Delete(ctx, queueInfoKey); err != nil {
			logger.Sugar().Warnf("deleteJobAcrossAgents: error deleting queueInfo key %s: %v", queueInfoKey, err)
		} else {
			logger.Sugar().Debugf("deleteJobAcrossAgents: deleted queueInfo key %s", queueInfoKey)
		}
	}
}

func processPolicyUpdate(ctx context.Context, agentsWithThisJob map[string]*pb.CompositionRequest, policyUpdate *pb.PolicyUpdate) {
	logger.Sugar().Debugf("processPolicyUpdate")

	vr := policyUpdate.ValidationResponse

	// If the policy enforcer returned no valid data providers at all, every
	// running job for this user must be cleaned up — there is nothing left to
	// route to.
	if vr == nil || len(vr.ValidDataproviders) == 0 {
		logger.Sugar().Infof("processPolicyUpdate: no valid data providers in ValidationResponse — deleting all active jobs for user %q", policyUpdate.User.UserName)
		deleteJobAcrossAgents(ctx, agentsWithThisJob, policyUpdate.User.UserName)
		return
	}

	// TODO: Kinda threw this in without testing..
	authorizedProviders, err := getAuthorizedProviders(policyUpdate.ValidationResponse)
	if err != nil {
		logger.Sugar().Errorf("error getAuthorizedProviders : %v", err)
	}

	archetype, err := chooseArchetype(policyUpdate.ValidationResponse, authorizedProviders)
	if err != nil {
		logger.Sugar().Errorf("error choosing archetype: %v", err)
	}

	logger.Sugar().Debugf("New archetype: %v", archetype)

	// Get the archetype configuration from etcd
	var archetypeConfig api.Archetype
	_, err = etcd.GetAndUnmarshalJSON(etcdClient, fmt.Sprintf("/archetypes/%s", archetype), &archetypeConfig)
	if err != nil {
		logger.Sugar().Errorf("error choosing archetype: %v", err)
		return
	}

	// technically now, this shouldn't be necessary
	computeProviderAlready := false
	var ttp lib.AgentDetails
	for agent, currentData := range agentsWithThisJob {
		key := fmt.Sprintf("/agents/jobs/%s/%s/%s", agent, policyUpdate.User.UserName, currentData.JobName)

		// Data-provider roles that are no longer in ValidDataproviders have had
		// their authorisation revoked — delete immediately regardless of archetype.
		// computeProvider agents are intentionally absent from ValidDataproviders
		// and are handled further below.
		if currentData.Role != "computeProvider" {
			if _, isValid := vr.ValidDataproviders[agent]; !isValid {
				logger.Sugar().Infof("processPolicyUpdate: agent %q is no longer a valid data provider — removing job entry", agent)
				if _, err := etcdClient.Delete(ctx, key); err != nil {
					logger.Sugar().Warnf("error deleting key from etcd: %v", err)
				}
				continue
			}
		}

		// Archetype unchanged for this agent — nothing to update.
		if currentData.ArchetypeId == archetype {
			logger.Sugar().Debugf("processPolicyUpdate: agent %q already on archetype %q, skipping", agent, archetype)
			continue
		}

		if archetypeConfig.ComputeProvider != "other" {
			// computeToData archetype
			if currentData.Role == "computeProvider" {
				if _, err := etcdClient.Delete(ctx, key); err != nil {
					logger.Sugar().Warnf("error deleting key from etcd: %v", err)
				}
				continue
			}

			newData := currentData
			newData.ArchetypeId = archetype
			newData.Role = "all"
			newData.DataProviders = []string{}
			if err := etcd.SaveStructToEtcd[*pb.CompositionRequest](etcdClient, key, newData); err != nil {
				logger.Sugar().Errorf("Error saving struct to etcd: %v", err)
				return
			}
			computeProviderAlready = true
		} else {
			// dataThroughTtp archetype
			var err error
			ttp, err = chooseThirdParty(policyUpdate.ValidationResponse)
			if err != nil {
				logger.Sugar().Errorf("Error choosing third party: %v", err)
				return
			}

			if currentData.Role == "computeProvider" && agent == ttp.Name {
				computeProviderAlready = true
				continue
			} else if currentData.Role == "computeProvider" && agent != ttp.Name {
				if _, err := etcdClient.Delete(ctx, key); err != nil {
					logger.Sugar().Warnf("error deleting key from etcd: %v", err)
				}
				continue
			}

			if currentData.Role == "all" {
				newData := currentData
				newData.ArchetypeId = archetype
				newData.Role = "dataProvider"
				newData.DataProviders = []string{}
				if err = etcd.SaveStructToEtcd[*pb.CompositionRequest](etcdClient, key, newData); err != nil {
					logger.Sugar().Errorf("Error saving struct to etcd: %v", err)
					return
				}
			}
		}
	}

	if !computeProviderAlready && archetype == "dataThroughTtp" {
		compositionRequest := &pb.CompositionRequest{}
		compositionRequest.User = policyUpdate.User
		tmpDataProvider := []string{}

		for key := range policyUpdate.ValidationResponse.ValidDataproviders {
			tmpDataProvider = append(tmpDataProvider, key)
		}
		compositionRequest.Role = "computeProvider"
		compositionRequest.DataProviders = tmpDataProvider
		compositionRequest.ArchetypeId = archetype
		for _, v := range agentsWithThisJob {
			compositionRequest.RequestType = v.RequestType
			compositionRequest.JobName = v.JobName
			break
		}

		if ttp.RoutingKey == "" {
			logger.Sugar().Errorf("processPolicyUpdate: ttp routing key is empty, cannot send composition request for job %q", compositionRequest.JobName)
			return
		}
		compositionRequest.DestinationQueue = ttp.RoutingKey

		c.SendCompositionRequest(ctx, compositionRequest)
	}
}

func getJobAcrossAgents(ctx context.Context, targetMap map[string]*pb.CompositionRequest, jobName string, userName string) context.Context {

	var agents *lib.AgentDetails
	key := "/agents/online/"
	activeAgents, err := etcd.GetPrefixListEtcd(etcdClient, key, agents)
	if err != nil {
		logger.Sugar().Warnf("error get agents: %v", err)
	}

	for _, agent := range activeAgents {

		key := fmt.Sprintf("/agents/jobs/%s/%s/%s", agent.Name, userName, jobName)

		resp, err := etcdClient.Get(ctx, key)
		if err != nil {
			logger.Sugar().Errorf("error getting value from etcd: %v", err)
			continue
		}

		if len(resp.Kvs) == 0 {
			logger.Sugar().Debugw("no value found for", "key", key)
			continue
		}

		agentsConfiguration := &pb.CompositionRequest{}
		err = json.Unmarshal(resp.Kvs[0].Value, agentsConfiguration)
		if err != nil {
			logger.Sugar().Errorf("error unmarshalling jobinfo: %v", err)
			continue
		}

		targetMap[agent.Name] = agentsConfiguration
	}

	return ctx
}

func handleRequestApproval(ctx context.Context, validationResponse *pb.ValidationResponse) {
	result := &pb.RequestApprovalResponse{Type: "requestApprovalResponse", RequestMetadata: &pb.RequestMetadata{DestinationQueue: "api-gateway-in"}}

	// Always populate User so the api-gateway can route the response by User.Id.
	result.User = validationResponse.User

	authorizedProviders, err := getAuthorizedProviders(validationResponse)
	if err != nil {
		result.Error = err.Error()
		c.SendRequestApprovalResponse(ctx, result)
		return
	}

	if len(authorizedProviders) == 0 {
		logger.Sugar().Warn("Request was processed, but no agreements or available dataproviders have been found")
		result.Error = "Request was processed, but no agreements or available dataproviders have been found"
		c.SendRequestApprovalResponse(ctx, result)
		return
	}

	// TODO: Might be able to improve processing by converting functions to go routines
	// Seems a bit tricky though due to the response writer.

	compositionRequest := &pb.CompositionRequest{}
	compositionRequest.User = &pb.User{}
	userTargets, ctx, err := startCompositionRequest(ctx, validationResponse, authorizedProviders, compositionRequest)
	if err != nil {
		switch e := err.(type) {
		case *UnauthorizedProviderError:
			logger.Sugar().Warn("Unauthorized provider error: %v", e)
			return
		default:
			logger.Sugar().Errorf("Error starting composition request: %v", err)
			return
		}
	}

	result.Auth = &pb.Auth{}
	result.User = &pb.User{}

	result.Auth = validationResponse.Auth
	result.User = validationResponse.User

	result.AuthorizedProviders = make(map[string]string)
	result.AuthorizedProviders = userTargets
	result.JobId = compositionRequest.JobName

	c.SendRequestApprovalResponse(ctx, result)
}
