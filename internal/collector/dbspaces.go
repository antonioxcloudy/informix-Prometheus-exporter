package collector

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/anmartsan/prometheus-informix-exporter/internal/informix"
	"github.com/prometheus/client_golang/prometheus"
)

type storageMetricDefinition struct {
	help      string
	valueType prometheus.ValueType
	labels    []string
}

type storageSample struct {
	metric string
	labels []string
	value  float64
}

var storageMetricDefinitions = map[string]storageMetricDefinition{
	"dbspace_size_bytes": {
		help: "Total usable size of the dbspace in bytes.", valueType: prometheus.GaugeValue,
		labels: []string{"informixserver", "dbspace"},
	},
	"dbspace_used_bytes": {
		help: "Used space in the dbspace in bytes.", valueType: prometheus.GaugeValue,
		labels: []string{"informixserver", "dbspace"},
	},
	"dbspace_free_bytes": {
		help: "Free space in the dbspace in bytes.", valueType: prometheus.GaugeValue,
		labels: []string{"informixserver", "dbspace"},
	},
	"dbspace_used_ratio": {
		help: "Fraction of usable dbspace capacity currently used.", valueType: prometheus.GaugeValue,
		labels: []string{"informixserver", "dbspace"},
	},
	"dbspace_page_size_bytes": {
		help: "Page size of the dbspace in bytes.", valueType: prometheus.GaugeValue,
		labels: []string{"informixserver", "dbspace"},
	},
	"dbspace_chunks": {
		help: "Number of chunks assigned to the dbspace.", valueType: prometheus.GaugeValue,
		labels: []string{"informixserver", "dbspace"},
	},
	"chunk_size_bytes": {
		help: "Total usable size of the chunk in bytes.", valueType: prometheus.GaugeValue,
		labels: []string{"informixserver", "dbspace", "chunk", "chunk_number"},
	},
	"chunk_used_bytes": {
		help: "Used space in the chunk in bytes.", valueType: prometheus.GaugeValue,
		labels: []string{"informixserver", "dbspace", "chunk", "chunk_number"},
	},
	"chunk_free_bytes": {
		help: "Free space in the chunk in bytes.", valueType: prometheus.GaugeValue,
		labels: []string{"informixserver", "dbspace", "chunk", "chunk_number"},
	},
	"chunk_used_ratio": {
		help: "Fraction of usable chunk capacity currently used.", valueType: prometheus.GaugeValue,
		labels: []string{"informixserver", "dbspace", "chunk", "chunk_number"},
	},
	"chunk_online": {
		help: "Whether the chunk is online and consistent (1) or unavailable (0).", valueType: prometheus.GaugeValue,
		labels: []string{"informixserver", "dbspace", "chunk", "chunk_number"},
	},
	"chunk_recovering": {
		help: "Whether the chunk is currently recovering.", valueType: prometheus.GaugeValue,
		labels: []string{"informixserver", "dbspace", "chunk", "chunk_number"},
	},
	"chunk_extendable": {
		help: "Whether the chunk is configured as extendable.", valueType: prometheus.GaugeValue,
		labels: []string{"informixserver", "dbspace", "chunk", "chunk_number"},
	},
	"chunk_io_operations_total": {
		help: "Cumulative physical I/O operations for the chunk.", valueType: prometheus.CounterValue,
		labels: []string{"informixserver", "dbspace", "chunk", "chunk_number", "operation"},
	},
	"chunk_io_pages_total": {
		help: "Cumulative pages transferred for the chunk.", valueType: prometheus.CounterValue,
		labels: []string{"informixserver", "dbspace", "chunk", "chunk_number", "operation"},
	},
}

type DbspaceMetrics struct {
	mutex     sync.RWMutex
	instances []informix.Instance
	descs     map[string]*prometheus.Desc
	samples   []storageSample
	query     func(context.Context, informix.Instance) ([]storageSample, error)
}

func NewDbspaces(instances []informix.Instance) *DbspaceMetrics {
	return newDbspaceMetrics(instances, func(string) bool { return true }, queryStorage)
}

func NewDbspaceCapacity(instances []informix.Instance) *DbspaceMetrics {
	return newDbspaceMetrics(instances, func(name string) bool {
		return name != "chunk_io_operations_total" && name != "chunk_io_pages_total"
	}, queryStorageCapacity)
}

func NewChunkIO(instances []informix.Instance) *DbspaceMetrics {
	return newDbspaceMetrics(instances, func(name string) bool {
		return name == "chunk_io_operations_total" || name == "chunk_io_pages_total"
	}, queryChunkIO)
}

func newDbspaceMetrics(
	instances []informix.Instance,
	include func(string) bool,
	query func(context.Context, informix.Instance) ([]storageSample, error),
) *DbspaceMetrics {
	descs := make(map[string]*prometheus.Desc)
	for name, definition := range storageMetricDefinitions {
		if !include(name) {
			continue
		}
		descs[name] = prometheus.NewDesc(
			prometheus.BuildFQName("informix", "", name),
			definition.help,
			definition.labels,
			nil,
		)
	}
	return &DbspaceMetrics{instances: instances, descs: descs, query: query}
}

