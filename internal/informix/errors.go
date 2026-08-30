package informix

import "strings"

// ErrorText removes NUL padding returned by older Informix ODBC drivers so
// operational logs remain readable and bounded to the actual error message.
func ErrorText(err error) string {
	if err == nil {
		return ""
	}
	return strings.TrimSpace(strings.ReplaceAll(err.Error(), "\x00", ""))
}
