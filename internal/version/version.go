package version

import "fmt"

var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

func String() string {
	return fmt.Sprintf("informix_exporter version=%s commit=%s build_date=%s", Version, Commit, BuildDate)
}
