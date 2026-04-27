// Package daemon implements the agent-gate daemon gRPC server.
// It manages per-session settings.json files so that /model changes
// in one Claude session don't leak to others.
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/fsnotify/fsnotify"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/config"
)

// Server implements the AgentGateD gRPC service.
type Server struct {
	daemonpb.UnimplementedAgentGateDServer

	log      *slog.Logger
	mu       sync.RWMutex
	sessions map[string]*wrapperSession // keyed by wrapper_id

	watcher        *fsnotify.Watcher
	globalSettings map[string]any // last-known global settings.json content

	sessionLogger *audit.SessionLogger
}

// wrapperSession holds runtime state for one active claude wrapper process.
type wrapperSession struct {
	wrapperID   string
	sessionName string // empty for bare claude invocations
	model       string
}

// New creates a new daemon Server and starts watching the global settings file.
func New(log *slog.Logger, cfg *config.Config) (*Server, error) {
	bg := context.Background()
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create settings watcher: %w", err)
	}

	convDir := config.DefaultConversationsDir()
	level := ""
	if cfg != nil {
		convDir = cfg.ConversationsDir()
		level = cfg.Log.Level
	}
	sessionLogger, err := audit.NewSessionLogger(convDir, level, log)
	if err != nil {
		_ = watcher.Close()
		return nil, fmt.Errorf("failed to create session logger: %w", err)
	}

	s := &Server{
		log:           log,
		sessions:      make(map[string]*wrapperSession),
		watcher:       watcher,
		sessionLogger: sessionLogger,
	}

	globalPath := globalSettingsPath()
	if err := s.loadGlobalSettings(bg); err != nil {
		log.WarnContext(bg, "global settings load failed on startup", "path", globalPath, "err", err)
	} else {
		globalModel, _ := s.globalSettings["model"].(string)
		log.InfoContext(bg, "global settings loaded", "path", globalPath, "model", globalModel, "keys", len(s.globalSettings))
	}

	if err := watcher.Add(globalPath); err != nil {
		log.WarnContext(bg, "failed to watch global settings", "path", globalPath, "err", err)
	} else {
		log.InfoContext(bg, "watching global settings", "path", globalPath)
	}

	go s.watchGlobalSettings()

	return s, nil
}

// Close shuts down the watcher and cleans up all active session runtime dirs.
func (s *Server) Close() {
	bg := context.Background()
	_ = s.watcher.Close()
	if s.sessionLogger != nil {
		_ = s.sessionLogger.Close()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, sess := range s.sessions {
		_ = os.RemoveAll(config.SessionRuntimeDir(sess.wrapperID))
	}
	s.log.InfoContext(bg, "daemon closed", "cleaned_sessions", len(s.sessions))
}

// AcquireSession writes a per-session settings.json (global settings with
// model overridden) and returns the path along with the real claude binary.
func (s *Server) AcquireSession(ctx context.Context, req *daemonpb.AcquireSessionRequest) (*daemonpb.AcquireSessionResponse, error) {
	if req.WrapperId == "" {
		return nil, status.Error(codes.InvalidArgument, "wrapper_id is required")
	}

	model, err := s.resolveModel(ctx, req.SessionName)
	if err != nil {
		s.log.WarnContext(ctx, "model resolution failed, using global", "session", req.SessionName, "err", err)
	}

	settingsFile, err := s.writeSettingsJSON(ctx, req.WrapperId, model)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to write settings: %v", err)
	}

	sess := &wrapperSession{
		wrapperID:   req.WrapperId,
		sessionName: req.SessionName,
		model:       model,
	}

	s.mu.Lock()
	s.sessions[req.WrapperId] = sess
	s.mu.Unlock()

	realClaude, err := findRealClaude()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to find real claude binary: %v", err)
	}

	s.log.InfoContext(ctx, "session acquired",
		"wrapper_id", req.WrapperId,
		"session", req.SessionName,
		"model", model,
		"settings_file", settingsFile,
		"claude_bin", realClaude,
		"active_sessions", len(s.sessions),
	)

	return &daemonpb.AcquireSessionResponse{
		RealClaude:   realClaude,
		Model:        model,
		SettingsFile: settingsFile,
	}, nil
}

// ReleaseSession removes the per-session runtime dir after claude exits.
func (s *Server) ReleaseSession(ctx context.Context, req *daemonpb.ReleaseSessionRequest) (*daemonpb.ReleaseSessionResponse, error) {
	s.mu.Lock()
	sess, ok := s.sessions[req.WrapperId]
	if ok {
		delete(s.sessions, req.WrapperId)
	}
	s.mu.Unlock()

	if ok {
		sessionDir := config.SessionRuntimeDir(sess.wrapperID)
		_ = os.RemoveAll(sessionDir)
		s.log.InfoContext(ctx, "session released",
			"wrapper_id", req.WrapperId,
			"session", sess.sessionName,
			"model", sess.model,
			"active_sessions", len(s.sessions),
		)
	} else {
		s.log.WarnContext(ctx, "release for unknown session", "wrapper_id", req.WrapperId)
	}

	return &daemonpb.ReleaseSessionResponse{}, nil
}

// HookEvent processes a Claude Code hook event forwarded from a wrapper process.
func (s *Server) HookEvent(ctx context.Context, req *daemonpb.HookEventRequest) (*daemonpb.HookEventResponse, error) {
	// TODO: route to hook handler, handle ConfigChange to update per-session model
	return &daemonpb.HookEventResponse{ExitCode: 0}, nil
}

