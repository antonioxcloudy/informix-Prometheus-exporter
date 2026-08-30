package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v2"
)

const (
	defaultMinimumExtents        = 100
	defaultMinimumPartitionPages = 8_000_000
	defaultClassicPageLimit      = 16_777_215
)

var defaultCollectors = []string{"profile", "activity", "dbspaces", "tables", "custom"}
var defaultSlowCollectors = []string{"dbspaces", "tables"}

type Server struct {
	Name           string   `yaml:"name"`
	InformixServer string   `yaml:"informixserver"`
	Collectors     []string `yaml:"collectors,omitempty"`
}

type CustomQuery struct {
	Name      string `yaml:"name"`
	Help      string `yaml:"help"`
	Query     string `yaml:"query"`
	ValueType string `yaml:"value_type"`
}

type TableLimitConfig struct {
	MinimumExtents            int64 `yaml:"minimum_extents"`
	MinimumPartitionPages     int64 `yaml:"minimum_partition_pages"`
	ClassicPartitionPageLimit int64 `yaml:"classic_partition_page_limit"`
}

type Config struct {
	Servers        []Server         `yaml:"servers"`
	Collectors     []string         `yaml:"collectors"`
	SlowCollectors []string         `yaml:"slow_collectors"`
	Custom         []CustomQuery    `yaml:"custom"`
	TableLimits    TableLimitConfig `yaml:"table_limits"`
}

var metricNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func Load(filename string) (*Config, error) {
	contents, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.UnmarshalStrict(contents, &cfg); err != nil {
		return nil, fmt.Errorf("parse YAML: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Collectors == nil {
		c.Collectors = append([]string(nil), defaultCollectors...)
	}
	if c.SlowCollectors == nil {
		c.SlowCollectors = append([]string(nil), defaultSlowCollectors...)
	}
	for i := range c.Collectors {
		c.Collectors[i] = strings.ToLower(strings.TrimSpace(c.Collectors[i]))
	}
	for i := range c.SlowCollectors {
		c.SlowCollectors[i] = strings.ToLower(strings.TrimSpace(c.SlowCollectors[i]))
	}
	for i := range c.Servers {
		for j := range c.Servers[i].Collectors {
			c.Servers[i].Collectors[j] = strings.ToLower(strings.TrimSpace(c.Servers[i].Collectors[j]))
		}
	}
	if c.TableLimits.MinimumExtents == 0 {
		c.TableLimits.MinimumExtents = defaultMinimumExtents
	}
	if c.TableLimits.MinimumPartitionPages == 0 {
		c.TableLimits.MinimumPartitionPages = defaultMinimumPartitionPages
	}
	if c.TableLimits.ClassicPartitionPageLimit == 0 {
		c.TableLimits.ClassicPartitionPageLimit = defaultClassicPageLimit
	}
	for i := range c.Custom {
		query := &c.Custom[i]
		if strings.TrimSpace(query.ValueType) == "" {
			query.ValueType = "gauge"
		} else {
			query.ValueType = strings.ToLower(strings.TrimSpace(query.ValueType))
		}
		if strings.TrimSpace(query.Help) == "" && strings.TrimSpace(query.Name) != "" {
			query.Help = "Custom Informix metric " + query.Name + "."
		}
	}
}

func (c Config) Validate() error {
	if len(c.Servers) == 0 {
		return errors.New("configuration must contain at least one server")
	}
	availableCollectors := make(map[string]struct{}, len(defaultCollectors))
	for _, name := range defaultCollectors {
		availableCollectors[name] = struct{}{}
	}
	collectors := c.Collectors
	if collectors == nil {
		collectors = defaultCollectors
	}
	if err := validateCollectors("collectors", collectors, availableCollectors); err != nil {
		return err
	}
	if len(c.SlowCollectors) > 0 {
		if err := validateCollectors("slow_collectors", c.SlowCollectors, availableCollectors); err != nil {
			return err
		}
	}

	names := make(map[string]struct{}, len(c.Servers))
	dsns := make(map[string]struct{}, len(c.Servers))
	for i, server := range c.Servers {
		name := strings.TrimSpace(server.Name)
		dsn := strings.TrimSpace(server.InformixServer)
		if name == "" {
			return fmt.Errorf("servers[%d].name must not be empty", i)
		}
		if dsn == "" {
			return fmt.Errorf("servers[%d].informixserver must not be empty", i)
		}
		if _, exists := names[name]; exists {
			return fmt.Errorf("duplicate server name %q", name)
		}
		if _, exists := dsns[dsn]; exists {
			return fmt.Errorf("duplicate Informix DSN %q", dsn)
		}
		names[name] = struct{}{}
		dsns[dsn] = struct{}{}
		if server.Collectors != nil {
			if err := validateCollectors(fmt.Sprintf("servers[%d].collectors", i), server.Collectors, availableCollectors); err != nil {
				return err
			}
		}
	}

	if c.TableLimits.MinimumExtents < -1 {
		return errors.New("table_limits.minimum_extents must be -1 or greater")
	}
	if c.TableLimits.MinimumPartitionPages < -1 {
		return errors.New("table_limits.minimum_partition_pages must be -1 or greater")
	}
	if c.TableLimits.ClassicPartitionPageLimit <= 0 {
		return errors.New("table_limits.classic_partition_page_limit must be greater than zero")
	}

	customNames := make(map[string]struct{}, len(c.Custom))
	for i, custom := range c.Custom {
		name := strings.TrimSpace(custom.Name)
		if !metricNamePattern.MatchString(name) {
			return fmt.Errorf("custom[%d].name %q is not a valid Prometheus metric suffix", i, custom.Name)
		}
		if _, exists := customNames[name]; exists {
			return fmt.Errorf("duplicate custom metric name %q", name)
		}
		customNames[name] = struct{}{}

		query := strings.TrimSpace(custom.Query)
		if query == "" {
			return fmt.Errorf("custom[%d].query must not be empty", i)
		}
		queryWithoutTrailingSemicolon := strings.TrimSuffix(query, ";")
		queryFields := strings.Fields(queryWithoutTrailingSemicolon)
		if len(queryFields) == 0 || strings.ToLower(queryFields[0]) != "select" {
			return fmt.Errorf("custom[%d].query must be a SELECT statement", i)
		}
		if strings.Contains(queryWithoutTrailingSemicolon, ";") {
			return fmt.Errorf("custom[%d].query must contain exactly one statement", i)
		}
		if custom.ValueType != "gauge" && custom.ValueType != "counter" {
			return fmt.Errorf("custom[%d].value_type must be gauge or counter", i)
		}
		if custom.ValueType == "counter" && !strings.HasSuffix(name, "_total") {
			return fmt.Errorf("custom[%d].name must end in _total when value_type is counter", i)
		}
	}
	return nil
}

func validateCollectors(path string, collectors []string, available map[string]struct{}) error {
	if len(collectors) == 0 {
		return fmt.Errorf("%s must enable at least one collector", path)
	}
	configured := make(map[string]struct{}, len(collectors))
	for i, configuredName := range collectors {
		name := strings.ToLower(strings.TrimSpace(configuredName))
		if _, exists := available[name]; !exists {
			return fmt.Errorf("%s[%d] %q is not available", path, i, configuredName)
		}
		if _, exists := configured[name]; exists {
			return fmt.Errorf("duplicate collector %q in %s", name, path)
		}
		configured[name] = struct{}{}
	}
	return nil
}

func (c Config) CollectorEnabled(name string) bool {
	return collectorEnabled(c.Collectors, name)
}

func (c Config) CollectorSlow(name string) bool {
	collectors := c.SlowCollectors
	if collectors == nil {
		collectors = defaultSlowCollectors
	}
	return collectorEnabled(collectors, name)
}

func (c Config) CollectorEnabledFor(server Server, name string) bool {
	return collectorEnabled(c.CollectorsFor(server), name)
}

func (c Config) CollectorsFor(server Server) []string {
	collectors := c.Collectors
	if server.Collectors != nil {
		collectors = server.Collectors
	}
	if collectors == nil {
		collectors = defaultCollectors
	}
	return append([]string(nil), collectors...)
}

func collectorEnabled(collectors []string, name string) bool {
	if collectors == nil {
		collectors = defaultCollectors
	}
	for _, configured := range collectors {
		if strings.EqualFold(strings.TrimSpace(configured), name) {
			return true
		}
	}
	return false
}
