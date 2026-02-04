package main

import (
	"context"
	"embed"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/eflint"
	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/httpapi"
	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/policyenforcer"
	policyenforcerhttp "github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/policyenforcerhttp"
	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/reasoner"
	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/repository"
	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/service"
	"github.com/Jorrit05/DYNAMOS/pkg/api"
	"github.com/Jorrit05/DYNAMOS/pkg/etcd"
	"github.com/Jorrit05/DYNAMOS/pkg/lib"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
	"github.com/gorilla/handlers"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.opencensus.io/plugin/ochttp"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

//go:embed eflint/dynamos-agreement.eflint
var embeddedModelFS embed.FS

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
	if app.etcdClient != nil {
		app.etcdClient.Close()
	}
	if app.grpcConn != nil {
		app.grpcConn.Close()
	}
}

// Run starts the application background tasks.
func (app *Application) Run() {
	app.logger.Debug("Starting message consumer")

	go func() {
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

	modelPath := resolveModelPath(app.logger, eflintModelPath)
	if modelPath == "" {
		app.logger.Warn("eflint model path is empty; auto-start will be skipped unless configured")
	}

	managerConfig := &eflint.ManagerConfig{
		EflintServerPath:  eflintServerPath,
		MinPort:           eflintMinPort,
		MaxPort:           eflintMaxPort,
		StartupDelay:      eflintStartupDelay,
		ConnectionTimeout: eflintTimeout,
	}
	manager := eflint.NewManager(managerConfig, app.logger)
	stateManager := eflint.NewStateManager(manager, eflintStateDir, app.logger)

	// Create the eFLINT reasoner (implements the Reasoner interface)
	eflintReasoner := reasoner.NewEflintReasoner(manager, app.logger)

	// Create the policy enforcer (uses the Reasoner interface)
	enforcer := policyenforcer.NewEnforcer(eflintReasoner, app.logger)

	instanceAPIHandler := eflint.NewInstanceAPIHandler(manager, app.logger)
	stateAPIHandler := eflint.NewStateAPIHandler(stateManager, app.logger)
	policyEnforcerHandler := policyenforcerhttp.NewHTTPHandler(enforcer, app.logger)

	headersOk := handlers.AllowedHeaders([]string{"X-Requested-With", "Content-Type", "Authorization"})
	originsOk := handlers.AllowedOrigins([]string{"*"})
	methodsOk := handlers.AllowedMethods([]string{"GET", "HEAD", "POST", "PUT", "DELETE", "OPTIONS"})

	mux := http.NewServeMux()
	apiMux := http.NewServeMux()

	mux.Handle("/health", http.HandlerFunc(healthHandler))

	// eFLINT instance management endpoints
	apiMux.Handle("/eflint/status", &ochttp.Handler{Handler: http.HandlerFunc(instanceAPIHandler.GetStatus)})
	apiMux.Handle("/eflint/start", &ochttp.Handler{Handler: http.HandlerFunc(instanceAPIHandler.Start)})
	apiMux.Handle("/eflint/stop", &ochttp.Handler{Handler: http.HandlerFunc(instanceAPIHandler.Stop)})
	apiMux.Handle("/eflint/command", &ochttp.Handler{Handler: http.HandlerFunc(instanceAPIHandler.SendCommand)})

	// eFLINT state endpoints
	apiMux.Handle("/eflint/state", &ochttp.Handler{Handler: http.HandlerFunc(stateAPIHandler.GetState)})
	apiMux.Handle("/eflint/state/export", &ochttp.Handler{Handler: http.HandlerFunc(stateAPIHandler.ExportState)})
	apiMux.Handle("/eflint/state/import", &ochttp.Handler{Handler: http.HandlerFunc(stateAPIHandler.ImportState)})
	apiMux.Handle("/eflint/state/checkpoint", &ochttp.Handler{Handler: http.HandlerFunc(stateAPIHandler.CreateCheckpoint)})
	apiMux.Handle("/eflint/state/checkpoint/restore", &ochttp.Handler{Handler: http.HandlerFunc(stateAPIHandler.RestoreCheckpoint)})
	apiMux.Handle("/eflint/state/checkpoints", &ochttp.Handler{Handler: http.HandlerFunc(stateAPIHandler.ListCheckpoints)})
	apiMux.Handle("/eflint/state/checkpoint/", &ochttp.Handler{Handler: http.HandlerFunc(stateAPIHandler.DeleteCheckpoint)})

	// Policy enforcer endpoints
	apiMux.Handle("/policy-enforcer/info", &ochttp.Handler{Handler: http.HandlerFunc(policyEnforcerHandler.GetReasonerInfo)})
	apiMux.Handle("/policy-enforcer/allowed-request-types", &ochttp.Handler{Handler: http.HandlerFunc(policyEnforcerHandler.GetAllowedRequestTypes)})
	apiMux.Handle("/policy-enforcer/allowed-data-sets", &ochttp.Handler{Handler: http.HandlerFunc(policyEnforcerHandler.GetAllowedDataSets)})
	apiMux.Handle("/policy-enforcer/allowed-archetypes", &ochttp.Handler{Handler: http.HandlerFunc(policyEnforcerHandler.GetAllowedArchetypes)})
	apiMux.Handle("/policy-enforcer/allowed-compute-providers", &ochttp.Handler{Handler: http.HandlerFunc(policyEnforcerHandler.GetAllowedComputeProviders)})
	apiMux.Handle("/policy-enforcer/allowed-clauses", &ochttp.Handler{Handler: http.HandlerFunc(policyEnforcerHandler.GetAllAllowedClauses)})
	apiMux.Handle("/policy-enforcer/validate", &ochttp.Handler{Handler: http.HandlerFunc(policyEnforcerHandler.ValidateRequest)})
	apiMux.Handle("/policy-enforcer/available-archetypes", &ochttp.Handler{Handler: http.HandlerFunc(policyEnforcerHandler.GetAvailableArchetypes)})
	apiMux.Handle("/policy-enforcer/available-compute-providers", &ochttp.Handler{Handler: http.HandlerFunc(policyEnforcerHandler.GetAvailableComputeProviders)})

	mux.Handle(apiVersion+"/", http.StripPrefix(apiVersion, apiMux))

	server := &http.Server{
		Addr:    port,
		Handler: api.LogMiddleware(handlers.CORS(originsOk, headersOk, methodsOk)(mux)),
	}

	go func() {
		app.logger.Sugar().Infow("Starting HTTP server", "port", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			app.logger.Sugar().Fatalw("Error starting HTTP server: %s", err)
		}
	}()

	if autoStartEflint && modelPath != "" {
		app.logger.Info("auto-starting eFLINT server", zap.String("model", modelPath))
		if err := manager.Start(modelPath); err != nil {
			app.logger.Error("failed to auto-start eFLINT server", zap.Error(err))
		}
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	app.logger.Info("shutting down...")

	if manager.IsRunning() {
		if err := manager.Stop(); err != nil {
			app.logger.Error("failed to stop eFLINT server", zap.Error(err))
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		app.logger.Error("failed to shutdown HTTP server", zap.Error(err))
	}

	app.logger.Info("shutdown complete")
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpapi.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "healthy"})
}

func resolveModelPath(logger *zap.Logger, configuredPath string) string {
	if configuredPath != "" {
		if _, err := os.Stat(configuredPath); err == nil {
			return configuredPath
		} else {
			logger.Warn("configured model path not found, falling back to embedded model",
				zap.String("path", configuredPath),
				zap.Error(err),
			)
		}
	}

	data, err := embeddedModelFS.ReadFile("eflint/dynamos-agreement.eflint")
	if err != nil {
		logger.Warn("failed to read embedded model", zap.Error(err))
		return configuredPath
	}

	tmpFile, err := os.CreateTemp("", "dynamos-agreement-*.eflint")
	if err != nil {
		logger.Warn("failed to create temp file for embedded model", zap.Error(err))
		return configuredPath
	}
	defer tmpFile.Close()

	if _, err := tmpFile.Write(data); err != nil {
		logger.Warn("failed to write embedded model to temp file", zap.Error(err))
		return configuredPath
	}

	return tmpFile.Name()
}
