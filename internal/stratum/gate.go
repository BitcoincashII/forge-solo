package stratum

import "go.uber.org/zap"

// SetAcceptGate makes the server refuse new connections while gate returns false. The gateway
// program closes its door when it is set to mine only for Forge Pool and cannot reach it, so
// miners fail over to their backup pool instead of mining work nobody will record (ported from
// Forge Pool's satellites, which do the same while their link to the primary is down).
func (s *Server) SetAcceptGate(gate func() bool) {
	s.acceptGate.Store(gate)
}

func (s *Server) acceptOpen() bool {
	gate, _ := s.acceptGate.Load().(func() bool)
	return gate == nil || gate()
}

// DisconnectAll closes every client connection and returns how many there were.
func (s *Server) DisconnectAll(reason string) int {
	n := 0
	s.clients.Range(func(_, value interface{}) bool {
		value.(*Client).Conn.Close()
		n++
		return true
	})
	if n > 0 {
		s.logger.Warn("Disconnected all clients", zap.Int("clients", n), zap.String("reason", reason))
	}
	return n
}
