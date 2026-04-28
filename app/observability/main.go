package observability

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

type Config struct {
	Enabled          bool
	ServiceName      string
	ServiceVersion   string
	OTLPEndpoint     string
	OTLPInsecure     bool
	MetricsEnabled   bool
	TracesEnabled    bool
	TraceSampleRatio float64
}

var (
	tracerName = "github.com/hotosm/scaleodm"

	httpServerDuration metric.Float64Histogram
	httpServerRequests metric.Int64Counter

	taskNewTotal    metric.Int64Counter
	taskNewDuration metric.Float64Histogram

	workflowCreateTotal    metric.Int64Counter
	workflowCreateDuration metric.Float64Histogram

	workflowReconciliationTotal metric.Int64Counter

	jobStatusUpdateTotal metric.Int64Counter

	readinessChecksTotal        metric.Int64Counter
	readinessDependencyFailures metric.Int64Counter
	readinessDuration           metric.Float64Histogram
	initialized                 bool
	initializedMu               sync.RWMutex
)

func IsEnabled() bool {
	initializedMu.RLock()
	defer initializedMu.RUnlock()
	return initialized
}

func Tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}

func Init(ctx context.Context, cfg Config) (func(context.Context) error, error) {
	if !cfg.Enabled {
		return func(context.Context) error { return nil }, nil
	}
	if strings.TrimSpace(cfg.OTLPEndpoint) == "" {
		return func(context.Context) error { return nil }, fmt.Errorf("SCALEODM_OBSERVABILITY_OTLP_ENDPOINT is required when observability is enabled")
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	res, err := resource.New(timeoutCtx,
		resource.WithAttributes(
			attribute.String("service.name", cfg.ServiceName),
			attribute.String("service.version", cfg.ServiceVersion),
		),
	)
	if err != nil {
		return func(context.Context) error { return nil }, fmt.Errorf("failed to initialize telemetry resource: %w", err)
	}

	shutdownFns := make([]func(context.Context) error, 0, 2)

	if cfg.TracesEnabled {
		traceOpts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(cfg.OTLPEndpoint)}
		if cfg.OTLPInsecure {
			traceOpts = append(traceOpts, otlptracegrpc.WithInsecure())
		}
		traceExporter, traceErr := otlptracegrpc.New(timeoutCtx, traceOpts...)
		if traceErr != nil {
			return func(context.Context) error { return nil }, fmt.Errorf("failed to initialize trace exporter: %w", traceErr)
		}

		sampler := cfg.TraceSampleRatio
		if sampler < 0 {
			sampler = 0
		}
		if sampler > 1 {
			sampler = 1
		}

		traceProvider := sdktrace.NewTracerProvider(
			sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(sampler))),
			sdktrace.WithBatcher(traceExporter),
			sdktrace.WithResource(res),
		)
		otel.SetTracerProvider(traceProvider)
		otel.SetTextMapPropagator(propagation.TraceContext{})
		shutdownFns = append(shutdownFns, traceProvider.Shutdown)
	}

	if cfg.MetricsEnabled {
		metricOpts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(cfg.OTLPEndpoint)}
		if cfg.OTLPInsecure {
			metricOpts = append(metricOpts, otlpmetricgrpc.WithInsecure())
		}
		metricExporter, metricErr := otlpmetricgrpc.New(timeoutCtx, metricOpts...)
		if metricErr != nil {
			return func(context.Context) error { return nil }, fmt.Errorf("failed to initialize metric exporter: %w", metricErr)
		}
		meterProvider := sdkmetric.NewMeterProvider(
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
			sdkmetric.WithResource(res),
		)
		otel.SetMeterProvider(meterProvider)
		shutdownFns = append(shutdownFns, meterProvider.Shutdown)

		meter := otel.Meter(tracerName)
		initInstruments(meter)
	}

	initializedMu.Lock()
	initialized = true
	initializedMu.Unlock()

	shutdown := func(ctx context.Context) error {
		var shutdownErr error
		for _, shutdownFn := range shutdownFns {
			if err := shutdownFn(ctx); err != nil {
				shutdownErr = err
			}
		}
		return shutdownErr
	}

	return shutdown, nil
}

func WrapHTTPHandler(handler http.Handler) http.Handler {
	if !IsEnabled() {
		return handler
	}
	base := withHTTPMetrics(handler)
	return otelhttp.NewHandler(base, "http.server",
		otelhttp.WithFilter(func(r *http.Request) bool {
			return r.URL.Path != "/__lbheartbeat__" && r.URL.Path != "/health"
		}),
	)
}

func RecordTaskNew(result, reason string, duration time.Duration) {
	if taskNewTotal != nil {
		taskNewTotal.Add(context.Background(), 1,
			metric.WithAttributes(
				attribute.String("result", normalize(result, "unknown")),
				attribute.String("reason", normalize(reason, "none")),
			),
		)
	}
	if taskNewDuration != nil {
		taskNewDuration.Record(context.Background(), duration.Seconds())
	}
}

func RecordWorkflowCreate(result, reason string, duration time.Duration) {
	if workflowCreateTotal != nil {
		workflowCreateTotal.Add(context.Background(), 1,
			metric.WithAttributes(
				attribute.String("result", normalize(result, "unknown")),
				attribute.String("reason", normalize(reason, "none")),
			),
		)
	}
	if workflowCreateDuration != nil {
		workflowCreateDuration.Record(context.Background(), duration.Seconds())
	}
}

