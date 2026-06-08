#!/usr/bin/env bash
set -euo pipefail

# 1. Start ClickHouse (skip if already running)
if ! docker ps --format '{{.Names}}' | grep -q '^clickhouse-otel$'; then
    docker run -d --name clickhouse-otel \
        -p 9000:9000 \
        -e CLICKHOUSE_USER=default \
        -e CLICKHOUSE_PASSWORD=test \
        clickhouse/clickhouse-server:26.2
    echo "ClickHouse container started."
else
    echo "ClickHouse container already running."
fi

# 2. Start the server (background, Ctrl+C to stop)
go run . --config config.toml &
SERVER_PID=$!
trap "kill $SERVER_PID 2>/dev/null; exit" INT TERM
sleep 3
echo "Server running on localhost:4317 (PID $SERVER_PID)"

# 3. Example: list services
echo
echo "--- Services ---"
set +e
grpcurl -plaintext localhost:4317 list

# 4. Example: send a gauge metric
NOW_NS="$(date +%s)000000000"

echo
echo "--- Sending gauge ---"

cat <<EOF | grpcurl -plaintext -d @ localhost:4317 \
    opentelemetry.proto.collector.metrics.v1.MetricsService/Export
{
  "resource_metrics": [{
    "resource": {
      "attributes": [
        {"key": "service.name", "value": {"string_value": "my-app"}}
      ]
    },
    "scope_metrics": [{
      "scope": {"name": "my-scope", "version": "1.0.0"},
      "metrics": [{
        "name": "cpu.utilization",
        "description": "CPU usage percent",
        "unit": "%",
        "gauge": {
          "data_points": [{
            "time_unix_nano": "$NOW_NS",
            "as_double": 73.5
          }]
        }
      }]
    }]
  }]
}
EOF
set -e

echo
echo "--- Send SIGUSR1 to see diagnostics: kill -SIGUSR1 $SERVER_PID ---"
echo "--- Press Ctrl+C to stop ---"
wait -f $SERVER_PID