func (d *DbspaceMetrics) Scrape(ctx context.Context) error {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	var (
		scrapeErr error
		snapshot  []storageSample
	)
	for _, instance := range d.instances {
		samples, err := d.query(ctx, instance)
		if err != nil {
			scrapeErr = errors.Join(scrapeErr, fmt.Errorf("storage for %s: %w", instance.Name, err))
			continue
		}
		snapshot = append(snapshot, samples...)
	}

	d.samples = snapshot
	return scrapeErr
}

func (d *DbspaceMetrics) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range d.descs {
		ch <- desc
	}
}

func (d *DbspaceMetrics) Collect(ch chan<- prometheus.Metric) {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	for _, sample := range d.samples {
		definition := storageMetricDefinitions[sample.metric]
		ch <- prometheus.MustNewConstMetric(
			d.descs[sample.metric], definition.valueType, sample.value, sample.labels...,
		)
	}
}

func queryStorage(ctx context.Context, instance informix.Instance) ([]storageSample, error) {
	capacity, err := queryStorageCapacity(ctx, instance)
	if err != nil {
		return nil, err
	}
	io, err := queryChunkIO(ctx, instance)
	if err != nil {
		return nil, err
	}
	return append(capacity, io...), nil
}

func queryStorageCapacity(ctx context.Context, instance informix.Instance) ([]storageSample, error) {
	defaultPageSize, err := getDefaultPageSize(ctx, instance)
	if err != nil {
		return nil, fmt.Errorf("query default page size: %w", err)
	}

	chunks, err := getChunkCapacity(ctx, instance)
	if err != nil {
		return nil, fmt.Errorf("query chunk capacity: %w", err)
	}

	spaces := aggregateDbspaces(chunks, defaultPageSize)
	var samples []storageSample
	for _, space := range spaces {
		labels := []string{instance.Name, space.name}
		totalBytes := float64(space.totalBytes)
		freeBytes := float64(space.freeBytes)
		usedBytes := totalBytes - freeBytes
		samples = append(samples,
			storageSample{metric: "dbspace_size_bytes", labels: labels, value: totalBytes},
			storageSample{metric: "dbspace_used_bytes", labels: labels, value: usedBytes},
			storageSample{metric: "dbspace_free_bytes", labels: labels, value: freeBytes},
			storageSample{metric: "dbspace_used_ratio", labels: labels, value: ratio(usedBytes, totalBytes)},
			storageSample{metric: "dbspace_page_size_bytes", labels: labels, value: float64(space.pageSize)},
			storageSample{metric: "dbspace_chunks", labels: labels, value: float64(space.chunkCount)},
		)
	}

	for _, chunk := range chunks {
		labels := []string{instance.Name, chunk.dbspace, chunk.path, strconv.FormatInt(chunk.number, 10)}
		total, free := chunkCapacityBytes(chunk, defaultPageSize)
		totalBytes := float64(total)
		freeBytes := float64(free)
		usedBytes := totalBytes - freeBytes
		online := 1.0
		if chunk.offline != 0 || chunk.inconsistent != 0 {
			online = 0
		}
		samples = append(samples,
			storageSample{metric: "chunk_size_bytes", labels: labels, value: totalBytes},
			storageSample{metric: "chunk_used_bytes", labels: labels, value: usedBytes},
			storageSample{metric: "chunk_free_bytes", labels: labels, value: freeBytes},
			storageSample{metric: "chunk_used_ratio", labels: labels, value: ratio(usedBytes, totalBytes)},
			storageSample{metric: "chunk_online", labels: labels, value: online},
			storageSample{metric: "chunk_recovering", labels: labels, value: boolFloat(chunk.recovering != 0)},
			storageSample{metric: "chunk_extendable", labels: labels, value: boolFloat(chunk.extendable != 0)},
		)
	}

	return samples, nil
}

func queryChunkIO(ctx context.Context, instance informix.Instance) ([]storageSample, error) {
	ioStats, err := getChunkIO(ctx, instance)
	if err != nil {
		return nil, fmt.Errorf("query chunk I/O: %w", err)
	}
	var samples []storageSample
	for _, io := range ioStats {
		labels := []string{instance.Name, io.dbspace, io.path, strconv.FormatInt(io.number, 10)}
		samples = append(samples,
			storageSample{metric: "chunk_io_operations_total", labels: appendCopy(labels, "read"), value: float64(io.reads)},
			storageSample{metric: "chunk_io_operations_total", labels: appendCopy(labels, "write"), value: float64(io.writes)},
			storageSample{metric: "chunk_io_pages_total", labels: appendCopy(labels, "read"), value: float64(io.pagesRead)},
			storageSample{metric: "chunk_io_pages_total", labels: appendCopy(labels, "write"), value: float64(io.pagesWritten)},
		)
	}

	return samples, nil
}

