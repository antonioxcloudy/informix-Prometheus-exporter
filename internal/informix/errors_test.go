package informix

import (
	"errors"
	"testing"
)

func TestErrorTextRemovesODBCNullPadding(t *testing.T) {
	t.Parallel()

	got := ErrorText(errors.New("connection not open\x00\x00\x00"))
	if got != "connection not open" {
		t.Fatalf("ErrorText() = %q", got)
	}
}
