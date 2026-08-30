package collector

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anmartsan/prometheus-informix-exporter/internal/config"
	"github.com/anmartsan/prometheus-informix-exporter/internal/informix"
	"github.com/prometheus/client_golang/prometheus"
)

type unavailableDatabase struct {
	pings   atomic.Int32
	queries atomic.Int32
}

func (d *unavailableDatabase) PingContext(context.Context) error {
	d.pings.Add(1)
	return errors.New("database unavailable")
}

func (d *unavailableDatabase) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	d.queries.Add(1)
	return nil, errors.New("database unavailable")
}

func (d *unavailableDatabase) Close() error { return nil }

type successfulDatabase struct {
	pings atomic.Int32
}

func (d *successfulDatabase) PingContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.pings.Add(1)
	return nil
}

func (d *successfulDatabase) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errors.New("unexpected query")
}

func (d *successfulDatabase) Close() error { return nil }

type blockingDatabase struct{}

func (*blockingDatabase) PingContext(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (*blockingDatabase) QueryContext(ctx context.Context, _ string, _ ...any) (*sql.Rows, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (*blockingDatabase) Close() error { return nil }

type failingCollector struct{}

func (failingCollector) Scrape(context.Context) error     { return errors.New("collector failed") }
func (failingCollector) Collect(chan<- prometheus.Metric) {}
func (failingCollector) Describe(chan<- *prometheus.Desc) {}

func TestProfileSamplesAreDeduplicated(t *testing.T) {
	t.Parallel()

	samples := []profileSample{
		{metric: "network_connections_total", labels: []string{"test"}, value: 1},
		{metric: "network_connections_total", labels: []string{"test"}, value: 2},
		{metric: "network_connections_total", labels: []string{"other"}, value: 3},
	}
	if got := deduplicateProfileSamples(samples); len(got) != 2 {
		t.Fatalf("deduplicateProfileSamples() returned %d samples, want 2", len(got))
	}
}

func TestParseBufferMetricSupportsAnyPageSize(t *testing.T) {
	t.Parallel()

	pageSize, operation, ok := parseBufferMetric("pagreads_32K")
	if !ok || pageSize != "32K" || operation != "disk_read" {
		t.Fatalf("parseBufferMetric() = %q, %q, %v", pageSize, operation, ok)
	}
}

func TestExporterStartsNotReady(t *testing.T) {
	t.Parallel()

	exporter := NewExporter(nil, config.Config{}, time.Second)
	if exporter.Ready() {
		t.Fatal("new exporter is ready before its first successful scrape")
	}
}

func TestExporterPingsEachInstanceOnce(t *testing.T) {
	t.Parallel()

	db := &unavailableDatabase{}
	instances := []informix.Instance{{Name: "test", Database: db}}
	exporter := NewExporter(instances, config.Config{}, time.Second)
	registry := prometheus.NewRegistry()
	registry.MustRegister(exporter)
	if _, err := registry.Gather(); err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	if got := db.pings.Load(); got != 1 {
		t.Fatalf("exporter made %d database pings, want 1", got)
	}
}

func TestExporterCheckReportsCollectorFailures(t *testing.T) {
	t.Parallel()

	db := &unavailableDatabase{}
	instances := []informix.Instance{{Name: "test", Database: db}}
	exporter := NewExporter(instances, config.Config{}, time.Second)
	err := exporter.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "database unavailable") {
		t.Fatalf("Check() error = %v, want database unavailable", err)
	}
	if got := db.pings.Load(); got != 1 {
		t.Fatalf("Check() made %d database pings, want 1", got)
	}
}

func TestExporterOnlyBuildsEnabledCollectors(t *testing.T) {
	t.Parallel()

	instances := []informix.Instance{{Name: "test", InformixServer: "test_dsn"}}
	exporter := NewExporter(instances, config.Config{Collectors: []string{"activity", "dbspaces"}}, time.Second)
	if len(exporter.collectors) != 2 {
		t.Fatalf("NewExporter() built %d collectors, want 2", len(exporter.collectors))
	}
	if exporter.collectors[0].name != "activity" || exporter.collectors[1].name != "dbspaces" {
		t.Fatalf("NewExporter() collectors = %#v", exporter.collectors)
	}
}

func TestExporterPublishesCollectorTelemetry(t *testing.T) {
	t.Parallel()

	db := &successfulDatabase{}
	instances := []informix.Instance{{Name: "test", InformixServer: "test_dsn", Database: db}}
	exporter := NewExporter(instances, config.Config{Collectors: []string{"activity"}}, time.Second)
	exporter.collectors = []namedCollector{{name: "test", collector: failingCollector{}}}
	exporter.instanceCollectors[0].collectors = exporter.collectors
	registry := prometheus.NewRegistry()
	registry.MustRegister(exporter)
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}

	metrics := make(map[string]float64)
	for _, family := range families {
		if len(family.Metric) == 1 && family.Metric[0].Gauge != nil {
			metrics[family.GetName()] = family.Metric[0].Gauge.GetValue()
		}
	}
	if got, exists := metrics["informix_exporter_collector_success"]; !exists || got != 0 {
		t.Fatalf("collector success metric = %v, exists = %v, want 0", got, exists)
	}
	if _, exists := metrics["informix_exporter_collector_duration_seconds"]; !exists {
		t.Fatal("collector duration metric is missing")
	}
}

