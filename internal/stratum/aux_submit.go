package stratum

import (
	"errors"
	"sync"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/mergemining"
	"go.uber.org/zap"
)

// auxSent holds the headers already sent to the 1175 node as 1175 blocks, so a share sent again,
// refused or not, does not send its block again. Only real 1175 solutions are kept, for an hour.
var auxSent struct {
	sync.Mutex
	at map[string]time.Time
}

const auxSentFor = time.Hour

// firstAuxSubmit reports whether a 1175 solution with this header hash was not sent before, and
// counts it as sent from now on.
func firstAuxSubmit(header []byte) bool {
	auxSent.Lock()
	defer auxSent.Unlock()
	now := time.Now()
	for k, at := range auxSent.at {
		if now.Sub(at) > auxSentFor {
			delete(auxSent.at, k)
		}
	}
	if _, ok := auxSent.at[string(header)]; ok {
		return false
	}
	if auxSent.at == nil {
		auxSent.at = map[string]time.Time{}
	}
	auxSent.at[string(header)] = now
	return true
}

// auxSolution reports whether a share whose header hash is blockHash (display order) solves its
// job's 1175 work, with merge mining on, and was not sent before.
func (s *Server) auxSolution(job *Job, blockHash []byte) bool {
	if job == nil || job.AuxWork == nil || blockHash == nil || s.getAuxClient() == nil ||
		!auxHashMeetsTarget(blockHash, job.AuxWork.Target) {
		return false
	}
	return firstAuxSubmit(blockHash)
}

// submitRefusedShareAux sends the 1175 block a refused share solves: one on the job of the BCH2
// tip before the current one, or over the intake rate limit. A 1175 block's proof does not depend
// on the BCH2 tip (1175 checks the header's work, the coinbase's commitment and its merkle branch),
// so the share is still a valid 1175 block, and its work is still in the 1175 node. The share
// itself stays refused and is not credited.
func (s *Server) submitRefusedShareAux(job *Job, blockHash []byte, en1, en2, ntime, nonce, versionBits, minerID string, isSolo bool) {
	if !s.auxSolution(job, blockHash) {
		return
	}
	finder := minerID
	// The miner an accepted share is credited to (handleSubmit), so the 1175 ledger names the same.
	if s.config.SoloOnly && s.config.CreditPayoutAddress {
		if payout := normalizeMinerAddress(s.SoloPayoutAddress()); payout != "" {
			finder = payout
		}
	}
	s.logger.Info("A refused share solves a 1175 block; sending it to the 1175 node",
		zap.String("miner", finder), zap.String("aux_hash", job.AuxWork.Hash))
	go s.submitAux(job, en1, en2, ntime, nonce, versionBits, finder, isSolo)
}

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
