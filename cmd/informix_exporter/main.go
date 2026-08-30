package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/anmartsan/prometheus-informix-exporter/internal/collector"
	"github.com/anmartsan/prometheus-informix-exporter/internal/config"
	"github.com/anmartsan/prometheus-informix-exporter/internal/informix"
	"github.com/anmartsan/prometheus-informix-exporter/internal/version"
	"github.com/prometheus/client_golang/prometheus"
	collectors "github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	configFile := flag.String("configfile", "config.yaml", "Configuration file")
	port := flag.String("port", "8087", "Listen port")
	scrapeTimeout := flag.Duration("scrape-timeout", 10*time.Second, "Maximum duration of a database scrape")
	slowRefreshInterval := flag.Duration("slow-refresh-interval", 2*time.Hour, "Minimum interval between slow collector database queries")
	checkConfig := flag.Bool("check-config", false, "Validate the configuration file and exit")
	check := flag.Bool("check", false, "Validate configuration, ODBC connectivity, and collector queries, then exit")
	showVersion := flag.Bool("version", false, "Print version information and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version.String())
		return
	}

	log.Printf("Loading configuration file %s", *configFile)
	cfg, err := config.Load(*configFile)
	if err != nil {
		log.Fatalf("Error loading configuration: %v", err)
	}
	if *checkConfig {
		log.Printf("Slow collector group: %s", strings.Join(cfg.SlowCollectors, ", "))
		for _, configuredServer := range cfg.Servers {
			log.Printf("Configured %s (DSN %s) with collectors: %s", configuredServer.Name, configuredServer.InformixServer, strings.Join(cfg.CollectorsFor(configuredServer), ", "))
		}
		log.Printf("Configuration is valid: %d Informix server(s), %d custom metric(s)", len(cfg.Servers), len(cfg.Custom))
		return
	}

	instances, err := informix.OpenInstances(cfg.Servers)
	if err != nil {
		log.Fatalf("Error configuring databases: %s", informix.ErrorText(err))
	}
	defer func() {
		if err := informix.CloseInstances(instances); err != nil {
			log.Printf("Error closing databases: %s", informix.ErrorText(err))
		}
	}()

	fullExporter := collector.NewExporter(instances, *cfg, *scrapeTimeout)
	if *check {
		ctx, cancel := context.WithTimeout(context.Background(), *scrapeTimeout)
		defer cancel()
		if err := fullExporter.Check(ctx); err != nil {
			log.Fatalf("Installation check failed: %s", informix.ErrorText(err))
		}
		for _, instance := range instances {
			log.Printf("Installation check passed for %s (DSN %s)", instance.Name, instance.InformixServer)
		}
		log.Printf("Installation check passed: %d Informix server(s), all collector queries succeeded", len(instances))
		return
	}

	fastExporter := collector.NewFastExporter(instances, *cfg, *scrapeTimeout)
	slowExporter := collector.NewCachedSlowExporter(instances, *cfg, *scrapeTimeout, *slowRefreshInterval)
	fullRegistry := prometheus.NewRegistry()
	fullRegistry.MustRegister(fullExporter, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	fastRegistry := prometheus.NewRegistry()
	fastRegistry.MustRegister(fastExporter, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	slowRegistry := prometheus.NewRegistry()
	slowRegistry.MustRegister(slowExporter)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(fullRegistry, promhttp.HandlerOpts{}))
	mux.Handle("/metrics/fast", promhttp.HandlerFor(fastRegistry, promhttp.HandlerOpts{}))
	mux.Handle("/metrics/storage", promhttp.HandlerFor(slowRegistry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !fastExporter.Ready() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	})
	mux.HandleFunc("/readyz/storage", func(w http.ResponseWriter, _ *http.Request) {
		if !slowExporter.Ready() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	})

	server := &http.Server{
		Addr: ":" + *port, Handler: mux, ReadHeaderTimeout: 5 * time.Second,
	}
	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-shutdownCtx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("Error stopping HTTP server: %v", err)
		}
	}()

	log.Printf("Starting Informix exporter on port %s", *port)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("HTTP server stopped: %v", err)
	}
}
