package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
)

var (
	listenAddr            string
	maxReceiveMessageSize int
	diagnosticInterval    time.Duration
	enableReflection      bool
	clickhouseAddr        string
	clickhouseDatabase    string
	clickhouseUsername    string
	clickhousePassword    string
	configFile            string
)

var rootCmd = &cobra.Command{
	Use:  "otlp-metric-store-backend",
	Long: "OTLP metric store backend — receives OTLP metrics via gRPC and stores them in ClickHouse.",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if configFile == "" {
			return nil
		}
		cfg, err := loadConfig(configFile)
		if err != nil {
			return err
		}
		applyConfig(cmd, cfg)
		return nil
	},
	RunE: run,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&listenAddr, "listenAddr", "localhost:4317", "The listen address")
	rootCmd.PersistentFlags().IntVar(&maxReceiveMessageSize, "maxReceiveMessageSize", 16777216, "The max message size in bytes the server can receive")
	rootCmd.PersistentFlags().DurationVar(&diagnosticInterval, "diagnosticInterval", 0, "If >0, periodically log diagnostic counters at this interval")
	rootCmd.PersistentFlags().BoolVar(&enableReflection, "enableReflection", true, "Enable gRPC server reflection (for grpcurl)")
	rootCmd.PersistentFlags().StringVar(&clickhouseAddr, "clickhouseAddr", "", "ClickHouse server address (host:port); if empty, metrics are not persisted")
	rootCmd.PersistentFlags().StringVar(&clickhouseDatabase, "clickhouseDatabase", "default", "ClickHouse database name")
	rootCmd.PersistentFlags().StringVar(&clickhouseUsername, "clickhouseUsername", "default", "ClickHouse username")
	rootCmd.PersistentFlags().StringVar(&clickhousePassword, "clickhousePassword", "", "ClickHouse password")
	rootCmd.PersistentFlags().StringVar(&configFile, "config", "", "Path to TOML config file")

	rootCmd.AddCommand(migrateCmd)
}

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Create ClickHouse tables and exit",
	RunE: func(cmd *cobra.Command, args []string) error {
		if clickhouseAddr == "" {
			return fmt.Errorf("--clickhouseAddr is required")
		}
		ctx := context.Background()
		store, err := NewClickHouseMetricsStore(ctx, clickhouseAddr, clickhouseDatabase, clickhouseUsername, clickhousePassword)
		if err != nil {
			return err
		}
		defer store.Close()
		if err := store.CreateTables(ctx); err != nil {
			return fmt.Errorf("creating tables: %w", err)
		}
		fmt.Println("Tables created successfully.")
		return nil
	},
}

const name = "dash0.com/otlp-log-processor-backend"

var (
	meter                    = otel.Meter(name)
	logger                   = otelslog.NewLogger(name)
	metricsReceivedCounter   metric.Int64Counter
	rowsInsertedCounter      metric.Int64Counter
	insertErrorsCounter      metric.Int64Counter
	insertLatencyHistogram   metric.Float64Histogram
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
	if err := rootCmd.Execute(); err != nil {
		log.Fatalln(err)
	}
}

func run(cmd *cobra.Command, args []string) (err error) {
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

	var store MetricsStore
	if clickhouseAddr != "" {
		slog.Info("Connecting to ClickHouse", slog.String("addr", clickhouseAddr))
		chStore, err := NewClickHouseMetricsStore(context.Background(), clickhouseAddr, clickhouseDatabase, clickhouseUsername, clickhousePassword)
		if err != nil {
			return err
		}
		defer func() {
			if cerr := chStore.Close(); cerr != nil {
				err = errors.Join(err, cerr)
			}
		}()
		store = chStore
	} else {
		slog.Warn("No ClickHouse address configured; metrics will not be persisted")
	}

	slog.Debug("Starting listener", slog.String("listenAddr", listenAddr))
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return err
	}

	grpcServer := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.MaxRecvMsgSize(maxReceiveMessageSize),
		grpc.Creds(insecure.NewCredentials()),
	)
	colmetricspb.RegisterMetricsServiceServer(grpcServer, newServer(listenAddr, store))

	healthServer := health.NewServer()
	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(grpcServer, healthServer)
	if enableReflection {
		reflection.Register(grpcServer)
	}

	slog.Debug("Starting gRPC server")

	startDiagnostics()

	// Graceful shutdown on SIGTERM/SIGINT.
	shutdownCh := make(chan os.Signal, 1)
	signal.Notify(shutdownCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-shutdownCh
		fmt.Fprintf(os.Stderr, "[diagnostics] received %s, initiating graceful shutdown\n", sig.String())
		healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING)
		grpcServer.GracefulStop()
	}()

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

	if diagnosticInterval > 0 {
		slog.Info("Periodic diagnostics enabled", slog.Duration("interval", diagnosticInterval))
		go func() {
			ticker := time.NewTicker(diagnosticInterval)
			defer ticker.Stop()
			for range ticker.C {
				fmt.Fprintf(os.Stderr, "[diagnostics] %s\n", diags.dump())
			}
		}()
	}
}
