package main

import (
	"context"
	"log/slog"

	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
)

type dash0MetricsServiceServer struct {
	addr  string
	store MetricsStore

	colmetricspb.UnimplementedMetricsServiceServer
}

func newServer(addr string, store MetricsStore) colmetricspb.MetricsServiceServer {
	return &dash0MetricsServiceServer{addr: addr, store: store}
}

func (m *dash0MetricsServiceServer) Export(ctx context.Context, request *colmetricspb.ExportMetricsServiceRequest) (*colmetricspb.ExportMetricsServiceResponse, error) {
	slog.DebugContext(ctx, "Received ExportMetricsServiceRequest")
	metricsReceivedCounter.Add(ctx, 1)

	if m.store != nil {
		rm := request.GetResourceMetrics()
		if len(rm) == 0 {
			return &colmetricspb.ExportMetricsServiceResponse{}, nil
		}

		batch := MapToBatch(rm)

		if len(batch.Metadata) > 0 {
			metadata := make([]MetadataRow, 0, len(batch.Metadata))
			for _, row := range batch.Metadata {
				metadata = append(metadata, row)
			}
			if err := m.store.InsertMetadata(ctx, metadata); err != nil {
				return nil, err
			}
		}

		if len(batch.Gauges) > 0 {
			if err := m.store.InsertGauge(ctx, batch.Gauges); err != nil {
				return nil, err
			}
		}

		if len(batch.Sums) > 0 {
			if err := m.store.InsertSum(ctx, batch.Sums); err != nil {
				return nil, err
			}
		}
	}

	return &colmetricspb.ExportMetricsServiceResponse{}, nil
}
