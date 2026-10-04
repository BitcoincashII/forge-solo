//go:build sqlite && windows

package pgmigrate

import "context"

// Server does not exist on Windows: the launcher starts PostgreSQL itself (pg_ctl) and runs
// forge-solo-migrate's plan, prepare and commit; run is the Umbrel container's.
type Server struct{}

// StartServer refuses: see Server.
func StartServer(ctx context.Context, pgdata string, logf func(string, ...any)) (*Server, error) {
	return nil, newErr(CodeOther, "run is not used on Windows, where the launcher starts PostgreSQL and runs prepare and commit", nil)
}

func (s *Server) DSN() string          { return "" }
func (s *Server) Stop() error          { return nil }
func (s *Server) LogTail(n int) string { return "" }
