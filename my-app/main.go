package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"time"

	"therealbroker/api/proto"
	"therealbroker/internal/module"
	"therealbroker/pkg/broker"

	"google.golang.org/grpc"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"therealbroker/internal/jaeger"

	"github.com/opentracing/opentracing-go"

	_ "net/http/pprof"
)

var (
	rpcMethodCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rpc_method_count",
			Help: "Number of RPC method calls",
		},
		[]string{"method", "status"},
	)

	rpcMethodDuration = prometheus.NewSummaryVec(
		prometheus.SummaryOpts{
			Name:       "rpc_method_duration",
			Help:       "Latency distribution of RPC method calls",
			Objectives: map[float64]float64{0.5: 0.05, 0.95: 0.01, 0.99: 0.001},
		},
		[]string{"method"},
	)
)

type BrokerService struct {
	proto.BrokerServer
	broker.Broker
	activeSubscribers *prometheus.GaugeVec
}

func (s *BrokerService) Publish(ctx context.Context, req *proto.PublishRequest) (*proto.PublishResponse, error) {
	span := jaeger.Tracer.StartSpan("Publish-API")
	defer span.Finish()

	ctxSpan := opentracing.ContextWithSpan(context.Background(), span)

	start := time.Now()
	defer func() {
		methodDuration := time.Since(start).Seconds()
		rpcMethodDuration.WithLabelValues("Publish").Observe(methodDuration)
	}()

	log.Printf("Received Publish request for subject: %s", req.Subject)

	msg := broker.Message{
		Body:       string(req.Body),
		Expiration: time.Duration(req.ExpirationSeconds) * time.Second,
	}

	id, err := s.Broker.Publish(ctxSpan, req.Subject, msg)
	if err != nil {
		rpcMethodCount.WithLabelValues("Publish", "error").Inc()
		log.Printf("Publish failed: %v", err)
		return nil, err
	}

	rpcMethodCount.WithLabelValues("Publish", "success").Inc()
	log.Printf("Publish successful. Message ID: %d", id)

	return &proto.PublishResponse{Id: int32(id)}, nil
}

func (s *BrokerService) Subscribe(req *proto.SubscribeRequest, stream proto.Broker_SubscribeServer) error {
	span := jaeger.Tracer.StartSpan("Subscribe-API")
	defer span.Finish()

	start := time.Now()
	defer func() {
		methodDuration := time.Since(start).Seconds()
		rpcMethodDuration.WithLabelValues("Subscribe").Observe(methodDuration)
	}()

	log.Printf("Received Subscribe request for subject: %s", req.Subject)

	subChan, err := s.Broker.Subscribe(stream.Context(), req.Subject)
	if err != nil {
		rpcMethodCount.WithLabelValues("Subscribe", "error").Inc()
		log.Printf("Subscribe failed: %v", err)
		return err
	}

	rpcMethodCount.WithLabelValues("Subscribe", "success").Inc()
	s.activeSubscribers.WithLabelValues(req.Subject).Inc()
	defer s.activeSubscribers.WithLabelValues(req.Subject).Dec()

	for {
		select {
		case <-stream.Context().Done():
			log.Printf("Subscription stream closed for subject: %s", req.Subject)
			return nil
		case msg := <-subChan:
			if err := stream.Send(&proto.MessageResponse{Body: []byte(msg.Body)}); err != nil {
				log.Printf("Error sending message to subscriber: %v", err)
				return err
			}
		}
	}
}

func (s *BrokerService) Fetch(ctx context.Context, req *proto.FetchRequest) (*proto.MessageResponse, error) {
	span := jaeger.Tracer.StartSpan("Fetch-API")
	defer span.Finish()

	start := time.Now()
	defer func() {
		methodDuration := time.Since(start).Seconds()
		rpcMethodDuration.WithLabelValues("Fetch").Observe(methodDuration)
	}()

	log.Printf("Received Fetch request for subject: %s, ID: %d", req.Subject, req.Id)

	msg, err := s.Broker.Fetch(ctx, req.Subject, int(req.Id))
	if err != nil {
		rpcMethodCount.WithLabelValues("Fetch", "error").Inc()
		log.Printf("Fetch failed: %v", err)
		return nil, err
	}

	rpcMethodCount.WithLabelValues("Fetch", "success").Inc()
	log.Printf("Fetch successful for subject: %s, ID: %d", req.Subject, req.Id)

	return &proto.MessageResponse{Body: []byte(msg.Body)}, nil
}

func main() {
	tracer, closer := jaeger.InitializeJaeger()
	defer closer.Close()
	opentracing.SetGlobalTracer(tracer)

	go func() {
		log.Println(http.ListenAndServe("localhost:6060", nil))
	}()

	listenAddr := "0.0.0.0:5052"

	lis, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	brokerModule := module.NewModule()

	activeSubscribers := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "active_subscribers",
			Help: "Number of active subscribers for each subject",
		},
		[]string{"subject"},
	)

	reg := prometheus.NewRegistry()
	reg.MustRegister(rpcMethodCount, rpcMethodDuration, activeSubscribers)

	promHandler := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})

	http.Handle("/metrics", promHandler)

	go func() {
		log.Fatal(http.ListenAndServe(":8081", nil))
	}()

	grpcServer := grpc.NewServer()
	proto.RegisterBrokerServer(grpcServer, &BrokerService{
		Broker:            brokerModule,
		activeSubscribers: activeSubscribers,
	})

	log.Printf("gRPC server listening on %s", listenAddr)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}

	select {}

}
