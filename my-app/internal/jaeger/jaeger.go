package jaeger

import (
	"fmt"
	"io"
	"os"

	"github.com/opentracing/opentracing-go"
	"github.com/uber/jaeger-client-go"
	"github.com/uber/jaeger-client-go/config"
)

// Jaeger open-tracing instance
var Tracer opentracing.Tracer

// InitializeJaeger Initializes jaeger tracing variable
func InitializeJaeger() (opentracing.Tracer, io.Closer) {
	os.Setenv("JAEGER_SERVICE_NAME", "Broker")
	os.Setenv("JAEGER_SAMPLER_PARAM", "1")

	os.Setenv("JAEGER_AGENT_HOST", "jaeger")
	os.Setenv("JAEGER_AGENT_PORT", "5775")

	cfg, err := config.FromEnv()
	if err != nil {
		panic(fmt.Sprintf("Failed reading Jaeger env vars: %v\n", err))
	}

	tracer, closer, err := cfg.NewTracer(config.Logger(jaeger.StdLogger))
	if err != nil {
		panic(fmt.Sprintf("Cannot initialize Jaeger: %v\n", err))
	}

	Tracer = tracer

	return tracer, closer
}
