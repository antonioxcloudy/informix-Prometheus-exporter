package collector

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anmartsan/prometheus-informix-exporter/internal/config"
	"github.com/anmartsan/prometheus-informix-exporter/internal/informix"
	"github.com/anmartsan/prometheus-informix-exporter/internal/version"
	"github.com/prometheus/client_golang/prometheus"
)

type Collector interface {
	Scrape(context.Context) error
	Collect(chan<- prometheus.Metric)
	Describe(chan<- *prometheus.Desc)
}

type namedCollector struct {
	name      string
	collector Collector
}

type instanceCollectors struct {
	instance   informix.Instance
	collectors []namedCollector
}

type instanceScrapeResult struct {
	name    string
	success bool
	err     error
}

type collectorGroup int

const (
	collectorGroupAll collectorGroup = iota
	collectorGroupFast
	collectorGroupSlow
)

type Exporter struct {
	mutex               sync.Mutex
	collectors          []namedCollector
	instanceCollectors  []instanceCollectors
	scrapeTimeout       time.Duration
	refreshInterval     time.Duration
	lastRefresh         time.Time
	up                  *prometheus.GaugeVec
	scrapeSuccess       prometheus.Gauge
	scrapeDuration      prometheus.Gauge
	scrapeErrors        *prometheus.CounterVec
	lastSuccess         prometheus.Gauge
	instanceSuccess     *prometheus.GaugeVec
	instanceDuration    *prometheus.GaugeVec
	instanceLastSuccess *prometheus.GaugeVec
	collectorSuccess    *prometheus.GaugeVec
	collectorDuration   *prometheus.GaugeVec
	buildInfo           *prometheus.Desc
	ready               atomic.Bool
}

func NewExporter(instances []informix.Instance, cfg config.Config, timeout time.Duration) *Exporter {
	return newExporter(instances, cfg, timeout, collectorGroupAll)
}

func NewFastExporter(instances []informix.Instance, cfg config.Config, timeout time.Duration) *Exporter {
	return newExporter(instances, cfg, timeout, collectorGroupFast)
}

func NewSlowExporter(instances []informix.Instance, cfg config.Config, timeout time.Duration) *Exporter {
	return newExporter(instances, cfg, timeout, collectorGroupSlow)
}

func NewCachedSlowExporter(instances []informix.Instance, cfg config.Config, timeout, refreshInterval time.Duration) *Exporter {
	exporter := NewSlowExporter(instances, cfg, timeout)
	exporter.refreshInterval = refreshInterval
	return exporter
}

func newExporter(instances []informix.Instance, cfg config.Config, timeout time.Duration, group collectorGroup) *Exporter {
	serverConfigs := make(map[string]config.Server, len(cfg.Servers))
	for _, server := range cfg.Servers {
		serverConfigs[server.InformixServer] = server
	}

	var (
		allCollectors []namedCollector
		sets          []instanceCollectors
	)
	for _, instance := range instances {
		server := serverConfigs[instance.InformixServer]
		if server.Name == "" {
			server = config.Server{Name: instance.Name, InformixServer: instance.InformixServer}
		}
		collectors := collectorsForInstance(instance, server, cfg, group)
		allCollectors = append(allCollectors, collectors...)
		sets = append(sets, instanceCollectors{instance: instance, collectors: collectors})
	}

	return &Exporter{
		collectors:         allCollectors,
		instanceCollectors: sets,
		scrapeTimeout:      timeout,
		up: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "informix", Name: "up",
			Help: "Whether the Informix instance accepted an ODBC ping during the last scrape.",
		}, []string{"informixserver"}),
		scrapeSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "informix", Subsystem: "exporter", Name: "scrape_success",
			Help: "Whether every configured Informix instance completed the last scrape successfully.",
		}),
		scrapeDuration: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "informix", Subsystem: "exporter", Name: "scrape_duration_seconds",
			Help: "Duration of the last complete exporter scrape in seconds.",
		}),
		scrapeErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "informix", Subsystem: "exporter", Name: "scrape_errors_total",
			Help: "Total number of collector and connectivity errors by Informix instance.",
		}, []string{"informixserver"}),
		lastSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "informix", Subsystem: "exporter", Name: "last_successful_scrape_timestamp_seconds",
			Help: "Unix timestamp of the last scrape in which every instance succeeded.",
		}),
		instanceSuccess: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "informix", Subsystem: "exporter", Name: "instance_scrape_success",
			Help: "Whether the Informix instance and all of its collectors completed the last scrape successfully.",
		}, []string{"informixserver"}),
		instanceDuration: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "informix", Subsystem: "exporter", Name: "instance_scrape_duration_seconds",
			Help: "Duration of the last scrape for the Informix instance.",
		}, []string{"informixserver"}),
		instanceLastSuccess: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "informix", Subsystem: "exporter", Name: "instance_last_successful_scrape_timestamp_seconds",
			Help: "Unix timestamp of the last successful scrape for the Informix instance.",
		}, []string{"informixserver"}),
		collectorSuccess: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "informix", Subsystem: "exporter", Name: "collector_success",
			Help: "Whether the collector completed its last scrape successfully for the Informix instance.",
		}, []string{"informixserver", "collector"}),
		collectorDuration: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "informix", Subsystem: "exporter", Name: "collector_duration_seconds",
			Help: "Duration of the collector for the Informix instance during the last scrape.",
		}, []string{"informixserver", "collector"}),
		buildInfo: prometheus.NewDesc(
			"informix_exporter_build_info",
			"Build information for the Informix exporter.",
			[]string{"version", "commit", "build_date"},
			nil,
		),
	}
}

