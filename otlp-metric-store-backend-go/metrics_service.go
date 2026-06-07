package main

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
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

		mapStart := time.Now()
		batch := MapToBatch(rm)
		mapDur := time.Since(mapStart)
		mappingDurationHistogram.Record(ctx, mapDur.Seconds())
		diags.recordMapping(mapDur)

		if len(batch.Metadata) > 0 {
			metadata := make([]MetadataRow, 0, len(batch.Metadata))
			for _, row := range batch.Metadata {
				metadata = append(metadata, row)
			}
			if err := m.store.InsertMetadata(ctx, metadata); err != nil {
				insertErrorsCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("table", "metadata")))
				diags.recordError()
				return nil, err
			}
			rowsInsertedCounter.Add(ctx, int64(len(metadata)), metric.WithAttributes(attribute.String("table", "metadata")))
			diags.recordRows("metadata", len(metadata))
		}

		if len(batch.Gauges) > 0 {
			if err := m.store.InsertGauge(ctx, batch.Gauges); err != nil {
				insertErrorsCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("table", "gauge")))
				diags.recordError()
				return nil, err
			}
			rowsInsertedCounter.Add(ctx, int64(len(batch.Gauges)), metric.WithAttributes(attribute.String("table", "gauge")))
			diags.recordRows("gauge", len(batch.Gauges))
		}

		if len(batch.Sums) > 0 {
			if err := m.store.InsertSum(ctx, batch.Sums); err != nil {
				insertErrorsCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("table", "sum")))
				diags.recordError()
				return nil, err
			}
			rowsInsertedCounter.Add(ctx, int64(len(batch.Sums)), metric.WithAttributes(attribute.String("table", "sum")))
			diags.recordRows("sum", len(batch.Sums))
		}
	}

	return &colmetricspb.ExportMetricsServiceResponse{}, nil
}
