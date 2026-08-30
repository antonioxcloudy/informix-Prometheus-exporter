package collector

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/anmartsan/prometheus-informix-exporter/internal/informix"
	"github.com/prometheus/client_golang/prometheus"
)

type profileMetricDefinition struct {
	help      string
	valueType prometheus.ValueType
	labels    []string
}

type profileSample struct {
	metric string
	labels []string
	value  float64
}

var profileMetricDefinitions = map[string]profileMetricDefinition{
	"isam_operations_total": {
		help: "Cumulative number of ISAM operations.", valueType: prometheus.CounterValue,
		labels: []string{"informixserver", "operation"},
	},
	"waits_total": {
		help: "Cumulative number of internal waits.", valueType: prometheus.CounterValue,
		labels: []string{"informixserver", "type"},
	},
	"lock_requests_total": {
		help: "Cumulative number of lock requests.", valueType: prometheus.CounterValue,
		labels: []string{"informixserver"},
	},
	"physical_log_writes_total": {
		help: "Cumulative number of physical log writes.", valueType: prometheus.CounterValue,
		labels: []string{"informixserver"},
	},
	"page_reads_total": {
		help: "Cumulative number of page reads.", valueType: prometheus.CounterValue,
		labels: []string{"informixserver"},
	},
	"read_ahead_pages_total": {
		help: "Cumulative number of read-ahead pages.", valueType: prometheus.CounterValue,
		labels: []string{"informixserver", "type"},
	},
	"sequential_scans_total": {
		help: "Cumulative number of sequential scans.", valueType: prometheus.CounterValue,
		labels: []string{"informixserver"},
	},
	"buffer_operations_total": {
		help: "Cumulative buffer and disk page operations by page size.", valueType: prometheus.CounterValue,
		labels: []string{"informixserver", "page_size", "operation"},
	},
	"network_connections_total": {
		help: "Cumulative number of network connections.", valueType: prometheus.CounterValue,
		labels: []string{"informixserver"},
	},
	"sorts_total": {
		help: "Cumulative number of sorts by execution location.", valueType: prometheus.CounterValue,
		labels: []string{"informixserver", "location"},
	},
	"checkpoint_duration_seconds": {
		help: "Duration of the most recent checkpoint in seconds.", valueType: prometheus.GaugeValue,
		labels: []string{"informixserver"},
	},
	"checkpoint_dirty_buffers": {
		help: "Dirty buffers processed by the most recent checkpoint.", valueType: prometheus.GaugeValue,
		labels: []string{"informixserver"},
	},
	"checkpoint_flushes_per_second": {
		help: "Disk flush rate reported for the most recent checkpoint.", valueType: prometheus.GaugeValue,
		labels: []string{"informixserver"},
	},
	"buffer_turnover_ratio": {
		help: "Average buffer pool turnovers per hour since statistics were reset, by page size.", valueType: prometheus.GaugeValue,
		labels: []string{"informixserver", "page_size"},
	},
	"last_backup_timestamp_seconds": {
		help: "Timestamp reported by Informix for the last level-0 backup.", valueType: prometheus.GaugeValue,
		labels: []string{"informixserver"},
	},
	"hdr_last_update_timestamp_seconds": {
		help: "Timestamp reported by Informix for the latest HDR update.", valueType: prometheus.GaugeValue,
		labels: []string{"informixserver"},
	},
}

type ProfileMetrics struct {
	mutex     sync.RWMutex
	instances []informix.Instance
	descs     map[string]*prometheus.Desc
	samples   []profileSample
}

func NewProfile(instances []informix.Instance) *ProfileMetrics {
	descs := make(map[string]*prometheus.Desc, len(profileMetricDefinitions))
	for name, definition := range profileMetricDefinitions {
		descs[name] = prometheus.NewDesc(
			prometheus.BuildFQName("informix", "", name),
			definition.help,
			definition.labels,
			nil,
		)
	}

	return &ProfileMetrics{instances: instances, descs: descs}
}

func (p *ProfileMetrics) Scrape(ctx context.Context) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	var (
		scrapeErr error
		snapshot  []profileSample
	)
	for _, instance := range p.instances {
		samples, err := queryProfile(ctx, instance)
		if err != nil {
			scrapeErr = errors.Join(scrapeErr, fmt.Errorf("profile for %s: %w", instance.Name, err))
			continue
		}
		snapshot = append(snapshot, samples...)
	}

	p.samples = snapshot
	return scrapeErr
}