type dbspaceSnapshot struct {
	name                            string
	pageSize, totalBytes, freeBytes int64
	chunkCount                      int64
}

func getDefaultPageSize(ctx context.Context, instance informix.Instance) (int64, error) {
	rows, err := instance.Database.QueryContext(ctx, `select pagesize from sysdbspaces where dbsnum = 1`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var pageSize int64
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, err
		}
		return 0, errors.New("root dbspace not found")
	}
	if err := rows.Scan(&pageSize); err != nil {
		return 0, err
	}
	return pageSize, nil
}

type chunkCapacitySnapshot struct {
	dbspace, path                                 string
	number, pageSize, totalPages, freePages       int64
	isBlob, isSb                                  int64
	offline, recovering, inconsistent, extendable int64
}

func getChunkCapacity(ctx context.Context, instance informix.Instance) ([]chunkCapacitySnapshot, error) {
	rows, err := instance.Database.QueryContext(ctx, `select trim(d.name), trim(c.fname), c.chknum, c.pagesize,
		case when c.is_sbchunk = 1 then c.udsize else c.chksize end,
		case when c.is_sbchunk = 1 then c.udfree else c.nfree end,
		c.is_blobchunk, c.is_sbchunk,
		c.is_offline, c.is_recovering, c.is_inconsistent, c.is_extendable
		from sysdbspaces d, syschunks c
		where d.dbsnum = c.dbsnum order by 1,3`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []chunkCapacitySnapshot
	for rows.Next() {
		var chunk chunkCapacitySnapshot
		if err := rows.Scan(
			&chunk.dbspace, &chunk.path, &chunk.number, &chunk.pageSize,
			&chunk.totalPages, &chunk.freePages, &chunk.isBlob, &chunk.isSb,
			&chunk.offline, &chunk.recovering,
			&chunk.inconsistent, &chunk.extendable,
		); err != nil {
			return nil, err
		}
		chunk.dbspace = strings.TrimSpace(chunk.dbspace)
		chunk.path = strings.TrimSpace(chunk.path)
		result = append(result, chunk)
	}
	return result, rows.Err()
}

// syschunks.chksize is measured in default server pages, even for a dbspace
// with a larger logical page size. Blobspace free pages use the blob page size,
// while sbspace udsize/udfree use the sbspace page size.
func chunkCapacityBytes(chunk chunkCapacitySnapshot, defaultPageSize int64) (total, free int64) {
	if chunk.isSb != 0 {
		return chunk.totalPages * chunk.pageSize, chunk.freePages * chunk.pageSize
	}
	total = chunk.totalPages * defaultPageSize
	if chunk.isBlob != 0 {
		return total, chunk.freePages * chunk.pageSize
	}
	return total, chunk.freePages * defaultPageSize
}

func aggregateDbspaces(chunks []chunkCapacitySnapshot, defaultPageSize int64) []dbspaceSnapshot {
	byName := make(map[string]*dbspaceSnapshot)
	var order []string
	for _, chunk := range chunks {
		space, ok := byName[chunk.dbspace]
		if !ok {
			space = &dbspaceSnapshot{name: chunk.dbspace, pageSize: chunk.pageSize}
			byName[chunk.dbspace] = space
			order = append(order, chunk.dbspace)
		}
		total, free := chunkCapacityBytes(chunk, defaultPageSize)
		space.totalBytes += total
		space.freeBytes += free
		space.chunkCount++
	}

	result := make([]dbspaceSnapshot, 0, len(order))
	for _, name := range order {
		result = append(result, *byName[name])
	}
	return result
}

type chunkIOSnapshot struct {
	dbspace, path           string
	number, reads, writes   int64
	pagesRead, pagesWritten int64
}

func getChunkIO(ctx context.Context, instance informix.Instance) ([]chunkIOSnapshot, error) {
	rows, err := instance.Database.QueryContext(ctx, `select trim(d.name), trim(c.fname), c.chknum,
		c.reads, c.writes, c.pagesread, c.pageswritten
		from sysdbspaces d, syschktab c
		where d.dbsnum = c.dbsnum order by 1,3`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []chunkIOSnapshot
	for rows.Next() {
		var io chunkIOSnapshot
		if err := rows.Scan(&io.dbspace, &io.path, &io.number, &io.reads, &io.writes, &io.pagesRead, &io.pagesWritten); err != nil {
			return nil, err
		}
		io.dbspace = strings.TrimSpace(io.dbspace)
		io.path = strings.TrimSpace(io.path)
		result = append(result, io)
	}
	return result, rows.Err()
}

func ratio(value, total float64) float64 {
	if total <= 0 {
		return 0
	}
	return value / total
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func appendCopy(labels []string, value string) []string {
	result := make([]string, len(labels), len(labels)+1)
	copy(result, labels)
	return append(result, value)
}
