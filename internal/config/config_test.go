package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAppliesDefaults(t *testing.T) {
	t.Parallel()

	filename := filepath.Join(t.TempDir(), "config.yaml")
	contents := []byte(`servers:
  - name: test
    informixserver: test_dsn
custom: []
`)
	if err := os.WriteFile(filename, contents, 0o600); err != nil {
		t.Fatalf("write configuration: %v", err)
	}

	cfg, err := Load(filename)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.TableLimits.MinimumExtents != 100 ||
		cfg.TableLimits.MinimumPartitionPages != 8_000_000 ||
		cfg.TableLimits.ClassicPartitionPageLimit != 16_777_215 {
		t.Fatalf("unexpected table limit defaults: %#v", cfg.TableLimits)
	}
	for _, name := range []string{"profile", "activity", "dbspaces", "tables", "custom"} {
		if !cfg.CollectorEnabled(name) {
			t.Fatalf("default collector %q is disabled", name)
		}
	}
	if !cfg.CollectorSlow("dbspaces") || !cfg.CollectorSlow("tables") || cfg.CollectorSlow("profile") {
		t.Fatalf("unexpected slow collector defaults: %#v", cfg.SlowCollectors)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	filename := filepath.Join(t.TempDir(), "config.yaml")
	contents := []byte("servers:\n  - name: test\n    informixserver: test_dsn\n    typo: value\n")
	if err := os.WriteFile(filename, contents, 0o600); err != nil {
		t.Fatalf("write configuration: %v", err)
	}

	_, err := Load(filename)
	if err == nil || !strings.Contains(err.Error(), "typo") {
		t.Fatalf("Load() error = %v, want unknown field error", err)
	}
}

func TestValidateRejectsDuplicateServers(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Servers: []Server{
			{Name: "one", InformixServer: "dsn_one"},
			{Name: "one", InformixServer: "dsn_two"},
		},
		TableLimits: TableLimitConfig{ClassicPartitionPageLimit: 1},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate server name") {
		t.Fatalf("Validate() error = %v, want duplicate server name", err)
	}
}

func TestLoadAppliesCustomMetricDefaults(t *testing.T) {
	t.Parallel()

	filename := filepath.Join(t.TempDir(), "config.yaml")
	contents := []byte(`servers:
  - name: test
    informixserver: test_dsn
custom:
  - name: example_value
    query: select count(*) from syssessions
`)
	if err := os.WriteFile(filename, contents, 0o600); err != nil {
		t.Fatalf("write configuration: %v", err)
	}

	cfg, err := Load(filename)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Custom[0].ValueType != "gauge" || cfg.Custom[0].Help == "" {
		t.Fatalf("unexpected custom defaults: %#v", cfg.Custom[0])
	}
}

func TestValidateRejectsUnsafeCustomMetrics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		query CustomQuery
		want  string
	}{
		{name: "invalid name", query: CustomQuery{Name: "bad-name", Query: "select 1", ValueType: "gauge"}, want: "metric suffix"},
		{name: "non select", query: CustomQuery{Name: "bad_query", Query: "delete from systables", ValueType: "gauge"}, want: "SELECT"},
		{name: "multiple statements", query: CustomQuery{Name: "many", Query: "select 1; select 2", ValueType: "gauge"}, want: "one statement"},
		{name: "invalid type", query: CustomQuery{Name: "bad_type", Query: "select 1", ValueType: "histogram"}, want: "gauge or counter"},
		{name: "counter suffix", query: CustomQuery{Name: "events", Query: "select 1", ValueType: "counter"}, want: "end in _total"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Config{
				Servers:     []Server{{Name: "test", InformixServer: "dsn"}},
				Custom:      []CustomQuery{test.query},
				TableLimits: TableLimitConfig{ClassicPartitionPageLimit: 1},
			}
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadSelectsCollectors(t *testing.T) {
	t.Parallel()

	filename := filepath.Join(t.TempDir(), "config.yaml")
	contents := []byte(`servers:
  - name: test
    informixserver: test_dsn
collectors:
  - activity
  - dbspaces
`)
	if err := os.WriteFile(filename, contents, 0o600); err != nil {
		t.Fatalf("write configuration: %v", err)
	}

	cfg, err := Load(filename)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.CollectorEnabled("activity") || !cfg.CollectorEnabled("dbspaces") {
		t.Fatal("configured collectors are disabled")
	}
	if cfg.CollectorEnabled("tables") || cfg.CollectorEnabled("profile") {
		t.Fatal("unconfigured collectors are enabled")
	}
}

func TestLoadCanDisableSlowCollectors(t *testing.T) {
	t.Parallel()

	filename := filepath.Join(t.TempDir(), "config.yaml")
	contents := []byte(`servers:
  - name: test
    informixserver: test_dsn
slow_collectors: []
`)
	if err := os.WriteFile(filename, contents, 0o600); err != nil {
		t.Fatalf("write configuration: %v", err)
	}
	cfg, err := Load(filename)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.CollectorSlow("dbspaces") || cfg.CollectorSlow("tables") {
		t.Fatal("explicit empty slow_collectors did not disable the slow group")
	}
}

func TestLoadSelectsCollectorsPerServer(t *testing.T) {
	t.Parallel()

	filename := filepath.Join(t.TempDir(), "config.yaml")
	contents := []byte(`servers:
  - name: primary
    informixserver: primary_dsn
  - name: reporting
    informixserver: reporting_dsn
    collectors:
      - activity
      - DBSPACES
collectors:
  - profile
`)
	if err := os.WriteFile(filename, contents, 0o600); err != nil {
		t.Fatalf("write configuration: %v", err)
	}

	cfg, err := Load(filename)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.CollectorEnabledFor(cfg.Servers[0], "profile") || cfg.CollectorEnabledFor(cfg.Servers[0], "activity") {
		t.Fatal("primary server did not inherit global collectors")
	}
	if !cfg.CollectorEnabledFor(cfg.Servers[1], "activity") || !cfg.CollectorEnabledFor(cfg.Servers[1], "dbspaces") {
		t.Fatal("reporting server collectors were not applied")
	}
	if cfg.CollectorEnabledFor(cfg.Servers[1], "profile") {
		t.Fatal("reporting server unexpectedly inherited global collectors")
	}
}

func TestValidateRejectsInvalidCollectors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		collectors []string
		want       string
	}{
		{name: "empty", collectors: []string{}, want: "at least one collector"},
		{name: "unknown", collectors: []string{"activity", "unknown"}, want: "not available"},
		{name: "duplicate", collectors: []string{"activity", "activity"}, want: "duplicate collector"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Config{
				Servers:     []Server{{Name: "test", InformixServer: "dsn"}},
				Collectors:  test.collectors,
				TableLimits: TableLimitConfig{ClassicPartitionPageLimit: 1},
			}
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateRejectsInvalidServerCollectors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		collectors []string
		want       string
	}{
		{name: "empty", collectors: []string{}, want: "at least one collector"},
		{name: "unknown", collectors: []string{"activity", "unknown"}, want: "not available"},
		{name: "duplicate", collectors: []string{"activity", "activity"}, want: "duplicate collector"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Config{
				Servers:     []Server{{Name: "test", InformixServer: "dsn", Collectors: test.collectors}},
				TableLimits: TableLimitConfig{ClassicPartitionPageLimit: 1},
			}
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}
