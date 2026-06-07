package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var (
	listenAddr            = flag.String("listenAddr", "localhost:4317", "The listen address")
	maxReceiveMessageSize = flag.Int("maxReceiveMessageSize", 16777216, "The max message size in bytes the server can receive")
	diagnosticInterval    = flag.Duration("diagnosticInterval", 0, "If >0, periodically log diagnostic counters at this interval")
)

const name = "dash0.com/otlp-log-processor-backend"

var (
	meter                  = otel.Meter(name)
	logger                 = otelslog.NewLogger(name)
	metricsReceivedCounter metric.Int64Counter
	rowsInsertedCounter    metric.Int64Counter
	insertErrorsCounter    metric.Int64Counter
	insertLatencyHistogram metric.Float64Histogram
	mappingDurationHistogram metric.Float64Histogram
)

func init() {
	var err error
	metricsReceivedCounter, err = meter.Int64Counter("com.dash0.homeexercise.metrics.received",
		metric.WithDescription("The number of metrics received by otlp-metrics-processor-backend"),
		metric.WithUnit("{metric}"))
	if err != nil {
		panic(err)
	}
	rowsInsertedCounter, err = meter.Int64Counter("com.dash0.homeexercise.rows.inserted",
		metric.WithDescription("The number of data-point rows inserted into ClickHouse"),
		metric.WithUnit("{row}"))
	if err != nil {
		panic(err)
	}
	insertErrorsCounter, err = meter.Int64Counter("com.dash0.homeexercise.insert.errors",
		metric.WithDescription("The number of failed ClickHouse insert operations"),
		metric.WithUnit("{error}"))
	if err != nil {
		panic(err)
	}
	insertLatencyHistogram, err = meter.Float64Histogram("com.dash0.homeexercise.insert.latency",
		metric.WithDescription("Latency of ClickHouse insert operations"),
		metric.WithUnit("s"))
	if err != nil {
		panic(err)
	}
	mappingDurationHistogram, err = meter.Float64Histogram("com.dash0.homeexercise.mapping.duration",
		metric.WithDescription("Duration of MapToBatch calls"),
		metric.WithUnit("s"))
	if err != nil {
		panic(err)
	}
}

func main() {
	if err := run(); err != nil {
		log.Fatalln(err)
	}
}

func run() (err error) {
	slog.SetDefault(logger)
	logger.Info("Starting application")

	// Set up OpenTelemetry.
	otelShutdown, err := setupOTelSDK(context.Background())
	if err != nil {
		return
	}

	// Handle shutdown properly so nothing leaks.
	defer func() {
		err = errors.Join(err, otelShutdown(context.Background()))
	}()

	flag.Parse()

	slog.Debug("Starting listener", slog.String("listenAddr", *listenAddr))
	listener, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		return err
	}

	grpcServer := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.MaxRecvMsgSize(*maxReceiveMessageSize),
		grpc.Creds(insecure.NewCredentials()),
	)
	colmetricspb.RegisterMetricsServiceServer(grpcServer, newServer(*listenAddr, nil))

	slog.Debug("Starting gRPC server")

	startDiagnostics()

	return grpcServer.Serve(listener)
}

func startDiagnostics() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGUSR1)
	go func() {
		for range sigCh {
			fmt.Fprintf(os.Stderr, "[diagnostics] %s\n", diags.dump())
		}
	}()

	if *diagnosticInterval > 0 {
		slog.Info("Periodic diagnostics enabled", slog.Duration("interval", *diagnosticInterval))
		go func() {
			ticker := time.NewTicker(*diagnosticInterval)
			defer ticker.Stop()
			for range ticker.C {
				fmt.Fprintf(os.Stderr, "[diagnostics] %s\n", diags.dump())
			}
		}()
	}
}
