// Package daemon implements the agent-gate daemon gRPC server.
// The daemon is the monolith: it manages per-process fake HOME directories
// in XDG_RUNTIME_DIR, syncs global Claude settings live, and processes
// all hook events from wrapper-launched sessions.
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
	"goodkind.io/agent-gate/internal/config"
)

// Server implements the AgentGateD gRPC service.
type Server struct {
	daemonpb.UnimplementedAgentGateDServer

	log     *slog.Logger
	mu      sync.RWMutex
	sessions map[string]*wrapperSession // keyed by wrapper_id

	watcher     *fsnotify.Watcher
	globalSettings map[string]any // last-known global settings.json content
}

// wrapperSession holds runtime state for one active claude wrapper process.
type wrapperSession struct {
	wrapperID   string
	sessionName string // empty for bare claude invocations
	model       string
	fakeHome    string
}

// New creates a new daemon Server and starts watching the global settings file.
func New(log *slog.Logger) (*Server, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create settings watcher: %w", err)
	}

	s := &Server{
		log:      log,
		sessions: make(map[string]*wrapperSession),
		watcher:  watcher,
	}

	if err := s.loadGlobalSettings(); err != nil {
		log.Warn("failed to load global settings on startup", "err", err)
	}

	globalPath := globalSettingsPath()
	if err := watcher.Add(globalPath); err != nil {
		log.Warn("failed to watch global settings", "path", globalPath, "err", err)
	}

	go s.watchGlobalSettings()

	return s, nil
}

// Close shuts down the watcher and cleans up all active session fake homes.
func (s *Server) Close() {
	_ = s.watcher.Close()

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, sess := range s.sessions {
		s.cleanupFakeHome(sess)
	}
}

// AcquireSession creates a fake HOME for a new claude wrapper process.
func (s *Server) AcquireSession(ctx context.Context, req *daemonpb.AcquireSessionRequest) (*daemonpb.AcquireSessionResponse, error) {
	if req.WrapperId == "" {
		return nil, status.Error(codes.InvalidArgument, "wrapper_id is required")
	}

	model, err := s.resolveModel(req.SessionName)
	if err != nil {
		s.log.Warn("failed to resolve model, using empty", "err", err)
	}

	fakeHome, err := s.createFakeHome(req.WrapperId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create fake home: %v", err)
	}

	settingsFile, err := s.writeSettingsJSON(req.WrapperId, model)
	if err != nil {
		_ = os.RemoveAll(fakeHome)
		return nil, status.Errorf(codes.Internal, "failed to write settings: %v", err)
	}

	sess := &wrapperSession{
		wrapperID:   req.WrapperId,
		sessionName: req.SessionName,
		model:       model,
		fakeHome:    fakeHome,
	}

	s.mu.Lock()
	s.sessions[req.WrapperId] = sess
	s.mu.Unlock()

	realClaude, err := findRealClaude()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to find real claude binary: %v", err)
	}

	s.log.Info("session acquired", "wrapper_id", req.WrapperId, "session", req.SessionName, "model", model, "fake_home", fakeHome)

	return &daemonpb.AcquireSessionResponse{
		FakeHome:     fakeHome,
		RealClaude:   realClaude,
		Model:        model,
		SettingsFile: settingsFile,
	}, nil
}

// ReleaseSession cleans up the fake HOME after claude exits.
func (s *Server) ReleaseSession(ctx context.Context, req *daemonpb.ReleaseSessionRequest) (*daemonpb.ReleaseSessionResponse, error) {
	s.mu.Lock()
	sess, ok := s.sessions[req.WrapperId]
	if ok {
		delete(s.sessions, req.WrapperId)
	}
	s.mu.Unlock()

	if ok {
		s.cleanupFakeHome(sess)
		s.log.Info("session released", "wrapper_id", req.WrapperId)
	}

	return &daemonpb.ReleaseSessionResponse{}, nil
}

// HookEvent processes a Claude Code hook event forwarded from a wrapper process.
func (s *Server) HookEvent(ctx context.Context, req *daemonpb.HookEventRequest) (*daemonpb.HookEventResponse, error) {
	// TODO: route to hook handler, handle ConfigChange to update per-session model
	return &daemonpb.HookEventResponse{ExitCode: 0}, nil
}

