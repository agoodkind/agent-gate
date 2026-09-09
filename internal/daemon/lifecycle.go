package daemon

import "context"

// Close shuts down daemon-owned resources.
func (s *Server) Close() {
	s.closeOnce.Do(s.close)
}

func (s *Server) close() {
	s.cfgMu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	if s.auditCancel != nil {
		s.auditCancel()
	}
	s.cfgMu.Unlock()
	s.auditWG.Wait()
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
