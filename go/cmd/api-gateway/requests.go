// This file contains the handlers for the requests that the API Gateway receives from the client
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Jorrit05/DYNAMOS/pkg/api"
	"github.com/Jorrit05/DYNAMOS/pkg/lib"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.opencensus.io/trace"
	"go.opencensus.io/trace/propagation"
)

// jobStatus is the polled state of an in-flight/completed asynchronous request.
type jobStatus struct {
	Status string `json:"status"` // "pending", "done", or "error"
	Result []byte `json:"-"`
	Error  string `json:"error,omitempty"`
}

var (
	// jobStore holds the result/status of the single job allowed to run at a time,
	// keyed by jobId so a client can keep polling the same id it received up front.
	jobStore = &sync.Map{}

	// Only one request may be in flight at a time; activeJobID is non-empty
	// while a job is pending and is cleared once it completes (success or error).
	activeJobMutex sync.Mutex
	activeJobID    string
)

func requestHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger.Debug("Starting requestApprovalHandler")

		// Reject the request outright if another one is already in flight.
		activeJobMutex.Lock()
		if activeJobID != "" {
			existing := activeJobID
			activeJobMutex.Unlock()
			logger.Sugar().Warnf("Rejecting new request, job %s is still active", existing)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "another request is already in progress",
				"jobId": existing,
			})
			return
		}
		jobId := uuid.NewString()
		activeJobID = jobId
		activeJobMutex.Unlock()

		body, err := api.GetRequestBody(w, r, serviceName)
		if err != nil {
			activeJobMutex.Lock()
			activeJobID = ""
			activeJobMutex.Unlock()
			return
		}

		var apiReqApproval api.RequestApproval
		if err := json.Unmarshal(body, &apiReqApproval); err != nil {
			logger.Sugar().Errorf("Error unmMarshalling get apiReqApproval: %v", err)
			activeJobMutex.Lock()
			activeJobID = ""
			activeJobMutex.Unlock()
			return
		}

		userPb := &pb.User{
			Id:       apiReqApproval.User.Id,
			UserName: apiReqApproval.User.UserName,
		}

		var dataRequestInterface map[string]interface{}
		if err := json.Unmarshal(apiReqApproval.DataRequest, &dataRequestInterface); err != nil {
			logger.Sugar().Errorf("Error unmarhsalling get request: %v", err)
			activeJobMutex.Lock()
			activeJobID = ""
			activeJobMutex.Unlock()
			return
		}

		dataRequestOptions := &api.DataRequestOptions{}
		dataRequestOptions.Options = make(map[string]bool)
		if err := json.Unmarshal(apiReqApproval.DataRequest, &dataRequestOptions); err != nil {
			logger.Sugar().Errorf("Error unmMarshalling get apiReqApproval: %v", err)
			activeJobMutex.Lock()
			activeJobID = ""
			activeJobMutex.Unlock()
			return
		}

		dataRequestInterface["user"] = userPb

		// Create protobuf struct for the req approval flow
		protoRequest := &pb.RequestApproval{
			Type:             apiReqApproval.Type,
			User:             userPb,
			DataProviders:    apiReqApproval.DataProviders,
			DestinationQueue: "policyEnforcer-in",
			Options:          dataRequestOptions.Options,
		}

		jobStore.Store(jobId, &jobStatus{Status: "pending"})

		// Run the rest of the flow asynchronously so the HTTP handler can
		// return immediately with the jobId; the caller polls for the result.
		go processRequestApproval(jobId, protoRequest, dataRequestInterface, apiReqApproval.Type)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"jobId": jobId})
	}
}

// processRequestApproval runs the request-approval + data-dispatch flow in the
// background and stores the outcome in jobStore under jobId. It always clears
// activeJobID when done (success or failure) so a new request can be accepted.
func processRequestApproval(jobId string, protoRequest *pb.RequestApproval, dataRequestInterface map[string]interface{}, requestType string) {
	defer func() {
		activeJobMutex.Lock()
		activeJobID = ""
		activeJobMutex.Unlock()
	}()

	finish := func(status string, result []byte, errMsg string) {
		jobStore.Store(jobId, &jobStatus{Status: status, Result: result, Error: errMsg})
	}

	ctxWithTimeout, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ctx, span := trace.StartSpan(ctxWithTimeout, "requestApprovalHandler")
	defer span.End()

	// Create a channel to receive the response
	responseChan := make(chan validation)

	requestApprovalMutex.Lock()
	requestApprovalMap[protoRequest.User.Id] = responseChan
	requestApprovalMutex.Unlock()

	_, err := c.SendRequestApproval(ctx, protoRequest)
	if err != nil {
		logger.Sugar().Errorf("error in sending requestapproval: %v", err)
	}

	select {
	case validationStruct := <-responseChan:
		msg := validationStruct.response

		logger.Sugar().Infof("Received response, %s", msg.Type)
		if msg.Type != "requestApprovalResponse" {
			logger.Sugar().Errorf("Unexpected message received, type: %s", msg.Type)
			finish("error", nil, "internal server error")
			return
		}

		if msg.Error != "" {
			logger.Sugar().Warnf("Request approval denied: %s", msg.Error)
			finish("error", nil, msg.Error)
			return
		}

		// Add necessary information for the data request in the request metadata
		requestMetadata := &pb.RequestMetadata{
			// Add the job id from the request approval to the data request body
			JobId: msg.JobId,
			// initialize the map to add values to it
			Traces: make(map[string][]byte),
		}
		// Add the binary trace of the span to the data request (used for appending the traces)
		requestMetadata.Traces["binaryTrace"] = propagation.Binary(span.SpanContext())
		// Set the data request interface to the request metadata from the previous steps
		dataRequestInterface["requestMetadata"] = requestMetadata

		// Marshal the combined data back into JSON for forwarding
		dataRequestJson, err := json.Marshal(dataRequestInterface)
		if err != nil {
			logger.Sugar().Errorf("Error marshalling combined data: %v", err)
			finish("error", nil, "failed to marshal data request")
			return
		}

		logger.Sugar().Infof("Data Prepared jsonData: %s", dataRequestJson)

		// Send the data to the authorized providers
		responses := sendDataToAuthProviders(dataRequestJson, msg.AuthorizedProviders, requestType, msg.JobId)
		finish("done", responses, "")

	case <-ctx.Done():
		finish("error", nil, "request timed out")
	}
}

