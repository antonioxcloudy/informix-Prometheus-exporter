package collector

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anmartsan/prometheus-informix-exporter/internal/informix"
	"github.com/prometheus/client_golang/prometheus"
)

type activityMetricDefinition struct {
	help   string
	labels []string
}

type activitySample struct {
	metric string
	labels []string
	value  float64
}

var activityMetricDefinitions = map[string]activityMetricDefinition{
	"sessions": {
		help: "Current number of user sessions.", labels: []string{"informixserver"},
	},
	"session_waiting": {
		help: "Current sessions waiting by reason.", labels: []string{"informixserver", "reason"},
	},
	"oldest_session_age_seconds": {
		help: "Age of the oldest current user session in seconds.", labels: []string{"informixserver"},
	},
	"open_transactions": {
		help: "Current number of open transactions.", labels: []string{"informixserver"},
	},
	"long_transactions": {
		help: "Current number of transactions marked as long transactions by Informix.", labels: []string{"informixserver"},
	},
	"transaction_locks": {
		help: "Current number of locks held by open transactions.", labels: []string{"informixserver"},
	},
	"locks": {
		help: "Current number of active or waiting locks.", labels: []string{"informixserver", "state"},
	},
	"locks_by_type": {
		help: "Current number of active locks by Informix lock type.", labels: []string{"informixserver", "type"},
	},
	"logical_log_files": {
		help: "Configured number of logical-log files.", labels: []string{"informixserver"},
	},
	"logical_logs_not_backed_up": {
		help: "Current number of used logical logs not backed up.", labels: []string{"informixserver"},
	},
	"logical_log_size_bytes": {
		help: "Configured size of a logical-log file in bytes.", labels: []string{"informixserver", "log_number"},
	},
	"logical_log_used_bytes": {
		help: "Used space in a logical-log file in bytes.", labels: []string{"informixserver", "log_number"},
	},
	"logical_log_used_ratio": {
		help: "Fraction of a logical-log file currently used.", labels: []string{"informixserver", "log_number"},
	},
	"logical_log_current": {
		help: "Whether this is the current logical-log file.", labels: []string{"informixserver", "log_number"},
	},
	"logical_log_backed_up": {
		help: "Whether this logical-log file has been backed up.", labels: []string{"informixserver", "log_number"},
	},
	"logical_log_archived": {
		help: "Whether this logical-log file has been archived.", labels: []string{"informixserver", "log_number"},
	},
	"logical_log_temporary": {
		help: "Whether this is a temporary logical-log file.", labels: []string{"informixserver", "log_number"},
	},
}

type ActivityMetrics struct {
	mutex     sync.RWMutex
	instances []informix.Instance
	descs     map[string]*prometheus.Desc
	samples   []activitySample
}

func NewActivity(instances []informix.Instance) *ActivityMetrics {
	descs := make(map[string]*prometheus.Desc, len(activityMetricDefinitions))
	for name, definition := range activityMetricDefinitions {
		descs[name] = prometheus.NewDesc(
			prometheus.BuildFQName("informix", "", name), definition.help, definition.labels, nil,
		)
	}
	return &ActivityMetrics{instances: instances, descs: descs}
}

func (a *ActivityMetrics) Scrape(ctx context.Context) error {
	a.mutex.Lock()
	defer a.mutex.Unlock()

	var (
		scrapeErr error
		snapshot  []activitySample
	)
	for _, instance := range a.instances {
		samples, err := queryActivity(ctx, instance, time.Now())
		if err != nil {
			scrapeErr = errors.Join(scrapeErr, fmt.Errorf("activity for %s: %w", instance.Name, err))
			continue
		}
		snapshot = append(snapshot, samples...)
	}
	a.samples = snapshot
	return scrapeErr
}

func (a *ActivityMetrics) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range a.descs {
		ch <- desc
	}
}

func (a *ActivityMetrics) Collect(ch chan<- prometheus.Metric) {
	a.mutex.RLock()
	defer a.mutex.RUnlock()

	for _, sample := range a.samples {
		ch <- prometheus.MustNewConstMetric(a.descs[sample.metric], prometheus.GaugeValue, sample.value, sample.labels...)
	}
}

