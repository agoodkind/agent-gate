// Package daemon implements the agent-gate daemon gRPC server.
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
	"goodkind.io/agent-gate/internal/version"
)

const configReloadDebounce = 200 * time.Millisecond

// Server implements the AgentGateD gRPC service.
type Server struct {
	daemonpb.UnimplementedAgentGateDServer

	log           *slog.Logger
	cfgMu         sync.RWMutex
	cfg           *config.Config
	eventLogger   *audit.EventLogger
	configWatcher *fsnotify.Watcher
	configPath    string
	closing       bool
}

// New creates a new daemon Server.
func New(log *slog.Logger, cfg *config.Config) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	if errs := hook.ValidateConfig(cfg); len(errs) > 0 {
		return nil, fmt.Errorf("invalid hook config: %v", errs[0]) //nolint:wrapped_error_without_slog
	}

	eventLogger, err := audit.NewEventLogger(cfg, log)
	if err != nil {
		return nil, fmt.Errorf("failed to create event logger: %w", err) //nolint:wrapped_error_without_slog
	}

	s := &Server{
		log:         log,
		cfg:         cfg,
		eventLogger: eventLogger,
		configPath:  config.ConfigPath(),
	}
	if err := s.startConfigWatcher(); err != nil {
		_ = eventLogger.Close()
		return nil, err
	}
	return s, nil
}

// Close shuts down daemon-owned resources.
func (s *Server) Close() {
	s.cfgMu.Lock()
	s.closing = true
	eventLogger := s.eventLogger
	s.eventLogger = nil
	s.cfgMu.Unlock()

	if s.configWatcher != nil {
		_ = s.configWatcher.Close()
	}
	if eventLogger != nil {
		_ = eventLogger.Close()
	}
	s.log.InfoContext(context.Background(), "daemon closed")
}

func (s *Server) startConfigWatcher() error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create config watcher: %w", err) //nolint:wrapped_error_without_slog
	}

	configDir := filepath.Dir(s.configPath)
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		_ = watcher.Close()
		return fmt.Errorf("failed to create config directory for watcher: %w", err) //nolint:wrapped_error_without_slog
	}
	if err := watcher.Add(configDir); err != nil {
		_ = watcher.Close()
		return fmt.Errorf("failed to watch config directory: %w", err) //nolint:wrapped_error_without_slog
	}

	s.configWatcher = watcher
	s.log.InfoContext(context.Background(), "watching config", "path", s.configPath)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.log.ErrorContext(context.Background(), "config watcher panic", "err", r)
			}
		}()
		s.watchConfigFile()
	}()
	return nil
}

func (s *Server) watchConfigFile() {
	ctx := context.Background()
	timer := time.NewTimer(configReloadDebounce)
	if !timer.Stop() {
		<-timer.C
	}
	pending := false
	defer func() { _ = timer.Stop() }()

	for {
		select {
		case event, ok := <-s.configWatcher.Events:
			if !ok {
				return
			}
			if s.shouldReloadConfig(event) {
				pending = true
				resetTimer(timer, configReloadDebounce)
				s.log.DebugContext(ctx, "config change detected", "path", s.configPath, "event", event.Op.String())
			}

		case <-timer.C:
			if !pending {
				continue
			}
			pending = false
			if err := s.reloadConfig(ctx); err != nil {
				s.log.WarnContext(ctx, "config reload rejected", "path", s.configPath, "err", err)
			}

		case err, ok := <-s.configWatcher.Errors:
			if !ok {
				return
			}
			s.log.WarnContext(ctx, "config watcher error", "path", s.configPath, "err", err)
		}
	}
}

func (s *Server) shouldReloadConfig(event fsnotify.Event) bool {
	if filepath.Clean(event.Name) != filepath.Clean(s.configPath) {
		return false
	}
	reloadEvents := fsnotify.Write | fsnotify.Create | fsnotify.Rename | fsnotify.Remove | fsnotify.Chmod
	return event.Op&reloadEvents != 0
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

func (s *Server) reloadConfig(ctx context.Context) error {
	candidate, err := config.LoadExisting(s.configPath)
	if err != nil {
		return fmt.Errorf("config load or compile failed: %w", err)
	}
	if errs := hook.ValidateConfig(candidate); len(errs) > 0 {
		return fmt.Errorf("hook config validation failed: %w", errs[0])
	}

	newEventLogger, err := audit.NewEventLogger(candidate, s.log)
	if err != nil {
		return fmt.Errorf("failed to create event logger for reloaded config: %w", err)
	}

	s.cfgMu.Lock()
	if s.closing {
		s.cfgMu.Unlock()
		_ = newEventLogger.Close()
		return nil
	}
	oldEventLogger := s.eventLogger
	s.cfg = candidate
	s.eventLogger = newEventLogger
	s.cfgMu.Unlock()

	if oldEventLogger != nil {
		if err := oldEventLogger.Close(); err != nil {
			s.log.WarnContext(ctx, "old audit logger close failed after config reload", "err", err)
		}
	}
	s.log.InfoContext(ctx, "config reloaded", "path", s.configPath, "rules", len(candidate.Rules), "audit_enabled", candidate.AuditEnabled())
	return nil
}

// EvaluateHook processes a hook event through daemon-owned enforcement.
func (s *Server) EvaluateHook(ctx context.Context, req *daemonpb.EvaluateHookRequest) (*daemonpb.EvaluateHookResponse, error) {
	rawJSON := req.RawJson
	if req.Cwd != "" {
		rawJSON = injectCWD(rawJSON, req.Cwd)
	}

	getenv := func(key string) string {
		if req.EnvFingerprint == nil {
			return ""
		}
		return req.EnvFingerprint[key]
	}

	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	cfg := s.cfg
	eventLogger := s.eventLogger
	sink := audit.NewLocalSink(eventLogger)
	stdout, stderr, exitCode := hook.HandleWithEnv(ctx, rawJSON, cfg, sink, hook.SystemFromString(req.ProviderHint), getenv)
	return &daemonpb.EvaluateHookResponse{
		ExitCode:   int32(exitCode),
		StdoutData: stdout,
		StderrData: stderr,
	}, nil
}

func injectCWD(rawJSON []byte, cwd string) []byte {
	if cwd == "" || len(rawJSON) == 0 || rawJSON[len(rawJSON)-1] != '}' {
		return rawJSON
	}
	insert := []byte(`,"cwd":"` + escapeJSONString(cwd) + `"}`)
	out := make([]byte, 0, len(rawJSON)+len(insert))
	out = append(out, rawJSON[:len(rawJSON)-1]...)
	out = append(out, insert...)
	return out
}

func escapeJSONString(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	return replacer.Replace(value)
}

func (s *Server) Status(ctx context.Context, _ *daemonpb.StatusRequest) (*daemonpb.StatusResponse, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "resolve executable: %v", err)
	}
	return &daemonpb.StatusResponse{
		Pid:            int64(os.Getpid()),
		ExecutablePath: exe,
		SocketPath:     config.DaemonSocketPath(),
		Version:        version.Version,
		Commit:         version.Commit,
		Dirty:          version.Dirty,
		BuildHash:      version.BuildHash(),
	}, nil
}