// requestStatusHandler lets the client poll for the outcome of the single
// in-flight (or most recently completed) request identified by jobId.
func requestStatusHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jobId := r.URL.Query().Get("jobId")
		if jobId == "" {
			http.Error(w, "jobId query parameter is required", http.StatusBadRequest)
			return
		}

		v, ok := jobStore.Load(jobId)
		if !ok {
			http.Error(w, "unknown jobId", http.StatusNotFound)
			return
		}
		job := v.(*jobStatus)

		w.Header().Set("Content-Type", "application/json")
		if job.Status == "done" {
			w.WriteHeader(http.StatusOK)
			w.Write(job.Result)
			return
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(job)
	}
}

// Use the data request that was previously built and send it to the authorised providers
// acquired from the request approval
func sendDataToAuthProviders(dataRequest []byte, authorizedProviders map[string]string, msgType string, jobId string) []byte {
	// Setup the wait group for async data requests
	var wg sync.WaitGroup
	var responsesMutex sync.Mutex
	responses := make(map[string]string, len(authorizedProviders))

	// This will be replaced with AMQ in the future
	agentPort := "8080"
	// Iterate over each auth provider
	for auth, url := range authorizedProviders {
		wg.Add(1)
		target := strings.ToLower(auth)
		// Construct the end point
		endpoint := fmt.Sprintf("http://%s:%s/agent/v1/%s/%s", url, agentPort, msgType, target)

		// Print the request to the console (without \n to avoid the log only showing first line when searching)
		logger.Sugar().Infof("Sending request to %s. Endpoint: %s JSON:%v", target, endpoint, string(dataRequest))

		// Async call send the data
		go func(providerName, endpoint string) {
			defer wg.Done()
			respData, err := sendData(endpoint, dataRequest)

			responsesMutex.Lock()
			defer responsesMutex.Unlock()
			if err != nil {
				logger.Sugar().Errorf("Error sending data to %s: %v", providerName, err)
				responses[providerName] = fmt.Sprintf("error: %v", err)
				return
			}
			responses[providerName] = respData
		}(auth, endpoint)
	}

	// Wait until all the requests are complete
	wg.Wait()
	logger.Sugar().Debug("Returning responses")

	responseMap := map[string]interface{}{
		"jobId":     jobId,
		"responses": responses,
	}

	// jsonResponse, _ := json.Marshal(responseMap)
	// return jsonResponse
	return cleanupAndMarshalResponse(responseMap)
}

// Now assumes input is map[string]interface{} and directly marshals it to prettified JSON.
func cleanupAndMarshalResponse(responseMap map[string]interface{}) []byte {
	prettifiedJSON, err := json.MarshalIndent(responseMap, "", "    ")
	if err != nil {
		logger.Sugar().Errorf("Error marshalling cleaned response: %v", err)
	}
	return prettifiedJSON
}

func sendData(endpoint string, jsonData []byte) (string, error) {
	// FIXME: Change to an actual token in the future?
	headers := map[string]string{
		"Authorization": "bearer 1234",
	}
	// Request the data using the endpoint, body and headers
	body, err := api.PostRequest(endpoint, string(jsonData), headers)
	if err != nil {
		return "", err
	}

	// Print body (only use for debugging and testing, this is sometimes a very large output in the logs)
	// logger.Sugar().Debugf("Body: %v", body)

	// Here we should send the request over the socket
	// For now we should append it to a list so that we gather all responses and send them in bulk
	return string(body), nil
}

func availableProvidersHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger.Debug("Starting requestApprovalHandler")
		var availableProviders = make(map[string]lib.AgentDetails)
		resp, err := getAvailableProviders()
		if err != nil {
			logger.Sugar().Errorf("Error getting available providers: %v", err)
			return
		}

		// Bind resp to availableProviders
		availableProviders = resp

		jsonResponse, err := json.Marshal(availableProviders)
		if err != nil {
			logger.Sugar().Errorf("Error marshalling result, %v", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write(jsonResponse)
	}
}

// Maybe this should be moved into the orchestrarot
func getAvailableProviders() (map[string]lib.AgentDetails, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Get the value from etcd.
	resp, err := etcdClient.Get(ctx, "/agents/online", clientv3.WithPrefix())
	if err != nil {
		logger.Sugar().Errorf("failed to get value from etcd: %v", err)
		return nil, err
	}

	// Initialize an empty map to store the unmarshaled structs.
	result := make(map[string]lib.AgentDetails)
	// Iterate through the key-value pairs and unmarshal the values into structs.
	for _, kv := range resp.Kvs {
		var target lib.AgentDetails
		err = json.Unmarshal(kv.Value, &target)
		if err != nil {
			// return nil, fmt.Errorf("failed to unmarshal JSON for key %s: %v", key, err)
		}
		result[string(target.Name)] = target
	}

	return result, nil

}
