package main

import (
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/Jorrit05/DYNAMOS/pkg/api"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// sharedRulesResource is the path segment that identifies the global Layer-2
// rules update endpoint (vs. a steward name).
const sharedRulesResource = "sharedRules"

func archetypesHandler(etcdClient *clientv3.Client, root string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger.Info("Entering archetypesHandler")
		switch r.Method {
		case http.MethodGet:
			// Call your handler for GET
			api.GenericGetHandler[api.Archetype](w, r, etcdClient, "/archetypes")
		case http.MethodPut:
			// Call your handler for PUT
			archetype := &api.Archetype{}
			api.GenericPutToEtcd[api.Archetype](w, r, etcdClient, "/archetypes", archetype)
		default:
			// Respond with a 405 'Method Not Allowed' HTTP response if the method isn't supported
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func requestTypesHandler(etcdClient *clientv3.Client, etcdRoot string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			// Call your handler for GET
			api.GenericGetHandler[api.RequestType](w, r, etcdClient, etcdRoot)
		case http.MethodPut:
			// Call your handler for PUT
			requestType := &api.RequestType{}
			api.GenericPutToEtcd[api.RequestType](w, r, etcdClient, etcdRoot, requestType)
		default:
			// Respond with a 405 'Method Not Allowed' HTTP response if the method isn't supported
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func microserviceMetadataHandler(etcdClient *clientv3.Client, etcdRoot string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			// Call your handler for GET
			api.GenericGetHandler[api.MicroserviceMetadata](w, r, etcdClient, etcdRoot)
		case http.MethodPut:
			// Call your handler for PUT
			msMetadata := &api.MicroserviceMetadata{}
			api.GenericPutToEtcd[api.MicroserviceMetadata](w, r, etcdClient, etcdRoot, msMetadata)
		default:
			// Respond with a 405 'Method Not Allowed' HTTP response if the method isn't supported
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// agreementsHandler exposes the policy-update surface under /policyEnforcer.
// The PUT path is routed by URL suffix:
//
//	PUT /policyEnforcer/{steward}      → per-steward agreement update.
//	                                     Content-Type chooses the format:
//	                                       application/json → "json"
//	                                       text/plain (or any other) → "eflint"
//	                                     On approval, re-evaluates running jobs
//	                                     for {steward}.
//
//	PUT /policyEnforcer/sharedRules    → Layer-2 shared rules update (always
//	                                     eFLINT text). On approval, re-evaluates
//	                                     every running job for every steward.
func agreementsHandler(etcdClient *clientv3.Client, etcdRoot string) http.HandlerFunc {
	// The agreements JSON objects live under a sub-prefix; other sub-trees
	// (eflintModels, eflintRules, configs, eflint-states) hold non-Agreement
	// content that cannot be unmarshalled as api.Agreement.
	agreementsPrefix := etcdRoot + "/agreements/"

	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			// GenericGetHandler uses its etcdRoot both as a URL-path prefix
			// (for TrimPrefix) and as the etcd key prefix, so we cannot pass
			// agreementsPrefix directly without corrupting the URL stripping.
			// Instead, rewrite the request path to match agreementsPrefix so
			// both the URL stripping and the etcd lookup resolve correctly.
			r2 := r.Clone(r.Context())
			r2.URL.Path = agreementsPrefix + strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, etcdRoot), "/")
			api.GenericGetHandler[api.Agreement](w, r2, etcdClient, agreementsPrefix)
		case http.MethodPut:
			handlePutPolicyResource(w, r, etcdRoot)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// handlePutPolicyResource is the dispatcher for the unified /policyEnforcer
// PUT endpoint. The path suffix after etcdRoot selects between the
// per-steward and shared-rules flows.
func handlePutPolicyResource(w http.ResponseWriter, r *http.Request, etcdRoot string) {
	suffix := strings.Trim(strings.TrimPrefix(r.URL.Path, etcdRoot), "/")
	if suffix == "" {
		http.Error(w, "PUT requires a steward name or 'sharedRules' in the path", http.StatusBadRequest)
		return
	}
	if strings.Contains(suffix, "/") {
		http.Error(w, "nested resources are not supported", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		logger.Sugar().Errorf("Error reading body: %v", err)
		http.Error(w, "Error reading request body", http.StatusBadRequest)
		return
	}
	if len(body) == 0 {
		http.Error(w, "request body is empty", http.StatusBadRequest)
		return
	}

	if suffix == sharedRulesResource {
		handlePutSharedRules(w, r, body)
		return
	}

	handlePutStewardAgreement(w, r, suffix, body)
}

// handlePutStewardAgreement validates, persists, and triggers re-evaluation
// of running jobs for a single steward's agreement. Format is derived from
// the request's Content-Type so the same path supports both representations.
func handlePutStewardAgreement(w http.ResponseWriter, r *http.Request, steward string, body []byte) {
	format := formatFromContentType(r.Header.Get("Content-Type"))

	correlationId := uuid.New().String()
	policyUpdate := &pb.PolicyUpdate{
		Type: "agreementUpdate",
		RequestMetadata: &pb.RequestMetadata{
			DestinationQueue: "policyEnforcer-in",
			CorrelationId:    correlationId,
		},
		AgreementName:    steward,
		AgreementPayload: body,
		Format:           format,
	}

	res, ok := awaitPolicyEnforcerAck(w, r, policyUpdate, correlationId)
	if !ok {
		return
	}

	if res.ValidationResponse != nil && !res.ValidationResponse.RequestApproved {
		http.Error(w, "Policy update rejected by Policy Enforcer", http.StatusBadRequest)
		return
	}

	go checkJobs(steward)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// handlePutSharedRules validates and persists the consortium-wide Layer-2
// rules and, on approval, re-evaluates every running job for every steward
// (since shared rules affect derivations across all agreements).
func handlePutSharedRules(w http.ResponseWriter, r *http.Request, body []byte) {
	correlationId := uuid.New().String()
	policyUpdate := &pb.PolicyUpdate{
		Type: "sharedRulesUpdate",
		RequestMetadata: &pb.RequestMetadata{
			DestinationQueue: "policyEnforcer-in",
			CorrelationId:    correlationId,
		},
		AgreementPayload: body,
	}

	res, ok := awaitPolicyEnforcerAck(w, r, policyUpdate, correlationId)
	if !ok {
		return
	}

	if res.ValidationResponse != nil && !res.ValidationResponse.RequestApproved {
		http.Error(w, "Shared rules update rejected by Policy Enforcer", http.StatusBadRequest)
		return
	}

	go checkAllJobs()
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// awaitPolicyEnforcerAck submits the policy update to the policy-enforcer and
// blocks until the matching ack (keyed by correlation id) arrives or the
// timeout fires. On timeout / failure, it has already written an HTTP error
// to w and returns ok=false.
func awaitPolicyEnforcerAck(
	w http.ResponseWriter,
	r *http.Request,
	policyUpdate *pb.PolicyUpdate,
	correlationId string,
) (*pb.PolicyUpdate, bool) {
	resChan := make(chan *pb.PolicyUpdate, 1)
	agreementUpdateMutex.Lock()
	agreementUpdateMap[correlationId] = resChan
	agreementUpdateMutex.Unlock()

	defer func() {
		agreementUpdateMutex.Lock()
		delete(agreementUpdateMap, correlationId)
		agreementUpdateMutex.Unlock()
	}()

	logger.Sugar().Debugf("awaitPolicyEnforcerAck: sending type=%q correlationId=%s", policyUpdate.Type, correlationId)
	if _, err := c.SendPolicyUpdate(r.Context(), policyUpdate); err != nil {
		logger.Sugar().Errorf("error sending policy update: %v", err)
		http.Error(w, "Failed to dispatch policy update", http.StatusInternalServerError)
		return nil, false
	}

	select {
	case res := <-resChan:
		approved := res.ValidationResponse != nil && res.ValidationResponse.RequestApproved
		logger.Sugar().Debugf("awaitPolicyEnforcerAck: ack received correlationId=%s approved=%v", correlationId, approved)
		return res, true
	case <-time.After(30 * time.Second):
		logger.Sugar().Warnf("awaitPolicyEnforcerAck: timeout after 30s waiting for ack correlationId=%s", correlationId)
		http.Error(w, "Timeout waiting for Policy Enforcer validation", http.StatusGatewayTimeout)
		return nil, false
	}
}

// formatFromContentType maps the request Content-Type to the policy update
// format label understood by the policy-enforcer. application/json selects
// the legacy JSON validator; any other media type (text/plain,
// application/eflint, etc.) selects the eFLINT validator.
func formatFromContentType(contentType string) string {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType == "" {
		return api.ValidationStrategyEflint
	}
	if strings.EqualFold(mediaType, "application/json") {
		return "json"
	}
	return api.ValidationStrategyEflint
}

func updateEtc() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Your original updateEtc code here.
		go registerPolicyEnforcerConfiguration()

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Updated all config"))
	}
}
