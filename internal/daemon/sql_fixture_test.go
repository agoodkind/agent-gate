package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func readDaemonSQLFixture(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
