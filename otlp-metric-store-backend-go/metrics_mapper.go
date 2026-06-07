package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cespare/xxhash/v2"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// MetricBatch holds the result of mapping an ExportMetricsServiceRequest
// into deduplicated metadata rows and thin data-point rows.
type MetricBatch struct {
	Metadata map[uint64]MetadataRow
	Gauges   []ThinGaugeRow
	Sums     []ThinSumRow
}

// canonicalMap serializes a map to a deterministic string: sorted keys,
// "key=value" lines separated by newlines. Empty map returns "".
func canonicalMap(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(m[k])
		b.WriteByte('\n')
	}
	return b.String()
}

// metadataHashForGauge computes the xxHash-64 of all metadata fields for a gauge
// data point, including a type discriminator to prevent cross-type collisions.
func metadataHashForGauge(resource resourceFields, scope scopeFields, metric metricFields, attrs map[string]string) uint64 {
	d := xxhash.New()
	d.WriteString("gauge\n")
	d.WriteString(scope.schemaURL)
	d.WriteString(resource.schemaURL)
	d.WriteString(canonicalMap(scope.attributes))
	d.WriteString(fmt.Sprintf("%d\n", scope.droppedAttrCount))
	d.WriteString(scope.name)
	d.WriteString(scope.version)
	d.WriteString(canonicalMap(resource.attributes))
	d.WriteString(resource.serviceName)
	d.WriteString(metric.name)
	d.WriteString(metric.description)
	d.WriteString(metric.unit)
	d.WriteString(canonicalMap(attrs))
	return d.Sum64()
}

// metadataHashForSum computes the xxHash-64 of all metadata fields for a sum
// data point. Includes sum-specific fields (aggregation temporality, monotonicity)
// so a sum and a gauge with otherwise identical fields produce different hashes.
func metadataHashForSum(resource resourceFields, scope scopeFields, metric metricSumFields, attrs map[string]string) uint64 {
	d := xxhash.New()
	d.WriteString("sum\n")
	d.WriteString(scope.schemaURL)
	d.WriteString(resource.schemaURL)
	d.WriteString(canonicalMap(scope.attributes))
	d.WriteString(fmt.Sprintf("%d\n", scope.droppedAttrCount))
	d.WriteString(scope.name)
	d.WriteString(scope.version)
	d.WriteString(canonicalMap(resource.attributes))
	d.WriteString(resource.serviceName)
	d.WriteString(metric.name)
	d.WriteString(metric.description)
	d.WriteString(metric.unit)
	d.WriteString(canonicalMap(attrs))
	d.WriteString(fmt.Sprintf("%d\n", metric.aggregationTemporality))
	d.WriteString(fmt.Sprintf("%t\n", metric.isMonotonic))
	return d.Sum64()
}

// resourceFields holds parsed resource-level metadata.
type resourceFields struct {
	attributes  map[string]string
	schemaURL   string
	serviceName string
}

// scopeFields holds parsed scope-level metadata.
type scopeFields struct {
	name             string
	version          string
	attributes       map[string]string
	droppedAttrCount uint32
	schemaURL        string
}

// metricFields holds parsed metric-level metadata for a gauge.
type metricFields struct {
	name        string
	description string
	unit        string
}

// metricSumFields holds parsed metric-level metadata for a sum.
type metricSumFields struct {
	name                   string
	description            string
	unit                   string
	aggregationTemporality int32
	isMonotonic            bool
}

// serviceName extracts the service.name from resource attributes, returning "" if not found.
func serviceName(resource *resourcepb.Resource) string {
	if resource == nil {
		return ""
	}
	for _, attr := range resource.GetAttributes() {
		if attr.GetKey() == "service.name" {
			return attr.GetValue().GetStringValue()
		}
	}
	return ""
}

// kvToMap converts a slice of OTLP KeyValue pairs to a Go map.
func kvToMap(attrs []*commonpb.KeyValue) map[string]string {
	m := make(map[string]string, len(attrs))
	for _, kv := range attrs {
		m[kv.GetKey()] = anyValueToString(kv.GetValue())
	}
	return m
}

// anyValueToString converts an OTLP AnyValue to its string representation.
func anyValueToString(v *commonpb.AnyValue) string {
	if v == nil {
		return ""
	}
	switch v.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return v.GetStringValue()
	case *commonpb.AnyValue_IntValue:
		return fmt.Sprintf("%d", v.GetIntValue())
	case *commonpb.AnyValue_DoubleValue:
		return fmt.Sprintf("%g", v.GetDoubleValue())
	case *commonpb.AnyValue_BoolValue:
		return fmt.Sprintf("%t", v.GetBoolValue())
	default:
		return fmt.Sprintf("%v", v)
	}
}

// nanosToTime converts a uint64 nanoseconds-since-epoch to time.Time.
func nanosToTime(nanos uint64) time.Time {
	return time.Unix(0, int64(nanos))
}

