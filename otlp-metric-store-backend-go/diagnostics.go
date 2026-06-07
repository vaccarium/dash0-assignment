package main

import (
	"fmt"
	"sync/atomic"
	"time"
)

type diagnostics struct {
	// Number of data-point rows successfully inserted, by metric type.
	metadataRows atomic.Int64 // metadata rows inserted (shared across all types)
	gaugeRows    atomic.Int64 // gauge data-point rows inserted
	sumRows      atomic.Int64 // sum data-point rows inserted

	// Number of failed insert attempts.
	insertErrors atomic.Int64

	// Mapping step: count of MapToBatch calls and total wall-clock time.
	mapCount   atomic.Int64
	mapTotalNs atomic.Int64

	// Insert calls per table: count and total wall-clock time.
	metaInsertCount   atomic.Int64
	metaInsertTotalNs atomic.Int64
	gaugeInsertCount  atomic.Int64
	gaugeInsertTotalNs atomic.Int64
	sumInsertCount    atomic.Int64
	sumInsertTotalNs  atomic.Int64
}

var diags diagnostics

func (d *diagnostics) recordRows(table string, n int) {
	switch table {
	case "metadata":
		d.metadataRows.Add(int64(n))
	case "gauge":
		d.gaugeRows.Add(int64(n))
	case "sum":
		d.sumRows.Add(int64(n))
	}
}

func (d *diagnostics) recordMapping(dur time.Duration) {
	d.mapCount.Add(1)
	d.mapTotalNs.Add(dur.Nanoseconds())
}

func (d *diagnostics) recordInsert(table string, dur time.Duration) {
	switch table {
	case "metadata":
		d.metaInsertCount.Add(1)
		d.metaInsertTotalNs.Add(dur.Nanoseconds())
	case "gauge":
		d.gaugeInsertCount.Add(1)
		d.gaugeInsertTotalNs.Add(dur.Nanoseconds())
	case "sum":
		d.sumInsertCount.Add(1)
		d.sumInsertTotalNs.Add(dur.Nanoseconds())
	}
}

func (d *diagnostics) recordError() {
	d.insertErrors.Add(1)
}

func (d *diagnostics) dump() string {
	return fmt.Sprintf(
		"rows: metadata=%d gauge=%d sum=%d | errors=%d | "+
			"mapping: count=%d avg=%s | "+
			"inserts: metadata(count=%d avg=%s) gauge(count=%d avg=%s) sum(count=%d avg=%s)",
		d.metadataRows.Load(),
		d.gaugeRows.Load(),
		d.sumRows.Load(),
		d.insertErrors.Load(),
		d.mapCount.Load(),
		avgDur(d.mapCount.Load(), d.mapTotalNs.Load()),
		d.metaInsertCount.Load(),
		avgDur(d.metaInsertCount.Load(), d.metaInsertTotalNs.Load()),
		d.gaugeInsertCount.Load(),
		avgDur(d.gaugeInsertCount.Load(), d.gaugeInsertTotalNs.Load()),
		d.sumInsertCount.Load(),
		avgDur(d.sumInsertCount.Load(), d.sumInsertTotalNs.Load()),
	)
}

func avgDur(count, totalNs int64) time.Duration {
	if count == 0 {
		return 0
	}
	return time.Duration(totalNs / count)
}
