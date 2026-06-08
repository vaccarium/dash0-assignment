# OTLP Metric Storage (Go) Assignment

## Usage

Create tables in ClickHouse:
```
go run . migrate --config config.toml
```

Run:
```
go run . --config config.toml
```

Alternatively, you can use the included demo script to launch the server, send some data to it,
and showcase stats output. The script depends on Docker to be installed.
```
# create ClickHouse tables if necessary
go run . migrate --config config.toml

./run-dev.sh
```

The server supports outputting stats, either on schedule or on receiving an SIGUSR1 signal.
Please see config.toml for an overview of possible configuration.

# Development

This code was developed using a coding agent (Claude Code with deepseek-v4-pro). An agent-generated
log of the main decisions taken while designing the solution is available in DECISIONS.md.

# Known Issues

- The stats output is a bit too noisy. I think this is fine for an assignment; in a real life
  scenario, I would have put more thought into the debug output.
- Since the metric query patterns we're expected to serve are not known to me, I've had to make
  some assumptions. Most importantly, I've assumed that our queries always include (ServiceName, 
  MetricName). Queries that do not can still be served; however, they cause a full scan in the
  metadata table (which I, however, expect to be small). While the original README.md did say 
  there should be no full scans, optimising for every theoretically possible query is unpractical,
  especially with limited development time.
- Expanding on the above, I have not made any optimisations for common attributes.
- The gRPC server uses plaintext transport, as it did in the provided scaffolding.
  In a real-life deployment, the server could be required to use TLS; alternatively,
  TLS termination could be happening at the load balancer or service mesh.

# Assumptions

This section is agent-generated.

- **Metadata cardinality is bounded.** The number of unique (ServiceName, MetricName,
  Attributes) combinations is expected to be in the hundreds to low thousands. Quantified:
  up to ~10K metadata rows is fine; at 1M+ the ReplacingMergeTree merges and bloom filter
  indexes on the Map columns begin to degrade. If a high-cardinality value like a request
  ID leaks into Attributes, the metadata table grows without bound.
- **`service.name` is present on every resource.** It is extracted from Resource attributes
  and used as the leading column in the metadata ORDER BY. When absent, it defaults to an
  empty string, which is correct but could create a single hot partition in the metadata table.
- **64-bit xxHash collision risk is negligible.** At the expected cardinality (~10K rows),
  the collision probability is roughly 10^(-12). This assumption breaks if the metadata row
  count reaches billions.
- **Histogram, ExponentialHistogram, and Summary metric types are defined in the schema but
  not wired in the write path.** `MapToBatch` only produces rows for gauge and sum. The
  DDL for the remaining types exists but is unreachable.
- **ReplacingMergeTree async dedup is sufficient.** Readers may briefly observe duplicate
  metadata rows before a background merge eliminates them. The write path does not use
  `FINAL` or `SELECT DISTINCT`.
- **Single-node setup.** No replication, sharding, or distributed table configured in ClickHouse,
  and likewise no support for multiple instances of the server.
- **Most metric queries filter by ServiceName and MetricName.** The metadata table's
  `ORDER BY (, MetricName, Hash)` is designed for this access pattern. Queries
  that filter solely by attribute key/value pairs still require a full metadata scan.
- **Attribute maps are low-cardinality.** The bloom filter indexes on `mapKeys` and
  `mapValues` work well for a few hundred distinct keys. If every data point carries a
  unique attribute, the indexes fill up and become ineffective.