func RecordWorkflowReconciliation(transition, trigger string) {
	if workflowReconciliationTotal == nil {
		return
	}
	workflowReconciliationTotal.Add(context.Background(), 1,
		metric.WithAttributes(
			attribute.String("transition", normalize(transition, "unknown")),
			attribute.String("trigger", normalize(trigger, "unknown")),
		),
	)
}

func RecordJobStatusUpdate(result, status, reason string, duration time.Duration) {
	if jobStatusUpdateTotal == nil {
		return
	}
	jobStatusUpdateTotal.Add(context.Background(), 1,
		metric.WithAttributes(
			attribute.String("result", normalize(result, "unknown")),
			attribute.String("status", normalize(status, "unknown")),
			attribute.String("reason", normalize(reason, "none")),
		),
	)
	_ = duration
}

func RecordReadinessCheck(ready bool, duration time.Duration) {
	if readinessChecksTotal != nil {
		result := "success"
		if !ready {
			result = "failure"
		}
		readinessChecksTotal.Add(context.Background(), 1,
			metric.WithAttributes(attribute.String("result", result)),
		)
	}
	if readinessDuration != nil {
		readinessDuration.Record(context.Background(), duration.Seconds())
	}
}

func RecordReadinessDependencyFailure(dependency, reason string) {
	if readinessDependencyFailures == nil {
		return
	}
	readinessDependencyFailures.Add(context.Background(), 1,
		metric.WithAttributes(
			attribute.String("dependency", normalize(dependency, "unknown")),
			attribute.String("reason", normalize(reason, "unknown")),
		),
	)
}

func initInstruments(meter metric.Meter) {
	var err error

	httpServerDuration, err = meter.Float64Histogram("scaleodm_http_server_duration_seconds")
	if err != nil {
		log.Printf("observability: failed creating http duration histogram: %v", err)
	}
	httpServerRequests, err = meter.Int64Counter("scaleodm_http_server_requests_total")
	if err != nil {
		log.Printf("observability: failed creating http request counter: %v", err)
	}
	taskNewTotal, err = meter.Int64Counter("scaleodm_task_new_total")
	if err != nil {
		log.Printf("observability: failed creating task new counter: %v", err)
	}
	taskNewDuration, err = meter.Float64Histogram("scaleodm_task_new_duration_seconds")
	if err != nil {
		log.Printf("observability: failed creating task new duration histogram: %v", err)
	}
	workflowCreateTotal, err = meter.Int64Counter("scaleodm_workflow_create_total")
	if err != nil {
		log.Printf("observability: failed creating workflow create counter: %v", err)
	}
	workflowCreateDuration, err = meter.Float64Histogram("scaleodm_workflow_create_duration_seconds")
	if err != nil {
		log.Printf("observability: failed creating workflow create duration histogram: %v", err)
	}
	workflowReconciliationTotal, err = meter.Int64Counter("scaleodm_workflow_reconciliation_total")
	if err != nil {
		log.Printf("observability: failed creating workflow reconciliation counter: %v", err)
	}
	jobStatusUpdateTotal, err = meter.Int64Counter("scaleodm_job_status_update_total")
	if err != nil {
		log.Printf("observability: failed creating job status update counter: %v", err)
	}
	readinessChecksTotal, err = meter.Int64Counter("scaleodm_readiness_checks_total")
	if err != nil {
		log.Printf("observability: failed creating readiness checks counter: %v", err)
	}
	readinessDependencyFailures, err = meter.Int64Counter("scaleodm_readiness_dependency_failures_total")
	if err != nil {
		log.Printf("observability: failed creating readiness dependency failure counter: %v", err)
	}
	readinessDuration, err = meter.Float64Histogram("scaleodm_readiness_duration_seconds")
	if err != nil {
		log.Printf("observability: failed creating readiness duration histogram: %v", err)
	}
}

func withHTTPMetrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__lbheartbeat__" || r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rec, r)
		route := normalizeRoute(r.URL.Path)
		statusClass := fmt.Sprintf("%dxx", rec.statusCode/100)
		attrs := metric.WithAttributes(
			attribute.String("method", normalize(strings.ToUpper(r.Method), "UNKNOWN")),
			attribute.String("route", route),
			attribute.String("status_class", statusClass),
		)
		if httpServerRequests != nil {
			httpServerRequests.Add(r.Context(), 1, attrs)
		}
		if httpServerDuration != nil {
			httpServerDuration.Record(r.Context(), time.Since(start).Seconds(), attrs)
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *statusRecorder) WriteHeader(statusCode int) {
	r.statusCode = statusCode
	r.ResponseWriter.WriteHeader(statusCode)
}

func normalizeRoute(path string) string {
	switch {
	case path == "/task/new":
		return "/task/new"
	case strings.HasPrefix(path, "/task/") && strings.HasSuffix(path, "/info"):
		return "/task/{uuid}/info"
	case strings.HasPrefix(path, "/task/") && strings.HasSuffix(path, "/output"):
		return "/task/{uuid}/output"
	case strings.HasPrefix(path, "/task/") && strings.Contains(path, "/download/"):
		return "/task/{uuid}/download/{asset}"
	case path == "":
		return "unknown"
	default:
		return path
	}
}

func normalize(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