func (p *ProfileMetrics) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range p.descs {
		ch <- desc
	}
}

func (p *ProfileMetrics) Collect(ch chan<- prometheus.Metric) {
	p.mutex.RLock()
	defer p.mutex.RUnlock()

	for _, sample := range p.samples {
		definition := profileMetricDefinitions[sample.metric]
		ch <- prometheus.MustNewConstMetric(
			p.descs[sample.metric],
			definition.valueType,
			sample.value,
			sample.labels...,
		)
	}
}

type profileMetricTarget struct {
	metric string
	label  string
}

var sharedMemoryMetrics = map[string]profileMetricTarget{
	"pf_isamtot":     {metric: "isam_operations_total", label: "total"},
	"pf_isopens":     {metric: "isam_operations_total", label: "open"},
	"pf_isreads":     {metric: "isam_operations_total", label: "read"},
	"pf_iswrites":    {metric: "isam_operations_total", label: "write"},
	"pf_isrewrites":  {metric: "isam_operations_total", label: "update"},
	"pf_isdeletes":   {metric: "isam_operations_total", label: "delete"},
	"pf_iscommits":   {metric: "isam_operations_total", label: "commit"},
	"pf_isrollbacks": {metric: "isam_operations_total", label: "rollback"},
	"pf_latchwts":    {metric: "waits_total", label: "latch"},
	"pf_buffwts":     {metric: "waits_total", label: "buffer"},
	"pf_lockwts":     {metric: "waits_total", label: "lock"},
	"pf_ckptwts":     {metric: "waits_total", label: "checkpoint"},
	"pf_lockreqs":    {metric: "lock_requests_total"},
	"pf_plgwrites":   {metric: "physical_log_writes_total"},
	"pf_pagreads":    {metric: "page_reads_total"},
	"pf_btradata":    {metric: "read_ahead_pages_total", label: "data"},
	"pf_btraidx":     {metric: "read_ahead_pages_total", label: "index"},
	"pf_dpra":        {metric: "read_ahead_pages_total", label: "direct"},
	"pf_rapgs_used":  {metric: "read_ahead_pages_total", label: "used"},
	"pf_seqscans":    {metric: "sequential_scans_total"},
	"net_connects":   {metric: "network_connections_total"},
	"pf_totalsorts":  {metric: "sorts_total", label: "total"},
	"pf_memsorts":    {metric: "sorts_total", label: "memory"},
	"pf_disksorts":   {metric: "sorts_total", label: "disk"},
}

