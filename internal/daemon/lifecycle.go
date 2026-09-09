package daemon

import "context"

// Close shuts down daemon-owned resources.
func (s *Server) Close() {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.cfgMu.Lock()
	s.closing = true
	updateCancel := s.updateCancel
	s.updateCancel = nil
	s.runtimeMu.Lock()
	snapshot := s.runtime.Swap(nil)
	s.runtimeMu.Unlock()
	s.cfgMu.Unlock()

	if s.configWatcher != nil {
		_ = s.configWatcher.Close()
	}
	if updateCancel != nil {
		updateCancel()
	}
	snapshot.close(context.Background(), s.log)
	if s.inferRuntime != nil {
		s.inferRuntime.Close()
	}
	if s.hotKV != nil {
		s.hotKV.Close()
	}
	s.log.InfoContext(context.Background(), "daemon closed")
}
