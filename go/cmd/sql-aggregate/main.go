package main

import (
	"context"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/Jorrit05/DYNAMOS/pkg/lib"
	"github.com/Jorrit05/DYNAMOS/pkg/msinit"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
)

var (
	logger               = lib.InitLogger(logLevel)
	COORDINATOR          = make(chan struct{})
	NR_OF_DATA_PROVIDERS = getNrOfDataProviders()
	msCommList           = []*pb.MicroserviceCommunication{}
	msCommMutex          = &sync.Mutex{}
	finishOnce           sync.Once
)

// waitForProvidersTimeout bounds how long we wait for the remaining data providers
// before giving up, so this pod exits instead of hanging until ActiveDeadlineSeconds.
const waitForProvidersTimeout = 45 * time.Second

func getNrOfDataProviders() int {
	nr_of_data_providers_int := 0
	nr_of_data_providers := os.Getenv("NR_OF_DATA_PROVIDERS")
	var err error
	if nr_of_data_providers != "" {
		nr_of_data_providers_int, err = strconv.Atoi(nr_of_data_providers)
		if err != nil {
			logger.Sugar().Errorf("Error converting nr_of_data_providers to int: %v", err)
		}
	}
	return nr_of_data_providers_int
}

func main() {
	logger.Sugar().Debugf("Starting %s service", serviceName)

	oce, err := lib.InitTracer(serviceName)
	if err != nil {
		logger.Sugar().Fatalf("Failed to create ocagent-exporter: %v", err)
	}

	config, err := msinit.NewConfiguration(context.Background(), serviceName, grpcAddr, COORDINATOR, messageHandler)
	if err != nil {
		logger.Sugar().Fatalf("%v", err)
	}

	go func() {
		<-COORDINATOR
		time.Sleep(waitForProvidersTimeout)
		finishOnce.Do(func() {
			msCommMutex.Lock()
			received := len(msCommList)
			msCommMutex.Unlock()
			logger.Sugar().Warnf("Timed out after %s waiting for data providers; got %d/%d", waitForProvidersTimeout, received, NR_OF_DATA_PROVIDERS)
			finishAggregation(context.Background(), config)
		})
	}()

	// Wait here until the message arrives in the messageHandler
	<-config.StopMicroservice

	config.SafeExit(oce, serviceName)
	os.Exit(0)
}

// finishAggregation merges whatever data has arrived so far, forwards it, and unblocks main().
// Called at most once, either once all providers responded or on waitForProvidersTimeout.
func finishAggregation(ctx context.Context, config *msinit.Configuration) {
	msCommMutex.Lock()
	list := append([]*pb.MicroserviceCommunication{}, msCommList...)
	msCommMutex.Unlock()

	if len(list) == 0 {
		logger.Sugar().Errorf("No data provider responses received; nothing to forward")
		close(config.StopMicroservice)
		return
	}

	msComm := list[0]
	switch msComm.RequestType {
	case "sqlDataRequest":
		var err error
		ctx, msComm, err = handleSqlDataRequest(ctx, list)
		if err != nil {
			logger.Sugar().Errorf("Failed to process %s message: %v", msComm.RequestType, err)
		}
	default:
		logger.Sugar().Errorf("Unknown RequestType type: %v", msComm.RequestType)
	}

	config.NextClient.SendData(ctx, msComm)
	close(config.StopMicroservice)
}

func messageHandler(config *msinit.Configuration) func(ctx context.Context, msComm *pb.MicroserviceCommunication) error {
	return func(ctx context.Context, msComm *pb.MicroserviceCommunication) error {
		ctx, span, err := lib.StartRemoteParentSpan(ctx, serviceName+"/func: messageHandler", msComm.Traces)
		if err != nil {
			logger.Sugar().Warnf("Error starting span: %v", err)
		}
		defer span.End()

		// Wait till all services and connections have started
		<-COORDINATOR

		msCommMutex.Lock()
		msCommList = append(msCommList, msComm)
		received := len(msCommList)
		msCommMutex.Unlock()

		logger.Sugar().Infof("amount of data providers %v, received %v", NR_OF_DATA_PROVIDERS, received)

		// If NR_OF_DATA_PROVIDERS == 0 aggregate won't actually function and pass on the message.
		// This can happen at this moment if the aggregate flag is set to True, but it is not allowed by policy.
		if received != NR_OF_DATA_PROVIDERS && NR_OF_DATA_PROVIDERS != 0 {
			logger.Sugar().Infof("Waiting for %v more message(s)", NR_OF_DATA_PROVIDERS-received)
			return nil
		}

		finishOnce.Do(func() {
			finishAggregation(ctx, config)
		})

		return nil
	}
}