func queryProfile(ctx context.Context, instance informix.Instance) ([]profileSample, error) {
	var samples []profileSample

	rows, err := instance.Database.QueryContext(ctx, "select name,value from sysshmhdr")
	if err != nil {
		return nil, fmt.Errorf("query sysshmhdr: %w", err)
	}
	for rows.Next() {
		var (
			name  string
			value float64
		)
		if err := rows.Scan(&name, &value); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan sysshmhdr: %w", err)
		}
		name = strings.TrimSpace(name)
		if target, ok := sharedMemoryMetrics[name]; ok {
			labels := []string{instance.Name}
			if target.label != "" {
				labels = append(labels, target.label)
			}
			samples = append(samples, profileSample{metric: target.metric, labels: labels, value: value})
			continue
		}

		if pageSize, operation, ok := parseBufferMetric(name); ok {
			samples = append(samples, profileSample{
				metric: "buffer_operations_total",
				labels: []string{instance.Name, pageSize, operation},
				value:  value,
			})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate sysshmhdr: %w", err)
	}
	rows.Close()

	checkpointRows, err := instance.Database.QueryContext(ctx,
		"select first 1 cp_time::decimal(10,2),n_dirty_buffs,dskflush_per_sec from syscheckpoint order by intvl desc")
	if err != nil {
		return nil, fmt.Errorf("query checkpoint: %w", err)
	}
	if checkpointRows.Next() {
		var duration, dirty, flushes sql.NullFloat64
		if err := checkpointRows.Scan(&duration, &dirty, &flushes); err != nil {
			checkpointRows.Close()
			return nil, fmt.Errorf("scan checkpoint: %w", err)
		}
		samples = append(samples,
			profileSample{metric: "checkpoint_duration_seconds", labels: []string{instance.Name}, value: nullFloat(duration)},
			profileSample{metric: "checkpoint_dirty_buffers", labels: []string{instance.Name}, value: nullFloat(dirty)},
			profileSample{metric: "checkpoint_flushes_per_second", labels: []string{instance.Name}, value: nullFloat(flushes)},
		)
	}
	if err := checkpointRows.Err(); err != nil {
		checkpointRows.Close()
		return nil, fmt.Errorf("iterate checkpoint: %w", err)
	}
	checkpointRows.Close()

	hdrRows, err := instance.Database.QueryContext(ctx, "select lt_time_last_update from sysha_lagtime")
	if err == nil {
		for hdrRows.Next() {
			var value sql.NullFloat64
			if err := hdrRows.Scan(&value); err != nil {
				hdrRows.Close()
				return nil, fmt.Errorf("scan HDR update: %w", err)
			}
			if value.Valid {
				samples = append(samples, profileSample{metric: "hdr_last_update_timestamp_seconds", labels: []string{instance.Name}, value: value.Float64})
			}
		}
		hdrRows.Close()
	}

	backupRows, err := instance.Database.QueryContext(ctx, "select first 1 level0 from sysdbstab order by level0 desc")
	if err != nil {
		return nil, fmt.Errorf("query last backup: %w", err)
	}
	if backupRows.Next() {
		var value sql.NullFloat64
		if err := backupRows.Scan(&value); err != nil {
			backupRows.Close()
			return nil, fmt.Errorf("scan last backup: %w", err)
		}
		if value.Valid {
			samples = append(samples, profileSample{metric: "last_backup_timestamp_seconds", labels: []string{instance.Name}, value: value.Float64})
		}
	}
	backupRows.Close()

	turnoverRows, err := instance.Database.QueryContext(ctx, `select bufsize,
		(((pagreads + bufwrites) / nbuffs) /
		 (select (ROUND(((sh_curtime - sh_pfclrtime)/60)/60)) from sysshmvals))
		from sysbufpool where bufsize > 0`)
	if err != nil {
		return nil, fmt.Errorf("query buffer turnover: %w", err)
	}
	for turnoverRows.Next() {
		var pageSize string
		var value sql.NullFloat64
		if err := turnoverRows.Scan(&pageSize, &value); err != nil {
			turnoverRows.Close()
			return nil, fmt.Errorf("scan buffer turnover: %w", err)
		}
		if value.Valid {
			samples = append(samples, profileSample{metric: "buffer_turnover_ratio", labels: []string{instance.Name, strings.TrimSpace(pageSize)}, value: value.Float64})
		}
	}
	if err := turnoverRows.Err(); err != nil {
		turnoverRows.Close()
		return nil, fmt.Errorf("iterate buffer turnover: %w", err)
	}
	turnoverRows.Close()

	return deduplicateProfileSamples(samples), nil
}

func deduplicateProfileSamples(samples []profileSample) []profileSample {
	unique := make(map[string]profileSample, len(samples))
	for _, sample := range samples {
		key := sample.metric + "\x00" + strings.Join(sample.labels, "\x00")
		unique[key] = sample
	}
	result := make([]profileSample, 0, len(unique))
	for _, sample := range unique {
		result = append(result, sample)
	}
	return result
}

func parseBufferMetric(name string) (pageSize, operation string, ok bool) {
	prefixes := map[string]string{
		"bufreads_":  "buffer_read",
		"bufwrites_": "buffer_write",
		"bufwaits_":  "buffer_wait",
		"pagreads_":  "disk_read",
		"pagwrites_": "disk_write",
	}
	for prefix, operation := range prefixes {
		if strings.HasPrefix(name, prefix) {
			return strings.TrimSuffix(strings.TrimPrefix(name, prefix), "K") + "K", operation, true
		}
	}
	return "", "", false
}

func appendSingleValue(ctx context.Context, instance informix.Instance, samples []profileSample, metric, query string) ([]profileSample, error) {
	rows, err := instance.Database.QueryContext(ctx, query)
	if err != nil {
		return samples, err
	}
	defer rows.Close()

	if rows.Next() {
		var value sql.NullFloat64
		if err := rows.Scan(&value); err != nil {
			return samples, err
		}
		if value.Valid {
			samples = append(samples, profileSample{metric: metric, labels: []string{instance.Name}, value: value.Float64})
		}
	}
	return samples, rows.Err()
}

func nullFloat(value sql.NullFloat64) float64 {
	if !value.Valid {
		return 0
	}
	return value.Float64
}