func TestExporterUsesCollectorsConfiguredPerInstance(t *testing.T) {
	t.Parallel()

	instances := []informix.Instance{
		{Name: "primary", InformixServer: "primary_dsn"},
		{Name: "reporting", InformixServer: "reporting_dsn"},
	}
	cfg := config.Config{
		Servers: []config.Server{
			{Name: "primary", InformixServer: "primary_dsn"},
			{Name: "reporting", InformixServer: "reporting_dsn", Collectors: []string{"activity", "dbspaces"}},
		},
		Collectors: []string{"profile"},
	}
	exporter := NewExporter(instances, cfg, time.Second)
	if got := len(exporter.instanceCollectors[0].collectors); got != 1 {
		t.Fatalf("primary collectors = %d, want 1", got)
	}
	if got := len(exporter.instanceCollectors[1].collectors); got != 2 {
		t.Fatalf("reporting collectors = %d, want 2", got)
	}
	if exporter.instanceCollectors[1].collectors[0].name != "activity" ||
		exporter.instanceCollectors[1].collectors[1].name != "dbspaces" {
		t.Fatalf("unexpected reporting collectors: %#v", exporter.instanceCollectors[1].collectors)
	}
}

func TestExporterSplitsFastAndSlowCollectors(t *testing.T) {
	t.Parallel()

	instances := []informix.Instance{{Name: "test", InformixServer: "test_dsn"}}
	cfg := config.Config{Collectors: []string{"profile", "activity", "dbspaces", "tables"}}
	fast := NewFastExporter(instances, cfg, time.Second)
	slow := NewSlowExporter(instances, cfg, time.Second)

	fastNames := make([]string, 0, len(fast.collectors))
	for _, configured := range fast.collectors {
		fastNames = append(fastNames, configured.name)
	}
	slowNames := make([]string, 0, len(slow.collectors))
	for _, configured := range slow.collectors {
		slowNames = append(slowNames, configured.name)
	}
	if strings.Join(fastNames, ",") != "profile,activity,chunkio" {
		t.Fatalf("fast collectors = %v", fastNames)
	}
	if strings.Join(slowNames, ",") != "dbspaces,tables" {
		t.Fatalf("slow collectors = %v", slowNames)
	}
}