func collectorsForInstance(instance informix.Instance, server config.Server, cfg config.Config, group collectorGroup) []namedCollector {
	instances := []informix.Instance{instance}
	var collectors []namedCollector
	selected := func(name string) bool {
		if !cfg.CollectorEnabledFor(server, name) {
			return false
		}
		slow := cfg.CollectorSlow(name)
		return group == collectorGroupAll || (group == collectorGroupSlow) == slow
	}
	if selected("profile") {
		collectors = append(collectors, namedCollector{name: "profile", collector: NewProfile(instances)})
	}
	if selected("activity") {
		collectors = append(collectors, namedCollector{name: "activity", collector: NewActivity(instances)})
	}
	if cfg.CollectorEnabledFor(server, "dbspaces") {
		switch {
		case group == collectorGroupAll:
			collectors = append(collectors, namedCollector{name: "dbspaces", collector: NewDbspaces(instances)})
		case cfg.CollectorSlow("dbspaces") && group == collectorGroupFast:
			collectors = append(collectors, namedCollector{name: "chunkio", collector: NewChunkIO(instances)})
		case cfg.CollectorSlow("dbspaces") && group == collectorGroupSlow:
			collectors = append(collectors, namedCollector{name: "dbspaces", collector: NewDbspaceCapacity(instances)})
		case !cfg.CollectorSlow("dbspaces") && group == collectorGroupFast:
			collectors = append(collectors, namedCollector{name: "dbspaces", collector: NewDbspaces(instances)})
		}
	}
	if selected("tables") {
		collectors = append(collectors, namedCollector{name: "tables", collector: NewTables(instances, cfg.TableLimits)})
	}
	if selected("custom") && len(cfg.Custom) > 0 {
		collectors = append(collectors, namedCollector{name: "custom", collector: NewCustom(instances, cfg.Custom)})
	}
	return collectors
}

func (e *Exporter) scrapeAll(ctx context.Context) (bool, error) {
	results := make(chan instanceScrapeResult, len(e.instanceCollectors))
	for _, set := range e.instanceCollectors {
		set := set
		go func() {
			results <- e.scrapeInstance(ctx, set)
		}()
	}

	var (
		anySuccess bool
		scrapeErr  error
	)
	for range e.instanceCollectors {
		result := <-results
		anySuccess = anySuccess || result.success
		if result.err != nil {
			scrapeErr = errors.Join(scrapeErr, fmt.Errorf("instance %s: %w", result.name, result.err))
		}
	}
	return anySuccess, scrapeErr
}

