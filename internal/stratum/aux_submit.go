package stratum

import (
	"errors"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/mergemining"
	"go.uber.org/zap"
)

// How long a solved 1175 block is kept in front of a 1175 node that does not answer, and the
// waits between tries. The node keeps the block's work for about an hour (its last 256 work
// items, fetched every 15 s), and a 1175 block is worth a whole 1175 reward.
var (
	auxRetryFirstWait = time.Second
	auxRetryMaxWait   = 15 * time.Second
	auxSubmitRetryFor = 5 * time.Minute
)

// sendAuxBlock submits a solved 1175 block until the 1175 node has decided it. A node that is busy
// (HTTP 503), slow (no answer in time), or starting (-28) is asked again. An answer that did not
// arrive may still have been taken, and a node that took the block no longer has its work (-8),
// so then the chain is asked: a block on it was accepted. Any other answer from the node is its
// decision.
func (s *Server) sendAuxBlock(ac *mergemining.Client, job *Job, auxHex, finder string, isSolo bool) {
	hash := job.AuxWork.Hash
	deadline := time.Now().Add(auxSubmitRetryFor)
	for try, wait := 1, auxRetryFirstWait; ; try, wait = try+1, min(2*wait, auxRetryMaxWait) {
		accepted, err := ac.SubmitAuxBlock(hash, auxHex)
		if err == nil {
			if accepted {
				s.auxBlockFound(job, finder, isSolo)
			} else {
				s.logger.Warn("aux: block rejected (likely stale aux tip)", zap.String("aux_hash", hash))
			}
			return
		}
		var rpcErr *mergemining.RPCError
		answered := errors.As(err, &rpcErr) && rpcErr.Code != mergemining.RPCInWarmup
		if !answered || rpcErr.Code == mergemining.RPCInvalidParameter {
			if confs, found, cerr := ac.BlockConfirmations(hash); cerr == nil && found && confs > 0 {
				s.auxBlockFound(job, finder, isSolo)
				return
			}
		}
		if answered {
			s.logger.Warn("aux: submitauxblock error", zap.String("aux_hash", hash), zap.Int("tries", try), zap.Error(err))
			return
		}
		if time.Now().Add(wait).After(deadline) {
			s.logger.Error("aux: gave up submitting a solved 1175 block: the 1175 node did not answer",
				zap.String("aux_hash", hash), zap.Int("tries", try), zap.Error(err))
			return
		}
		if try == 1 {
			s.logger.Warn("aux: submitauxblock got no answer; trying again", zap.String("aux_hash", hash),
				zap.Duration("for", auxSubmitRetryFor), zap.Error(err))
		}
		time.Sleep(wait)
	}
}

// auxBlockFound reports a 1175 block the node accepted.
func (s *Server) auxBlockFound(job *Job, finder string, isSolo bool) {
	s.logger.Info("🎉 AUX (1175) BLOCK FOUND",
		zap.Int64("aux_height", job.AuxWork.Height),
		zap.String("aux_hash", job.AuxWork.Hash))
	if cb := s.getOnAuxBlock(); cb != nil {
		cb(job.AuxWork.Height, job.AuxWork.Hash, job.AuxWork.CoinbaseValue, finder, isSolo)
	}
}
