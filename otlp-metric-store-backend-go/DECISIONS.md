# Architectural Decisions

## Design: Metadata Normalization

**Decision.** Extract all per-metric metadata (ResourceAttributes, ScopeName, Version,
Attributes, ServiceName, MetricName, etc.) into a shared `metric_metadata` table. Store only
`MetadataHash`, time fields, and type-specific value columns in the per-metric data tables.

**Why.**
- The original schema repeats the full metadata tuple on every data-point row, inflating storage and
  making indexes wider than necessary.
- Separating metadata lets the value tables have a tight ORDER BY of
  `(MetadataHash, TimeUnix)`, aligning the primary index with the predominant access pattern:
  look up a set of metadata hashes, then scan data by time.
- Metadata changes are expected to be low-cardinality (thousands of unique combinations, not
  millions), so the metadata table stays small and `SELECT ... WHERE` queries against it finish in
  single-digit milliseconds.

## Design: Hash as Primary and Foreign Key

**Decision.** Use a deterministic, non-cryptographic xxHash (64-bit) of the canonical metadata
tuple as both the primary key of the metadata table and the foreign key in the value tables. No
auto-increment ID.

**Why.**
- The hash can be computed on the write path without a round-trip to the database — the `MetadataHash
  value` is derived entirely from the ingested data.
- It eliminates the need for an identity column and the associated
  INSERT-then-SELECT-last-ID or sequence-generation machinery.
- The foreign-key column in every value table is a single `UInt64`, keeping the data tables narrow.
- Collision risk is negligible for the expected cardinality (thousands). The application should
  detect and handle a hypothetical collision in production.

**Drawback.** The hash is opaque — debugging a row requires joining against the
metadata table to see the human-readable fields.

## Design: ReplacingMergeTree for Metadata

**Decision.** Use `ReplacingMergeTree` with `ORDER BY (ServiceName, MetricName, Hash)` for the
metadata table. Duplicate rows (same ORDER BY key) are eliminated during merges.

**Why.**
- The hash is deterministic: every insert with a given metadata tuple produces the same hash and the
  same column values. Since `Hash` depends on `ServiceName` and `MetricName`, two rows with the same
  hash always have the same ORDER BY key — dedup works identically to a single-column key.
- It avoids a SELECT-before-INSERT on the write path — just INSERT every metadata tuple alongside
  every batch of data points. The dedup happens asynchronously.
- With `FINAL` (or `SELECT DISTINCT`), queries always see one row per hash.
- Leading with `ServiceName` and `MetricName` aligns the primary index with the dominant Phase 1
  query pattern: filtering by service and metric name uses the primary key instead of a full scan.

## Design: Value Tables ORDER BY (MetadataHash, TimeUnix)

**Decision.** Every per-metric value table uses `ORDER BY (MetadataHash, toUnixTimestamp64Nano(TimeUnix))`
and `PARTITION BY toDate(TimeUnix)`.

**Why.**
- The leading column is the metadata hash, which corresponds to the foreign key from the metadata
  lookup. A query that has resolved `N` metadata hashes can issue
  `WHERE MetadataHash IN (...) AND TimeUnix BETWEEN A AND B`, and ClickHouse uses the primary index
  to seek directly to the relevant rows — sequential reads per hash, no scatter-gather.
- The time-based partition key ensures partition pruning on every query (time is the only mandatory
  filter).

## Design: Two-Phase Query Pattern

**Decision.** Queries execute in two phases:
1. Resolve metadata filters to a set of `MetadataHash` values.
2. Query the relevant value table(s) with those hashes and the time range.

**Why.**
- ClickHouse does not enforce foreign keys or support efficient joins at this
  cardinality without explicit planning. The two-phase pattern gives the application control over
  the execution plan.
- Phase 1 runs against a small table (metadata) where Bloom filters on map columns handle
  arbitrary attribute filters.
- Phase 2 runs against a large table (values) using index seeks on the column that is actually the
  ORDER BY leader, rather than relying on skip-index tricks to query against the grain of the
  primary key.

## Decision: Attributes Included in Metadata

**Decision.** `Attributes` (the per-data-point attribute map in OTLP) is included in the metadata
tuple that determines the hash. They are not stored in the value tables.

**Why.**
- With Attributes moved out of the value tables, the metadata table is the only place they can be
  filtered. Including them in the metadata tuple lets Phase 1 queries use the Bloom filters on
  `mapKeys(Attributes)` / `mapValues(Attributes)` to efficiently prune metadata rows when a query
  filters by an attribute key or value.
- Storing Attributes in the value tables would require a Bloom filter on a column that trails the
  ORDER BY by two positions — less effective than putting it on a small table that can be scanned
  cheaply even without index assistance.

**Trade-off.** Two data points with identical ServiceName, MetricName, etc. but different
Attributes produce different hashes → more metadata rows and the Phase 1 result set is larger.
Since attribute cardinality is assumed low (README), this is acceptable. If unbounded attributes
leak through (e.g. `request_id`), the metadata table grows linearly with distinct values — a
data-quality concern that should be addressed with ingestion validation.
