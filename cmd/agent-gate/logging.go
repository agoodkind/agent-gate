package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/logstorage"
	"goodkind.io/gklog"
)

func openBoundedLog(component string) (*slog.Logger, func()) {
	stateDir := config.DefaultStateDir()
	maintenanceErr := logstorage.Maintain(stateDir)
	writer, err := logstorage.OpenDiagnostic(stateDir)
	if err != nil {
		logger := slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("component", component)
		logger.Error("open diagnostic log failed", "err", err)
		return logger, func() {}
	}
	logger, _ := gklog.New(gklog.Config{
		Handlers: []slog.Handler{
			slog.NewJSONHandler(diagnosticErrorWriter{writer}, &slog.HandlerOptions{Level: slog.LevelDebug}),
		},
	})
	if maintenanceErr != nil {
		logger.Warn("maintain local logs failed", "err", maintenanceErr)
	}
	return logger.With("component", component), func() {
		if err := writer.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "agent-gate: close diagnostic log: %v\n", err)
		}
	}
}

type diagnosticErrorWriter struct {
	writer io.Writer
}

func (writer diagnosticErrorWriter) Write(data []byte) (int, error) {
	count, err := writer.writer.Write(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate: write diagnostic log: %v\n", err)
	}
	return count, err
}