func (e *Exporter) scrapeInstance(ctx context.Context, set instanceCollectors) instanceScrapeResult {
	instance := set.instance
	started := time.Now()
	var scrapeErr error

	if err := instance.Database.PingContext(ctx); err != nil {
		e.up.WithLabelValues(instance.Name).Set(0)
		e.scrapeErrors.WithLabelValues(instance.Name).Inc()
		log.Printf("Error pinging %s: %s", instance.Name, informix.ErrorText(err))
		scrapeErr = errors.Join(scrapeErr, fmt.Errorf("ping: %w", err))
	} else {
		e.up.WithLabelValues(instance.Name).Set(1)
	}

	for _, collector := range set.collectors {
		collectorStarted := time.Now()
		err := collector.collector.Scrape(ctx)
		e.collectorDuration.WithLabelValues(instance.Name, collector.name).Set(time.Since(collectorStarted).Seconds())
		if err != nil {
			e.collectorSuccess.WithLabelValues(instance.Name, collector.name).Set(0)
			e.scrapeErrors.WithLabelValues(instance.Name).Inc()
			log.Printf("Error scraping collector %s for %s: %s", collector.name, instance.Name, informix.ErrorText(err))
			scrapeErr = errors.Join(scrapeErr, fmt.Errorf("collector %s: %w", collector.name, err))
			continue
		}
		e.collectorSuccess.WithLabelValues(instance.Name, collector.name).Set(1)
	}

	success := scrapeErr == nil
	e.instanceDuration.WithLabelValues(instance.Name).Set(time.Since(started).Seconds())
	if success {
		e.instanceSuccess.WithLabelValues(instance.Name).Set(1)
		e.instanceLastSuccess.WithLabelValues(instance.Name).SetToCurrentTime()
	} else {
		e.instanceSuccess.WithLabelValues(instance.Name).Set(0)
	}
	return instanceScrapeResult{name: instance.Name, success: success, err: scrapeErr}
}

func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	described := make(map[string]struct{})
	for _, collector := range e.collectors {
		if _, exists := described[collector.name]; exists {
			continue
		}
		collector.collector.Describe(ch)
		described[collector.name] = struct{}{}
	}
	e.up.Describe(ch)
	e.scrapeSuccess.Describe(ch)
	e.scrapeDuration.Describe(ch)
	e.scrapeErrors.Describe(ch)
	e.lastSuccess.Describe(ch)
	e.instanceSuccess.Describe(ch)
	e.instanceDuration.Describe(ch)
	e.instanceLastSuccess.Describe(ch)
	e.collectorSuccess.Describe(ch)
	e.collectorDuration.Describe(ch)
	ch <- e.buildInfo
}

func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	e.mutex.Lock()
	defer e.mutex.Unlock()

	if e.lastRefresh.IsZero() || e.refreshInterval <= 0 || time.Since(e.lastRefresh) >= e.refreshInterval {
		ctx, cancel := context.WithTimeout(context.Background(), e.scrapeTimeout)
		started := time.Now()
		anySuccess, err := e.scrapeAll(ctx)
		cancel()

		e.scrapeDuration.Set(time.Since(started).Seconds())
		if err == nil {
			e.scrapeSuccess.Set(1)
			e.lastSuccess.SetToCurrentTime()
			e.lastRefresh = time.Now()
		} else {
			e.scrapeSuccess.Set(0)
		}
		e.ready.Store(anySuccess)
	}

	for _, collector := range e.collectors {
		collector.collector.Collect(ch)
	}
	e.up.Collect(ch)
	e.scrapeSuccess.Collect(ch)
	e.scrapeDuration.Collect(ch)
	e.scrapeErrors.Collect(ch)
	e.lastSuccess.Collect(ch)
	e.instanceSuccess.Collect(ch)
	e.instanceDuration.Collect(ch)
	e.instanceLastSuccess.Collect(ch)
	e.collectorSuccess.Collect(ch)
	e.collectorDuration.Collect(ch)
	ch <- prometheus.MustNewConstMetric(
		e.buildInfo, prometheus.GaugeValue, 1, version.Version, version.Commit, version.BuildDate,
	)
}

func (e *Exporter) Ready() bool {
	return e.ready.Load()
}

// Check runs all instance pipelines concurrently using the same timeout as a
// Prometheus scrape and reports every connectivity or collector failure.
func (e *Exporter) Check(ctx context.Context) error {
	e.mutex.Lock()
	defer e.mutex.Unlock()
	_, err := e.scrapeAll(ctx)
	return err
}