// numberDataPointValue extracts the float64 value from a NumberDataPoint.
func numberDataPointValue(dp *metricspb.NumberDataPoint) float64 {
	switch v := dp.GetValue().(type) {
	case *metricspb.NumberDataPoint_AsDouble:
		return v.AsDouble
	case *metricspb.NumberDataPoint_AsInt:
		return float64(v.AsInt)
	default:
		return 0
	}
}

// MapToBatch converts a slice of ResourceMetrics into a MetricBatch containing
// deduplicated metadata rows and thin data-point rows for all metric types.
func MapToBatch(resourceMetrics []*metricspb.ResourceMetrics) *MetricBatch {
	batch := &MetricBatch{
		Metadata: make(map[uint64]MetadataRow),
	}

	for _, rm := range resourceMetrics {
		resource := resourceFields{
			attributes:  kvToMap(rm.GetResource().GetAttributes()),
			schemaURL:   rm.GetSchemaUrl(),
			serviceName: serviceName(rm.GetResource()),
		}

		for _, sm := range rm.GetScopeMetrics() {
			rawScope := sm.GetScope()
			scope := scopeFields{
				name:             rawScope.GetName(),
				version:          rawScope.GetVersion(),
				attributes:       kvToMap(rawScope.GetAttributes()),
				droppedAttrCount: rawScope.GetDroppedAttributesCount(),
				schemaURL:        sm.GetSchemaUrl(),
			}

			for _, metric := range sm.GetMetrics() {
				// --- Gauge ---
				if gauge := metric.GetGauge(); gauge != nil {
					mf := metricFields{
						name:        metric.GetName(),
						description: metric.GetDescription(),
						unit:        metric.GetUnit(),
					}
					for _, dp := range gauge.GetDataPoints() {
						attrs := kvToMap(dp.GetAttributes())
						h := metadataHashForGauge(resource, scope, mf, attrs)

						if _, exists := batch.Metadata[h]; !exists {
							batch.Metadata[h] = MetadataRow{
								Hash:                  h,
								ResourceAttributes:    resource.attributes,
								ResourceSchemaUrl:     resource.schemaURL,
								ScopeName:             scope.name,
								ScopeVersion:          scope.version,
								ScopeAttributes:       scope.attributes,
								ScopeDroppedAttrCount: scope.droppedAttrCount,
								ScopeSchemaUrl:        scope.schemaURL,
								ServiceName:           resource.serviceName,
								MetricName:            mf.name,
								MetricDescription:     mf.description,
								MetricUnit:            mf.unit,
								Attributes:            attrs,
							}
						}

						batch.Gauges = append(batch.Gauges, ThinGaugeRow{
							MetadataHash:  h,
							StartTimeUnix: nanosToTime(dp.GetStartTimeUnixNano()),
							TimeUnix:      nanosToTime(dp.GetTimeUnixNano()),
							Value:         numberDataPointValue(dp),
							Flags:         dp.GetFlags(),
						})
					}
				}

				// --- Sum ---
				if sum := metric.GetSum(); sum != nil {
					aggTemp := int32(sum.GetAggregationTemporality())
					isMonotonic := sum.GetIsMonotonic()
					mf := metricSumFields{
						name:                   metric.GetName(),
						description:            metric.GetDescription(),
						unit:                   metric.GetUnit(),
						aggregationTemporality: aggTemp,
						isMonotonic:            isMonotonic,
					}

					for _, dp := range sum.GetDataPoints() {
						attrs := kvToMap(dp.GetAttributes())
						h := metadataHashForSum(resource, scope, mf, attrs)

						if _, exists := batch.Metadata[h]; !exists {
							aggTempCopy := aggTemp
							isMonotonicCopy := isMonotonic
							batch.Metadata[h] = MetadataRow{
								Hash:                   h,
								ResourceAttributes:     resource.attributes,
								ResourceSchemaUrl:      resource.schemaURL,
								ScopeName:              scope.name,
								ScopeVersion:           scope.version,
								ScopeAttributes:        scope.attributes,
								ScopeDroppedAttrCount:  scope.droppedAttrCount,
								ScopeSchemaUrl:         scope.schemaURL,
								ServiceName:            resource.serviceName,
								MetricName:             mf.name,
								MetricDescription:      mf.description,
								MetricUnit:             mf.unit,
								Attributes:             attrs,
								AggregationTemporality: &aggTempCopy,
								IsMonotonic:            &isMonotonicCopy,
							}
						}

						batch.Sums = append(batch.Sums, ThinSumRow{
							MetadataHash:  h,
							StartTimeUnix: nanosToTime(dp.GetStartTimeUnixNano()),
							TimeUnix:      nanosToTime(dp.GetTimeUnixNano()),
							Value:         numberDataPointValue(dp),
							Flags:         dp.GetFlags(),
						})
					}
				}
			}
		}
	}

	return batch
}
