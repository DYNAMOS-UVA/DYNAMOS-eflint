package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/repository"
	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/service"
	"github.com/Jorrit05/DYNAMOS/pkg/etcd"
	"github.com/Jorrit05/DYNAMOS/pkg/lib"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// Application holds all the dependencies for the policy enforcer service.
type Application struct {
	logger            *zap.Logger
	etcdClient        *clientv3.Client
	grpcConn          *grpc.ClientConn
	rabbitMQClient    pb.RabbitMQClient
	validationService *service.ValidationService
	responseSender    service.ResponseSender
	receiveMutex      *sync.Mutex
}

// NewApplication creates and initializes a new Application with all dependencies.
func NewApplication() (*Application, error) {
	app := &Application{
		logger:       lib.InitLogger(logLevel),
		receiveMutex: &sync.Mutex{},
	}

	// Initialize etcd client
	app.etcdClient = etcd.GetEtcdClient(etcdEndpoints)

	// Initialize gRPC connection and RabbitMQ client
	app.grpcConn = lib.GetGrpcConnection(grpcAddr)
	app.rabbitMQClient = lib.InitializeSidecarMessaging(
		app.grpcConn,
		&pb.InitRequest{
			ServiceName:     fmt.Sprintf("%s-in", serviceName),
			RoutingKey:      fmt.Sprintf("%s-in", serviceName),
			QueueAutoDelete: false,
		},
	)

	// Initialize the validation service with all its dependencies
	app.validationService = service.NewValidationService(
		repository.NewEtcdAgreementRepository(app.etcdClient),
		service.NewAgreementValidator(),
		service.NewStaticAuthTokenGenerator(),
		app.logger,
	)

	// Initialize the response sender
	app.responseSender = service.NewRabbitMQResponseSender(app.rabbitMQClient)

	return app, nil
}

// Close cleanly shuts down all resources.
func (app *Application) Close() {
	if app.grpcConn != nil {
		app.grpcConn.Close()
	}
}

// Run starts the application and blocks until completion.
func (app *Application) Run() {
	var wg sync.WaitGroup
	wg.Add(1)

	app.logger.Debug("Starting message consumer")

	go func() {
		defer wg.Done()
		lib.StartConsumingWithRetry(
			serviceName,
			app.rabbitMQClient,
			fmt.Sprintf("%s-in", serviceName),
			app.handleIncomingMessages,
			5,
			5*time.Second,
			app.receiveMutex,
		)
	}()

	wg.Wait()
}

func main() {
	_, err := lib.InitTracer(serviceName)
	if err != nil {
		panic(fmt.Sprintf("Failed to create ocagent-exporter: %v", err))
	}

	app, err := NewApplication()
	if err != nil {
		panic(fmt.Sprintf("Failed to initialize application: %v", err))
	}
	defer app.Close()

	app.Run()
}