// Audit accepts an audit log entry and enqueues it on the session logger.
// The call returns immediately. Disk writes happen asynchronously.
func (s *Server) Audit(_ context.Context, req *daemonpb.AuditRequest) (*daemonpb.AuditResponse, error) {
	if s.sessionLogger == nil {
		return &daemonpb.AuditResponse{}, nil
	}

	var attrs map[string]any
	if len(req.AttrsJson) > 0 {
		if err := json.Unmarshal(req.AttrsJson, &attrs); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid attrs_json: %v", err)
		}
	}

	s.sessionLogger.Log(req.System, req.SessionId, req.EventName, req.Level, req.Msg, attrs)
	return &daemonpb.AuditResponse{}, nil
}

// writeSettingsJSON writes a per-session settings.json to the runtime dir,
// merging global settings with the per-session model override.
func (s *Server) writeSettingsJSON(ctx context.Context, wrapperID, model string) (string, error) {
	s.mu.RLock()
	globalCopy := make(map[string]any, len(s.globalSettings))
	for k, v := range s.globalSettings {
		globalCopy[k] = v
	}
	globalModel, _ := s.globalSettings["model"].(string)
	s.mu.RUnlock()

	if model != "" {
		globalCopy["model"] = model
	}

	effectiveModel, _ := globalCopy["model"].(string)
	s.log.DebugContext(ctx, "writing per-session settings",
		"wrapper_id", wrapperID,
		"global_model", globalModel,
		"session_model", model,
		"effective_model", effectiveModel,
		"settings_keys", len(globalCopy),
	)

	data, err := json.MarshalIndent(globalCopy, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal settings: %w", err)
	}

	sessionDir := config.SessionRuntimeDir(wrapperID)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		return "", fmt.Errorf("failed to create session dir: %w", err)
	}

	settingsPath := filepath.Join(sessionDir, "settings.json")
	if err := os.WriteFile(settingsPath, data, 0o600); err != nil {
		return "", fmt.Errorf("failed to write settings.json: %w", err)
	}

	return settingsPath, nil
}

// syncAllSessions rewrites settings.json for all active sessions when the
// global settings file changes. Each session's current model is preserved
// so that /model changes in one session don't leak to others.
func (s *Server) syncAllSessions() {
	bg := context.Background()
	s.mu.RLock()
	sessions := make([]*wrapperSession, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.mu.RUnlock()

	for _, sess := range sessions {
		currentModel := s.readSessionModel(sess.wrapperID)
		if currentModel != "" {
			sess.model = currentModel
		}
		s.log.DebugContext(bg, "syncing session",
			"wrapper_id", sess.wrapperID,
			"session", sess.sessionName,
			"preserved_model", sess.model,
		)
		if _, err := s.writeSettingsJSON(bg, sess.wrapperID, sess.model); err != nil {
			s.log.WarnContext(bg, "failed to sync settings", "wrapper_id", sess.wrapperID, "err", err)
		}
	}

	s.log.InfoContext(bg, "global settings synced to all sessions", "active_sessions", len(sessions))
}

// readSessionModel reads the model from a session's current settings.json.
// Returns "" if the file doesn't exist or has no model.
func (s *Server) readSessionModel(wrapperID string) string {
	path := filepath.Join(config.SessionRuntimeDir(wrapperID), "settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return ""
	}
	model, _ := settings["model"].(string)
	return model
}

// watchGlobalSettings runs in a goroutine, syncing global settings changes
// to all active sessions.
func (s *Server) watchGlobalSettings() {
	bg := context.Background()
	for {
		select {
		case event, ok := <-s.watcher.Events:
			if !ok {
				return
			}
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
				s.log.DebugContext(bg, "global settings file changed", "event", event.Op.String())
				if err := s.loadGlobalSettings(bg); err != nil {
					s.log.WarnContext(bg, "failed to reload global settings", "err", err)
					continue
				}
				s.syncAllSessions()
			}

		case err, ok := <-s.watcher.Errors:
			if !ok {
				return
			}
			s.log.WarnContext(bg, "settings watcher error", "err", err)
		}
	}
}

// loadGlobalSettings reads ~/.claude/settings.json into memory.
func (s *Server) loadGlobalSettings(ctx context.Context) error {
	path := globalSettingsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			s.log.DebugContext(ctx, "global settings file not found, using empty", "path", path)
			s.mu.Lock()
			s.globalSettings = make(map[string]any)
			s.mu.Unlock()
			return nil
		}
		return fmt.Errorf("failed to read global settings: %w", err)
	}

	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("failed to parse global settings: %w", err)
	}

	model, _ := settings["model"].(string)
	s.log.DebugContext(ctx, "global settings reloaded", "model", model, "keys", len(settings))

	s.mu.Lock()
	s.globalSettings = settings
	s.mu.Unlock()

	return nil
}

// resolveModel returns the model for a session by looking up session metadata.
// Falls back to the global settings model if no session-specific model is set.
func (s *Server) resolveModel(ctx context.Context, sessionName string) (string, error) {
	s.mu.RLock()
	globalModel, _ := s.globalSettings["model"].(string)
	s.mu.RUnlock()

	if sessionName == "" {
		s.log.DebugContext(ctx, "no session name, using global model", "model", globalModel)
		return globalModel, nil
	}

	// TODO: look up session settings from .agent-gate/sessions/<name>/settings.json
	s.log.DebugContext(ctx, "session model resolution (stub, using global)", "session", sessionName, "model", globalModel)
	return globalModel, nil
}

func globalSettingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".claude", "settings.json")
}
