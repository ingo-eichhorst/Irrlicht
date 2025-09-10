package container

import (
	"time"

	"irrlicht/hook/adapters/outbound/configuration"
	"irrlicht/hook/adapters/outbound/filesystem"
	"irrlicht/hook/adapters/outbound/git"
	"irrlicht/hook/adapters/outbound/logger"
	"irrlicht/hook/adapters/outbound/transcript"
	"irrlicht/hook/application/services"
	"irrlicht/hook/application/usecase"
	"irrlicht/hook/domain/event"
	"irrlicht/hook/domain/metrics"
	"irrlicht/hook/ports/outbound"
)

// Container holds all application dependencies
type Container struct {
	// Infrastructure
	logger            outbound.Logger
	sessionRepo       outbound.SessionRepository
	transcriptAnalyzer outbound.TranscriptAnalyzer
	gitService        outbound.GitService
	metricsCollector  outbound.MetricsCollector
	fileSystemService outbound.FileSystemService
	configService     outbound.ConfigurationService

	// Application Services
	eventProcessor *services.EventProcessor

	// Use Cases
	processHookEventUseCase *usecase.ProcessHookEventUseCase

	// Domain Services
	eventValidator *event.Validator
}

// NewContainer creates and initializes a new dependency injection container
func NewContainer() (*Container, error) {
	c := &Container{}

	// Initialize infrastructure adapters
	if err := c.initializeInfrastructure(); err != nil {
		return nil, err
	}

	// Initialize application services
	c.initializeApplicationServices()

	// Initialize use cases
	c.initializeUseCases()

	return c, nil
}

// initializeInfrastructure sets up all infrastructure adapters
func (c *Container) initializeInfrastructure() error {
	// Configuration service (no dependencies)
	c.configService = configuration.NewServiceAdapter()

	// File system service (no dependencies)
	c.fileSystemService = filesystem.NewServiceAdapter()

	// Logger
	structuredLogger, err := logger.NewStructuredLoggerAdapter()
	if err != nil {
		return err
	}
	c.logger = structuredLogger

	// Session repository (depends on config service)
	c.sessionRepo = filesystem.NewSessionRepository(c.configService, c.logger)

	// Transcript analyzer
	c.transcriptAnalyzer = transcript.NewAnalyzer(outbound.DefaultTranscriptConfig())

	// Git service
	c.gitService = git.NewService()

	// Metrics collector - for now, use a no-op implementation
	c.metricsCollector = &noOpMetricsCollector{}

	return nil
}

// initializeApplicationServices sets up application layer services
func (c *Container) initializeApplicationServices() {
	c.eventProcessor = services.NewEventProcessor(
		c.sessionRepo,
		c.transcriptAnalyzer,
		c.logger,
		c.gitService,
		c.metricsCollector,
		c.fileSystemService,
		c.configService,
	)
}

// initializeUseCases sets up use cases
func (c *Container) initializeUseCases() {
	c.eventValidator = event.NewValidator(c.fileSystemService)
	c.processHookEventUseCase = usecase.NewProcessHookEventUseCase(
		c.eventProcessor,
		c.logger,
		c.eventValidator,
	)
}

// GetProcessHookEventUseCase returns the main use case for processing hook events
func (c *Container) GetProcessHookEventUseCase() *usecase.ProcessHookEventUseCase {
	return c.processHookEventUseCase
}

// GetLogger returns the logger instance
func (c *Container) GetLogger() outbound.Logger {
	return c.logger
}

// GetConfigService returns the configuration service
func (c *Container) GetConfigService() outbound.ConfigurationService {
	return c.configService
}

// Close cleans up any resources held by the container
func (c *Container) Close() error {
	// Close logger if it needs cleanup
	if closer, ok := c.logger.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

// noOpMetricsCollector is a temporary no-op implementation of MetricsCollector
type noOpMetricsCollector struct{}

func (n *noOpMetricsCollector) RecordEventProcessing(eventType string, processingTime time.Duration) {
	// No-op implementation
}

func (n *noOpMetricsCollector) RecordError(eventType string, errorType string) {
	// No-op implementation
}

func (n *noOpMetricsCollector) RecordSessionOperation(operation string, duration time.Duration, success bool) {
	// No-op implementation
}

func (n *noOpMetricsCollector) GetCurrentStats() metrics.Stats {
	return metrics.Stats{}
}

func (n *noOpMetricsCollector) GetSystemMetrics() *metrics.SystemMetrics {
	return &metrics.SystemMetrics{}
}