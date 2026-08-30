package informix

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "github.com/alexbrainman/odbc"
	"github.com/anmartsan/prometheus-informix-exporter/internal/config"
)

// Database is the subset of database/sql required by the collectors.
type Database interface {
	PingContext(context.Context) error
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	Close() error
}

type Instance struct {
	Name           string
	InformixServer string
	Database       Database
}

func OpenInstances(servers []config.Server) ([]Instance, error) {
	instances := make([]Instance, 0, len(servers))
	for _, server := range servers {
		db, err := sql.Open("odbc", "DSN="+server.InformixServer)
		if err != nil {
			_ = CloseInstances(instances)
			return nil, fmt.Errorf("open DSN %q: %w", server.InformixServer, err)
		}

		// Queries are serialized within each instance, while separate instances
		// are scraped concurrently. Keeping one connection per DSN prevents the
		// legacy ODBC driver from handing a stale second connection to a collector
		// after Docker or the database server resumes from an idle period.
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		db.SetConnMaxIdleTime(5 * time.Minute)
		db.SetConnMaxLifetime(30 * time.Minute)
		instances = append(instances, Instance{
			Name: server.Name, InformixServer: server.InformixServer, Database: db,
		})
	}
	return instances, nil
}

func CloseInstances(instances []Instance) error {
	var closeErr error
	for _, instance := range instances {
		if instance.Database == nil {
			continue
		}
		if err := instance.Database.Close(); err != nil {
			closeErr = errors.Join(closeErr, fmt.Errorf("close DSN %q: %w", instance.InformixServer, err))
		}
	}
	return closeErr
}
