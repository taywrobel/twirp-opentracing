package ottwirp

import (
	"context"
	"net/http"
	"strconv"

	ot "github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
	otlog "github.com/opentracing/opentracing-go/log"
	"github.com/twitchtv/twirp"
)

const (
	RequestReceivedEvent = "request.received"
)

type tracingInfoKey struct{}

// TODO: Add functional options for things such as filtering or maybe logging
// custom fields?

// NewOpenTracingHooks provides a twirp.ServerHooks struct which records
// OpenTracing spans.
func NewOpenTracingHooks(tracer ot.Tracer) *twirp.ServerHooks {
	hooks := &twirp.ServerHooks{
		RequestReceived: startTraceSpan(tracer),
		RequestRouted:   handleRequestRouted,
		ResponseSent:    finishTrace,
		Error:           handleError,
	}

	return hooks
}

func startTraceSpan(tracer ot.Tracer) func(context.Context) (context.Context, error) {
	return func(ctx context.Context) (context.Context, error) {
		spanContext, err := extractSpanCtx(ctx, tracer)
		if err != nil && err != ot.ErrSpanContextNotFound { // nolint: megacheck, staticcheck
			// TODO: We need to do error reporting here. The tracer implementation
			// will have to do something because we don't know where this error will
			// live.
		}
		// Create the initial span, it won't have a method name just yet.
		span, ctx := ot.StartSpanFromContext(ctx, RequestReceivedEvent, ext.RPCServerOption(spanContext), ext.SpanKindRPCServer)
		if span != nil {
			span.SetTag("component", "twirp")

			if packageName, ok := twirp.PackageName(ctx); ok {
				span.SetTag("package", packageName)
			}

			if serviceName, ok := twirp.ServiceName(ctx); ok {
				span.SetTag("service", serviceName)
			}
		}

		return ctx, nil
	}
}

// handleRequestRouted sets the operation name because we won't know what it is
// until the RequestRouted hook.
func handleRequestRouted(ctx context.Context) (context.Context, error) {
	span := ot.SpanFromContext(ctx)
	if span != nil {
		if method, ok := twirp.MethodName(ctx); ok {
			span.SetOperationName(method)
		}
	}

	return ctx, nil
}

func finishTrace(ctx context.Context) {
	span := ot.SpanFromContext(ctx)
	if span != nil {
		status, haveStatus := twirp.StatusCode(ctx)
		code, err := strconv.ParseInt(status, 10, 64)
		if haveStatus && err == nil {
			// TODO: Check the status code, if it's a non-2xx/3xx status code, we
			// should probably mark it as an error of sorts.
			span.SetTag("http.status_code", code)
		}

		span.Finish()
	}
}

// WithTraceContext wraps the handler and extracts the span context from request
// headers to attach to the context for connecting client and server calls.
func WithTraceContext(base http.Handler, tracer ot.Tracer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		carrier := ot.HTTPHeadersCarrier(r.Header)
		ctx = context.WithValue(ctx, tracingInfoKey{}, carrier)
		r = r.WithContext(ctx)

		base.ServeHTTP(w, r)
	})
}

func handleError(ctx context.Context, err twirp.Error) context.Context {
	span := ot.SpanFromContext(ctx)
	if span != nil {
		span.SetTag("error", true)
		span.LogFields(otlog.String("event", "error"), otlog.String("message", err.Msg()))
	}

	return ctx
}

func extractSpanCtx(ctx context.Context, tracer ot.Tracer) (ot.SpanContext, error) {
	carrier := ctx.Value(tracingInfoKey{})
	return tracer.Extract(ot.HTTPHeaders, carrier)
}
