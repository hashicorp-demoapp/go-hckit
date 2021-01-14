package hckit

import (
	"io"
	"log"
	"net/http"
	"strings"

	opentracing "github.com/opentracing/opentracing-go"
	ext "github.com/opentracing/opentracing-go/ext"
	otlog "github.com/opentracing/opentracing-go/log"
	jaeger "github.com/uber/jaeger-client-go"
	config "github.com/uber/jaeger-client-go/config"
	jaegerlog "github.com/uber/jaeger-client-go/log"
	"github.com/uber/jaeger-client-go/zipkin"
	"github.com/uber/jaeger-lib/metrics"
)

// InitGlobalTracer sets the GlobalTracer to an instance of Jaeger Tracer that
// loads the Jaeger tracer from the environment, samples 100% of traces, and logs all spans to stdout.
func InitGlobalTracer(service string) (io.Closer, error) {
	//config from env
	cfg, err := config.FromEnv()

	//overrides
	cfg.Sampler = &config.SamplerConfig{
		Type:  jaeger.SamplerTypeConst,
		Param: 1,
	}
	cfg.Reporter.LogSpans = true

	jLogger := jaegerlog.StdLogger
	jMetricsFactory := metrics.NullFactory

	// Zipkin shares span ID between client and server spans; it must be enabled via the following option.
	zipkinPropagator := zipkin.NewZipkinB3HTTPHeaderPropagator()

	// Create tracer and then initialize global tracer
	closer, err := cfg.InitGlobalTracer(
		service,
		config.Logger(jLogger),
		config.Metrics(jMetricsFactory),
		config.Injector(opentracing.HTTPHeaders, zipkinPropagator),
		config.Extractor(opentracing.HTTPHeaders, zipkinPropagator),
		config.ZipkinSharedRPCSpan(true),
	)

	if err != nil {
		log.Printf("Could not initialize jaeger tracer: %s", err.Error())
		return closer, err
	}

	return closer, nil
}

// TracingMiddleware returns an HTTP Handler appropriate for Middleware chaining via Router.Use.
func TracingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Ignore health checks. TODO: this should be some sort of configured value
		// in case a different endpoint name is used.
		if strings.Contains(r.URL.Path, "health") {
			next.ServeHTTP(w, r)
			return
		}

		log.Printf("INFO: TracingMiddleware beginning for %s---------------------------", r.URL.Path)

		tracer := opentracing.GlobalTracer()
		// If no context exists an error will be returned, but we ignore it
		// because if ctx == nil, a root span will be created.
		wireContext, err := tracer.Extract(opentracing.HTTPHeaders, opentracing.HTTPHeadersCarrier(r.Header))
		if err != nil {
			log.Printf("WARN: Extract failed, error recieved.\n%v\n", err)
		}

		if wireContext != nil {
			log.Printf("INFO: WireContext is %v", wireContext)
		}
		span := tracer.StartSpan(r.URL.Path, ext.RPCServerOption(wireContext))
		defer span.Finish()

		span.LogFields(
			otlog.String("event", r.URL.Path),
			otlog.String("value", "start"),
		)

		next.ServeHTTP(w, r)

		span.LogFields(
			otlog.String("event", r.URL.Path),
			otlog.String("value", "finish"),
		)

		log.Print("INFO: TracingMiddleware complete----------------------------------------------")

		return
	})
}

// InjectHeaders injects the necessary opentracing headers to support
// distributed tracing.
func InjectHeaders(r *http.Request) {
	span := opentracing.GlobalTracer().StartSpan(r.URL.Path)
	defer span.Finish()

	log.Printf("INFO: span.Context is %v", span.Context())

	ext.SpanKindRPCClient.Set(span)
	ext.HTTPUrl.Set(span, r.URL.Path)
	ext.HTTPMethod.Set(span, r.Method)
	span.Tracer().Inject(
		span.Context(),
		opentracing.HTTPHeaders,
		opentracing.HTTPHeadersCarrier(r.Header),
	)
}

// TracingRoundTripper implements the http.RoundTripper interface
type TracingRoundTripper struct {
	Proxied http.RoundTripper
}

// RoundTrip injects tracing headers to outbound request.
// TODO: Find a way to make registration less manual.
func (trt TracingRoundTripper) RoundTrip(req *http.Request) (res *http.Response, e error) {
	log.Print("INFO: TracingRoundTripper.RountTrip injecting headers")
	InjectHeaders(req)
	return trt.Proxied.RoundTrip(req)
}
