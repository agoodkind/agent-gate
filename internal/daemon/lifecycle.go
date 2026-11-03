package daemon

import "context"

func (s *Server) lockLifecycle() {
	for {
		s.lifecycleMu.Lock()
		transition := s.storageTransition
		if transition == nil {
			return
		}
		s.lifecycleMu.Unlock()
		<-transition
	}
}

// pauseDeferredWork requires lifecycleMu and releases it while workers finish.
func (s *Server) pauseDeferredWork() {
	s.storageTransition = make(chan struct{})
	s.lifecycleMu.Unlock()
	defer func() {
		s.lifecycleMu.Lock()
		close(s.storageTransition)
		s.storageTransition = nil
	}()
	s.deferredProcessor.pauseAndDrain()
}

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
	s.lockLifecycle()
	s.cfgMu.Lock()
	s.closing = true
	updateCancel := s.updateCancel
	s.updateCancel = nil
	s.runtimeMu.Lock()
	snapshot := s.runtime.Swap(nil)
	s.runtimeMu.Unlock()
	s.cfgMu.Unlock()
	s.lifecycleMu.Unlock()

	if s.configWatcher != nil {
		_ = s.configWatcher.Close()
	}
	if updateCancel != nil {
		updateCancel()
	}
	s.deferredProcessor.Close()
	snapshot.close(context.Background(), s.log)
	if s.hotKV != nil {
		s.hotKV.Close()
	}
	s.log.InfoContext(context.Background(), "daemon closed")
}