func TestCachedSlowExporterReusesSnapshotBeforeRefreshInterval(t *testing.T) {
	exporter := NewCachedSlowExporter(nil, config.Config{}, time.Second, time.Hour)
	registry := prometheus.NewRegistry()
	registry.MustRegister(exporter)

	if _, err := registry.Gather(); err != nil {
		t.Fatalf("first Gather() error = %v", err)
	}
	firstRefresh := exporter.lastRefresh
	if firstRefresh.IsZero() {
		t.Fatal("first Gather() did not record a refresh")
	}
	if _, err := registry.Gather(); err != nil {
		t.Fatalf("second Gather() error = %v", err)
	}
	if !exporter.lastRefresh.Equal(firstRefresh) {
		t.Fatalf("second Gather() refreshed before interval: got %v, want %v", exporter.lastRefresh, firstRefresh)
	}
}

func TestExporterIsolatesBlockedInstance(t *testing.T) {
	t.Parallel()

	healthy := &successfulDatabase{}
	instances := []informix.Instance{
		{Name: "blocked", InformixServer: "blocked_dsn", Database: &blockingDatabase{}},
		{Name: "healthy", InformixServer: "healthy_dsn", Database: healthy},
	}
	// No custom queries means these pipelines only exercise connectivity.
	exporter := NewExporter(instances, config.Config{Collectors: []string{"custom"}}, 50*time.Millisecond)
	registry := prometheus.NewRegistry()
	registry.MustRegister(exporter)
	if _, err := registry.Gather(); err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	if got := healthy.pings.Load(); got != 1 {
		t.Fatalf("healthy instance completed %d pings, want 1", got)
	}
	if !exporter.Ready() {
		t.Fatal("exporter is not ready although one instance completed successfully")
	}
}

func TestProfileScrapeKeepsUnavailableInstance(t *testing.T) {
	t.Parallel()

	db := &unavailableDatabase{}
	collector := NewProfile([]informix.Instance{{Name: "test", Database: db}})
	err := collector.Scrape(context.Background())
	if err == nil || !strings.Contains(err.Error(), "database unavailable") {
		t.Fatalf("Scrape() error = %v, want database unavailable", err)
	}
	if len(collector.instances) != 1 {
		t.Fatalf("Scrape() removed the unavailable instance")
	}
	if got := db.pings.Load(); got != 0 {
		t.Fatalf("collector made %d database pings, want 0", got)
	}
	if got := db.queries.Load(); got != 1 {
		t.Fatalf("collector made %d queries, want 1", got)
	}
}

func TestDbspaceCollectDoesNotScrapeAgain(t *testing.T) {
	t.Parallel()

	db := &unavailableDatabase{}
	collector := NewDbspaces([]informix.Instance{{Name: "test", Database: db}})
	metrics := make(chan prometheus.Metric, 1)
	collector.Collect(metrics)
	if got := db.pings.Load(); got != 0 {
		t.Fatalf("Collect() triggered %d database pings, want 0", got)
	}
}

func TestChunkCapacityUsesInformixPageUnits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		chunk               chunkCapacitySnapshot
		wantTotal, wantFree int64
	}{
		{name: "wide dbspace", chunk: chunkCapacitySnapshot{pageSize: 16_384, totalPages: 100, freePages: 25}, wantTotal: 204_800, wantFree: 51_200},
		{name: "blobspace", chunk: chunkCapacitySnapshot{pageSize: 8_192, totalPages: 100, freePages: 25, isBlob: 1}, wantTotal: 204_800, wantFree: 204_800},
		{name: "sbspace", chunk: chunkCapacitySnapshot{pageSize: 4_096, totalPages: 100, freePages: 25, isSb: 1}, wantTotal: 409_600, wantFree: 102_400},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			total, free := chunkCapacityBytes(test.chunk, 2_048)
			if total != test.wantTotal || free != test.wantFree {
				t.Fatalf("chunkCapacityBytes() = (%d, %d), want (%d, %d)", total, free, test.wantTotal, test.wantFree)
			}
		})
	}
}
