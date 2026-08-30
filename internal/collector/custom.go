package collector

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/anmartsan/prometheus-informix-exporter/internal/config"
	"github.com/anmartsan/prometheus-informix-exporter/internal/informix"
	"github.com/prometheus/client_golang/prometheus"
)

type customSample struct {
	name, instance string
	value          float64
}

type CustomMetrics struct {
	mutex     sync.RWMutex
	instances []informix.Instance
	queries   []config.CustomQuery
	descs     map[string]*prometheus.Desc
	types     map[string]prometheus.ValueType
	samples   []customSample
}

func NewCustom(instances []informix.Instance, queries []config.CustomQuery) *CustomMetrics {
	descs := make(map[string]*prometheus.Desc, len(queries))
	types := make(map[string]prometheus.ValueType, len(queries))
	for _, query := range queries {
		descs[query.Name] = prometheus.NewDesc(
			prometheus.BuildFQName("informix", "custom", query.Name),
			query.Help,
			[]string{"informixserver"},
			nil,
		)
		valueType := prometheus.GaugeValue
		if query.ValueType == "counter" {
			valueType = prometheus.CounterValue
		}
		types[query.Name] = valueType
	}
	return &CustomMetrics{instances: instances, queries: queries, descs: descs, types: types}
}

func (c *CustomMetrics) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range c.descs {
		ch <- desc
	}
}

func (c *CustomMetrics) Collect(ch chan<- prometheus.Metric) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	for _, sample := range c.samples {
		ch <- prometheus.MustNewConstMetric(
			c.descs[sample.name], c.types[sample.name], sample.value, sample.instance,
		)
	}
}

func (c *CustomMetrics) Scrape(ctx context.Context) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	var (
		scrapeErr error
		snapshot  []customSample
	)
	for _, instance := range c.instances {
		for _, query := range c.queries {
			value, err := queryScalar(ctx, instance, query.Query)
			if err != nil {
				scrapeErr = errors.Join(scrapeErr, fmt.Errorf("custom metric %q for %s: %w", query.Name, instance.Name, err))
				continue
			}
			if math.IsNaN(value) || math.IsInf(value, 0) {
				scrapeErr = errors.Join(scrapeErr, fmt.Errorf("custom metric %q for %s returned a non-finite value", query.Name, instance.Name))
				continue
			}
			if query.ValueType == "counter" && value < 0 {
				scrapeErr = errors.Join(scrapeErr, fmt.Errorf("custom counter %q for %s returned a negative value", query.Name, instance.Name))
				continue
			}
			snapshot = append(snapshot, customSample{name: query.Name, instance: instance.Name, value: value})
		}
	}
	c.samples = snapshot
	return scrapeErr
}

func queryScalar(ctx context.Context, instance informix.Instance, query string) (float64, error) {
	rows, err := instance.Database.QueryContext(ctx, query)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return 0, err
	}
	if len(columns) != 1 {
		return 0, fmt.Errorf("query returned %d columns, want exactly one", len(columns))
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, err
		}
		return 0, errors.New("query returned no rows")
	}

	var value float64
	if err := rows.Scan(&value); err != nil {
		return 0, err
	}
	if rows.Next() {
		return 0, errors.New("query returned more than one row")
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return value, nil
}
