package version

import (
	"strings"
	"testing"
)

func TestStringContainsBuildMetadata(t *testing.T) {
	t.Parallel()

	got := String()
	for _, value := range []string{"informix_exporter", "version=", "commit=", "build_date="} {
		if !strings.Contains(got, value) {
			t.Fatalf("String() = %q, want %q", got, value)
		}
	}
}
