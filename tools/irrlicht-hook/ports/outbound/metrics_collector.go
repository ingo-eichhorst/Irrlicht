package outbound

import (
	"irrlicht/hook/domain/metrics"
	"time"
)

// MetricsCollector defines the outbound port for collecting and recording metrics
type MetricsCollector interface {
	// RecordEventProcessing records metrics for event processing
	RecordEventProcessing(eventType string, duration time.Duration)
	
	// RecordError records error metrics
	RecordError(eventType string, errorType string)
	
	// RecordSessionOperation records session-related operations
	RecordSessionOperation(operation string, duration time.Duration, success bool)
	
	// GetCurrentStats returns current system statistics
	GetCurrentStats() metrics.Stats
	
	// GetSystemMetrics returns comprehensive system metrics
	GetSystemMetrics() *metrics.SystemMetrics
}

// PerformanceMonitor defines the interface for monitoring system performance
type PerformanceMonitor interface {
	// StartMonitoring begins performance monitoring
	StartMonitoring() error
	
	// StopMonitoring stops performance monitoring
	StopMonitoring() error
	
	// GetPerformanceReport generates a performance report
	GetPerformanceReport() *PerformanceReport
	
	// IsHealthy checks if the system is performing within acceptable parameters
	IsHealthy() bool
}

// PerformanceReport holds comprehensive performance information
type PerformanceReport struct {
	SystemStats      metrics.Stats     `json:"system_stats"`
	EventStats       []EventTypeStats  `json:"event_stats"`
	ErrorStats       []ErrorTypeStats  `json:"error_stats"`
	ResourceUsage    ResourceUsage     `json:"resource_usage"`
	HealthStatus     HealthStatus      `json:"health_status"`
	GeneratedAt      time.Time         `json:"generated_at"`
	ReportingPeriod  time.Duration     `json:"reporting_period"`
}

// EventTypeStats holds statistics for a specific event type
type EventTypeStats struct {
	EventType        string  `json:"event_type"`
	Count            int64   `json:"count"`
	AverageLatencyMs float64 `json:"average_latency_ms"`
	MinLatencyMs     int64   `json:"min_latency_ms"`
	MaxLatencyMs     int64   `json:"max_latency_ms"`
	ErrorRate        float64 `json:"error_rate"`
}

// ErrorTypeStats holds statistics for specific error types
type ErrorTypeStats struct {
	ErrorType   string `json:"error_type"`
	Count       int64  `json:"count"`
	LastOccurred time.Time `json:"last_occurred"`
	Percentage  float64 `json:"percentage"`
}

// ResourceUsage holds resource utilization information
type ResourceUsage struct {
	MemoryUsageMB    float64 `json:"memory_usage_mb"`
	CPUUsagePercent  float64 `json:"cpu_usage_percent"`
	DiskUsageMB      float64 `json:"disk_usage_mb"`
	OpenFileHandles  int64   `json:"open_file_handles"`
	GoroutineCount   int64   `json:"goroutine_count"`
}

// HealthStatus represents the overall health of the system
type HealthStatus struct {
	Status           string            `json:"status"` // "healthy", "degraded", "unhealthy"
	Checks           []HealthCheck     `json:"checks"`
	OverallScore     float64           `json:"overall_score"`
	LastChecked      time.Time         `json:"last_checked"`
}

// HealthCheck represents an individual health check
type HealthCheck struct {
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Message     string    `json:"message,omitempty"`
	Duration    int64     `json:"duration_ms"`
	CheckedAt   time.Time `json:"checked_at"`
}

// AlertManager defines the interface for managing performance alerts
type AlertManager interface {
	// RegisterAlert registers a new alert condition
	RegisterAlert(alert *AlertCondition) error
	
	// CheckAlerts evaluates all alert conditions
	CheckAlerts(metrics *PerformanceReport) []Alert
	
	// SendAlert sends an alert notification
	SendAlert(alert Alert) error
	
	// GetActiveAlerts returns currently active alerts
	GetActiveAlerts() []Alert
}

// AlertCondition defines a condition that triggers an alert
type AlertCondition struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Metric      string        `json:"metric"`
	Operator    string        `json:"operator"` // "gt", "lt", "eq", "gte", "lte"
	Threshold   float64       `json:"threshold"`
	Duration    time.Duration `json:"duration"`
	Severity    AlertSeverity `json:"severity"`
}

// Alert represents a triggered alert
type Alert struct {
	ID          string        `json:"id"`
	Condition   *AlertCondition `json:"condition"`
	TriggeredAt time.Time     `json:"triggered_at"`
	Value       float64       `json:"value"`
	Message     string        `json:"message"`
	Status      AlertStatus   `json:"status"`
}

// AlertSeverity represents the severity level of an alert
type AlertSeverity string

const (
	AlertSeverityLow      AlertSeverity = "low"
	AlertSeverityMedium   AlertSeverity = "medium"
	AlertSeverityHigh     AlertSeverity = "high"
	AlertSeverityCritical AlertSeverity = "critical"
)

// AlertStatus represents the current status of an alert
type AlertStatus string

const (
	AlertStatusActive   AlertStatus = "active"
	AlertStatusResolved AlertStatus = "resolved"
	AlertStatusMuted    AlertStatus = "muted"
)

// MetricsExporter defines the interface for exporting metrics data
type MetricsExporter interface {
	// ExportMetrics exports metrics in the specified format
	ExportMetrics(format ExportFormat) ([]byte, error)
	
	// ExportToFile exports metrics to a file
	ExportToFile(filepath string, format ExportFormat) error
	
	// GetSupportedFormats returns supported export formats
	GetSupportedFormats() []ExportFormat
}

// ExportFormat represents different export formats for metrics
type ExportFormat string

const (
	ExportFormatJSON       ExportFormat = "json"
	ExportFormatCSV        ExportFormat = "csv"
	ExportFormatPrometheus ExportFormat = "prometheus"
	ExportFormatInfluxDB   ExportFormat = "influxdb"
)