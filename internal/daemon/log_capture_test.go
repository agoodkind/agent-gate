package daemon

import (
	"bytes"
	"fmt"
	"log"
	"log/slog"
	"strings"
	"testing"
)

func captureCancellationLogs(t testing.TB) {
	t.Helper()
	original, output := slog.Default(), log.Writer()
	var captured bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&captured, nil)))
	t.Cleanup(func() {
		slog.SetDefault(original)
		log.SetOutput(output)
		for _, line := range strings.Split(strings.TrimSpace(captured.String()), "\n") {
			if line != "" && (t.Failed() || !strings.Contains(line, `err="context canceled"`) && !strings.Contains(line, `: context canceled"`)) {
				fmt.Fprintln(output, line)
			}
		}
	})
}