// createFakeHome sets up the per-process fake HOME directory structure.
func (s *Server) createFakeHome(wrapperID string) (string, error) {
	fakeHome := config.FakeHomeDir(wrapperID)

	claudeDir := filepath.Join(fakeHome, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		return "", fmt.Errorf("failed to create fake .claude dir: %w", err)
	}

	realClaudeDir := globalClaudeDir()

	// Symlink projects/ so transcripts land in the real location.
	if err := symlinkIfExists(filepath.Join(realClaudeDir, "projects"), filepath.Join(claudeDir, "projects")); err != nil {
		return "", err
	}

	// Symlink output-styles/ so custom styles are accessible.
	if err := symlinkIfExists(filepath.Join(realClaudeDir, "output-styles"), filepath.Join(claudeDir, "output-styles")); err != nil {
		return "", err
	}

	return fakeHome, nil
}

// writeSettingsJSON writes settings.json into the fake home, merging global
// settings with the per-session model override.
func (s *Server) writeSettingsJSON(wrapperID, model string) (string, error) {
	s.mu.RLock()
	globalCopy := make(map[string]any, len(s.globalSettings))
	for k, v := range s.globalSettings {
		globalCopy[k] = v
	}
	s.mu.RUnlock()

	// Inject per-session model. Everything else comes from the live global copy.
	if model != "" {
		globalCopy["model"] = model
	}

	data, err := json.MarshalIndent(globalCopy, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal settings: %w", err)
	}

	settingsPath := config.FakeSettingsPath(wrapperID)
	if err := os.WriteFile(settingsPath, data, 0o600); err != nil {
		return "", fmt.Errorf("failed to write settings.json: %w", err)
	}

	return settingsPath, nil
}

// syncAllFakeHomes rewrites settings.json in all active fake homes when
// the global settings file changes, preserving each session's model.
func (s *Server) syncAllFakeHomes() {
	s.mu.RLock()
	sessions := make([]*wrapperSession, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.mu.RUnlock()

	for _, sess := range sessions {
		if _, err := s.writeSettingsJSON(sess.wrapperID, sess.model); err != nil {
			s.log.Warn("failed to sync settings to fake home", "wrapper_id", sess.wrapperID, "err", err)
		}
	}
}

// watchGlobalSettings runs in a goroutine, syncing global settings changes to all fake homes.
func (s *Server) watchGlobalSettings() {
	for {
		select {
		case event, ok := <-s.watcher.Events:
			if !ok {
				return
			}
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
				if err := s.loadGlobalSettings(); err != nil {
					s.log.Warn("failed to reload global settings", "err", err)
					continue
				}
				s.syncAllFakeHomes()
				s.log.Debug("synced global settings to all fake homes", "active_sessions", len(s.sessions))
			}

		case err, ok := <-s.watcher.Errors:
			if !ok {
				return
			}
			s.log.Warn("settings watcher error", "err", err)
		}
	}
}

// loadGlobalSettings reads ~/.claude/settings.json into memory.
func (s *Server) loadGlobalSettings() error {
	data, err := os.ReadFile(globalSettingsPath())
	if err != nil {
		if os.IsNotExist(err) {
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

	s.mu.Lock()
	s.globalSettings = settings
	s.mu.Unlock()

	return nil
}

// resolveModel returns the model for a session by looking up session metadata.
// Falls back to the global settings model if no session-specific model is set.
func (s *Server) resolveModel(sessionName string) (string, error) {
	if sessionName == "" {
		s.mu.RLock()
		model, _ := s.globalSettings["model"].(string)
		s.mu.RUnlock()
		return model, nil
	}

	// TODO: look up session settings from .agent-gate/sessions/<name>/settings.json
	// For now fall back to global model.
	s.mu.RLock()
	model, _ := s.globalSettings["model"].(string)
	s.mu.RUnlock()
	return model, nil
}

// cleanupFakeHome removes the per-process fake HOME directory.
func (s *Server) cleanupFakeHome(sess *wrapperSession) {
	if sess.fakeHome == "" {
		return
	}
	if err := os.RemoveAll(filepath.Dir(sess.fakeHome)); err != nil {
		s.log.Warn("failed to cleanup fake home", "wrapper_id", sess.wrapperID, "err", err)
	}
}

func globalClaudeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".claude")
}

func globalSettingsPath() string {
	return filepath.Join(globalClaudeDir(), "settings.json")
}

func symlinkIfExists(target, link string) error {
	if _, err := os.Stat(target); os.IsNotExist(err) {
		return nil
	}
	if err := os.Symlink(target, link); err != nil && !os.IsExist(err) {
		return fmt.Errorf("failed to symlink %s -> %s: %w", link, target, err)
	}
	return nil
}
