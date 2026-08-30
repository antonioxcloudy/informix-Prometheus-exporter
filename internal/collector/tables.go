package collector

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/anmartsan/prometheus-informix-exporter/internal/config"
	"github.com/anmartsan/prometheus-informix-exporter/internal/informix"
	"github.com/prometheus/client_golang/prometheus"
)

type tableLimitMetricDefinition struct {
	help   string
	labels []string
}

type tableLimitSample struct {
	metric string
	labels []string
	value  float64
}

var tableLimitMetricDefinitions = map[string]tableLimitMetricDefinition{
	"table_extents": {
		help:   "Total extents aggregated across all partitions of the table.",
		labels: []string{"informixserver", "database", "owner", "table"},
	},
	"table_allocated_bytes": {
		help:   "Bytes allocated across all partitions of the table.",
		labels: []string{"informixserver", "database", "owner", "table"},
	},
	"table_used_bytes": {
		help:   "Bytes used across all partitions of the table.",
		labels: []string{"informixserver", "database", "owner", "table"},
	},
	"partition_allocated_pages": {
		help:   "Pages allocated to a table partition.",
		labels: []string{"informixserver", "database", "owner", "table", "partition"},
	},
	"partition_used_pages": {
		help:   "Pages used by a table partition.",
		labels: []string{"informixserver", "database", "owner", "table", "partition"},
	},
	"partition_page_size_bytes": {
		help:   "Page size of a table partition in bytes.",
		labels: []string{"informixserver", "database", "owner", "table", "partition"},
	},
	"partition_classic_page_limit": {
		help:   "Configured classic ROWID page limit used for monitoring this partition.",
		labels: []string{"informixserver", "database", "owner", "table", "partition"},
	},
	"partition_classic_page_limit_ratio": {
		help:   "Used pages divided by the configured classic ROWID page limit; LARGE tables can exceed this model.",
		labels: []string{"informixserver", "database", "owner", "table", "partition"},
	},
}

type TableLimitMetrics struct {
	mutex     sync.RWMutex
	instances []informix.Instance
	config    config.TableLimitConfig
	descs     map[string]*prometheus.Desc
	samples   []tableLimitSample
}

func NewTables(instances []informix.Instance, limits config.TableLimitConfig) *TableLimitMetrics {
	descs := make(map[string]*prometheus.Desc, len(tableLimitMetricDefinitions))
	for name, definition := range tableLimitMetricDefinitions {
		descs[name] = prometheus.NewDesc(
			prometheus.BuildFQName("informix", "", name), definition.help, definition.labels, nil,
		)
	}
	return &TableLimitMetrics{instances: instances, config: limits, descs: descs}
}

func (t *TableLimitMetrics) Scrape(ctx context.Context) error {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	var (
		scrapeErr error
		snapshot  []tableLimitSample
	)
	for _, instance := range t.instances {
		samples, err := queryTableLimits(ctx, instance, t.config)
		if err != nil {
			scrapeErr = errors.Join(scrapeErr, fmt.Errorf("table limits for %s: %w", instance.Name, err))
			continue
		}
		snapshot = append(snapshot, samples...)
	}

	t.samples = snapshot
	return scrapeErr
}

func (t *TableLimitMetrics) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range t.descs {
		ch <- desc
	}
}

func (t *TableLimitMetrics) Collect(ch chan<- prometheus.Metric) {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	for _, sample := range t.samples {
		ch <- prometheus.MustNewConstMetric(t.descs[sample.metric], prometheus.GaugeValue, sample.value, sample.labels...)
	}
}

type tableIdentity struct {
	database, owner, table string
}

type tableAggregate struct {
	extents, allocatedBytes, usedBytes int64
	maximumPartitionPages              int64
}

type partitionSnapshot struct {
	tableIdentity
	partnum                   int64
	allocatedPages, usedPages int64
	extents, pageSize         int64
}

var systemDatabases = map[string]struct{}{
	"sysadmin": {}, "sysmaster": {}, "sysuser": {}, "sysutils": {}, "syscdr": {},
}

func queryTableLimits(ctx context.Context, instance informix.Instance, limits config.TableLimitConfig) ([]tableLimitSample, error) {
	rows, err := instance.Database.QueryContext(ctx, `select trim(n.dbsname), trim(n.owner), trim(n.tabname),
		p.partnum, p.nptotal, p.npused, p.nextns, p.pagesize
		from systabnames n, sysptnhdr p
		where n.partnum = p.partnum
		order by 1,2,3,4`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var partitions []partitionSnapshot
	tables := make(map[tableIdentity]tableAggregate)
	for rows.Next() {
		var partition partitionSnapshot
		if err := rows.Scan(
			&partition.database, &partition.owner, &partition.table,
			&partition.partnum, &partition.allocatedPages, &partition.usedPages,
			&partition.extents, &partition.pageSize,
		); err != nil {
			return nil, err
		}
		partition.database = strings.TrimSpace(partition.database)
		partition.owner = strings.TrimSpace(partition.owner)
		partition.table = strings.TrimSpace(partition.table)
		if _, system := systemDatabases[partition.database]; system {
			continue
		}

		partitions = append(partitions, partition)
		aggregate := tables[partition.tableIdentity]
		aggregate.extents += partition.extents
		aggregate.allocatedBytes += partition.allocatedPages * partition.pageSize
		aggregate.usedBytes += partition.usedPages * partition.pageSize
		if partition.usedPages > aggregate.maximumPartitionPages {
			aggregate.maximumPartitionPages = partition.usedPages
		}
		tables[partition.tableIdentity] = aggregate
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var samples []tableLimitSample
	for identity, aggregate := range tables {
		if aggregate.extents < limits.MinimumExtents && aggregate.maximumPartitionPages < limits.MinimumPartitionPages {
			continue
		}
		labels := []string{instance.Name, identity.database, identity.owner, identity.table}
		samples = append(samples,
			tableLimitSample{metric: "table_extents", labels: labels, value: float64(aggregate.extents)},
			tableLimitSample{metric: "table_allocated_bytes", labels: labels, value: float64(aggregate.allocatedBytes)},
			tableLimitSample{metric: "table_used_bytes", labels: labels, value: float64(aggregate.usedBytes)},
		)
	}

	for _, partition := range partitions {
		if partition.usedPages < limits.MinimumPartitionPages {
			continue
		}
		labels := []string{
			instance.Name, partition.database, partition.owner, partition.table,
			strconv.FormatInt(partition.partnum, 10),
		}
		samples = append(samples,
			tableLimitSample{metric: "partition_allocated_pages", labels: labels, value: float64(partition.allocatedPages)},
			tableLimitSample{metric: "partition_used_pages", labels: labels, value: float64(partition.usedPages)},
			tableLimitSample{metric: "partition_page_size_bytes", labels: labels, value: float64(partition.pageSize)},
			tableLimitSample{metric: "partition_classic_page_limit", labels: labels, value: float64(limits.ClassicPartitionPageLimit)},
			tableLimitSample{metric: "partition_classic_page_limit_ratio", labels: labels, value: ratio(float64(partition.usedPages), float64(limits.ClassicPartitionPageLimit))},
		)
	}

	return samples, nil
}