func queryActivity(ctx context.Context, instance informix.Instance, now time.Time) ([]activitySample, error) {
	var samples []activitySample

	sessionRows, err := instance.Database.QueryContext(ctx, `select count(*), sum(is_wlock), sum(is_wlatch),
		sum(is_wbuff), sum(is_wckpt), sum(is_wlogbuf), sum(is_wtrans), min(connected)
		from syssessions`)
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	if sessionRows.Next() {
		var total, lock, latch, buffer, checkpoint, logBuffer, transaction, oldest sql.NullInt64
		if err := sessionRows.Scan(&total, &lock, &latch, &buffer, &checkpoint, &logBuffer, &transaction, &oldest); err != nil {
			sessionRows.Close()
			return nil, fmt.Errorf("scan sessions: %w", err)
		}
		age := int64(0)
		if oldest.Valid && oldest.Int64 > 0 && now.Unix() > oldest.Int64 {
			age = now.Unix() - oldest.Int64
		}
		samples = append(samples,
			activitySample{metric: "sessions", labels: []string{instance.Name}, value: float64(nullInt(total))},
			activitySample{metric: "oldest_session_age_seconds", labels: []string{instance.Name}, value: float64(age)},
			activitySample{metric: "session_waiting", labels: []string{instance.Name, "lock"}, value: float64(nullInt(lock))},
			activitySample{metric: "session_waiting", labels: []string{instance.Name, "latch"}, value: float64(nullInt(latch))},
			activitySample{metric: "session_waiting", labels: []string{instance.Name, "buffer"}, value: float64(nullInt(buffer))},
			activitySample{metric: "session_waiting", labels: []string{instance.Name, "checkpoint"}, value: float64(nullInt(checkpoint))},
			activitySample{metric: "session_waiting", labels: []string{instance.Name, "log_buffer"}, value: float64(nullInt(logBuffer))},
			activitySample{metric: "session_waiting", labels: []string{instance.Name, "transaction"}, value: float64(nullInt(transaction))},
		)
	}
	if err := closeRows(sessionRows); err != nil {
		return nil, fmt.Errorf("iterate sessions: %w", err)
	}

	lockRows, err := instance.Database.QueryContext(ctx, `select trim(type), count(*),
		sum(case when waiter > 0 then 1 else 0 end) from syslocks group by 1 order by 1`)
	if err != nil {
		return nil, fmt.Errorf("query locks: %w", err)
	}
	var activeLocks, waitingLocks int64
	for lockRows.Next() {
		var lockType string
		var active, waiting sql.NullInt64
		if err := lockRows.Scan(&lockType, &active, &waiting); err != nil {
			lockRows.Close()
			return nil, fmt.Errorf("scan locks: %w", err)
		}
		activeLocks += nullInt(active)
		waitingLocks += nullInt(waiting)
		samples = append(samples, activitySample{
			metric: "locks_by_type", labels: []string{instance.Name, strings.TrimSpace(lockType)}, value: float64(nullInt(active)),
		})
	}
	if err := closeRows(lockRows); err != nil {
		return nil, fmt.Errorf("iterate locks: %w", err)
	}
	samples = append(samples,
		activitySample{metric: "locks", labels: []string{instance.Name, "active"}, value: float64(activeLocks)},
		activitySample{metric: "locks", labels: []string{instance.Name, "waiting"}, value: float64(waitingLocks)},
	)

	transactionRows, err := instance.Database.QueryContext(ctx, `select count(*), sum(tx_nlocks),
		sum(case when tx_longtx <> 0 then 1 else 0 end) from systrans`)
	if err != nil {
		return nil, fmt.Errorf("query transactions: %w", err)
	}
	if transactionRows.Next() {
		var transactions, locks, longTransactions sql.NullInt64
		if err := transactionRows.Scan(&transactions, &locks, &longTransactions); err != nil {
			transactionRows.Close()
			return nil, fmt.Errorf("scan transactions: %w", err)
		}
		samples = append(samples,
			activitySample{metric: "open_transactions", labels: []string{instance.Name}, value: float64(nullInt(transactions))},
			activitySample{metric: "transaction_locks", labels: []string{instance.Name}, value: float64(nullInt(locks))},
			activitySample{metric: "long_transactions", labels: []string{instance.Name}, value: float64(nullInt(longTransactions))},
		)
	}
	if err := closeRows(transactionRows); err != nil {
		return nil, fmt.Errorf("iterate transactions: %w", err)
	}

	pageSize, err := getDefaultPageSize(ctx, instance)
	if err != nil {
		return nil, fmt.Errorf("query logical-log page size: %w", err)
	}
	logRows, err := instance.Database.QueryContext(ctx, `select number, size, used, is_used,
		is_current, is_backed_up, is_archived, is_temp from syslogs order by number`)
	if err != nil {
		return nil, fmt.Errorf("query logical logs: %w", err)
	}
	var logFiles, notBackedUp int64
	for logRows.Next() {
		var number, size, used, isUsed, current, backedUp, archived, temporary int64
		if err := logRows.Scan(&number, &size, &used, &isUsed, &current, &backedUp, &archived, &temporary); err != nil {
			logRows.Close()
			return nil, fmt.Errorf("scan logical logs: %w", err)
		}
		logFiles++
		if isUsed != 0 && backedUp == 0 {
			notBackedUp++
		}
		labels := []string{instance.Name, strconv.FormatInt(number, 10)}
		sizeBytes := size * pageSize
		usedBytes := used * pageSize
		samples = append(samples,
			activitySample{metric: "logical_log_size_bytes", labels: labels, value: float64(sizeBytes)},
			activitySample{metric: "logical_log_used_bytes", labels: labels, value: float64(usedBytes)},
			activitySample{metric: "logical_log_used_ratio", labels: labels, value: ratio(float64(usedBytes), float64(sizeBytes))},
			activitySample{metric: "logical_log_current", labels: labels, value: boolFloat(current != 0)},
			activitySample{metric: "logical_log_backed_up", labels: labels, value: boolFloat(backedUp != 0)},
			activitySample{metric: "logical_log_archived", labels: labels, value: boolFloat(archived != 0)},
			activitySample{metric: "logical_log_temporary", labels: labels, value: boolFloat(temporary != 0)},
		)
	}
	if err := closeRows(logRows); err != nil {
		return nil, fmt.Errorf("iterate logical logs: %w", err)
	}
	samples = append(samples,
		activitySample{metric: "logical_log_files", labels: []string{instance.Name}, value: float64(logFiles)},
		activitySample{metric: "logical_logs_not_backed_up", labels: []string{instance.Name}, value: float64(notBackedUp)},
	)

	return samples, nil
}

func closeRows(rows *sql.Rows) error {
	iterationErr := rows.Err()
	closeErr := rows.Close()
	return errors.Join(iterationErr, closeErr)
}

func nullInt(value sql.NullInt64) int64 {
	if value.Valid {
		return value.Int64
	}
	return 0
}
