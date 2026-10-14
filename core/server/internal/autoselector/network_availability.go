package autoselector

import (
	"context"
	"sync"
	"time"
)

// NetworkUnavailable is a direct-network signal, independent of the OS adapter.
// Cancel queued/jittered/in-flight probes and preserve the existing box/TUN.
func (s *AutoSelector) NetworkUnavailable() {
	s.access.Lock()
	if s.externalNetworkOffline {
		s.access.Unlock()
		return
	}
	s.externalNetworkOffline = true
	s.suspended = true
	s.suspendedSince = time.Now()
	s.recoveryReturnedAt = time.Time{}
	s.beginNetworkRecoveryLocked()
	s.restartNetworkProbesLocked()
	s.access.Unlock()
}

// NetworkReturned acknowledges quickly; the immediate sweep runs off the RPC
// thread, shares the normal concurrency bound, and bypasses interval/jitter.
func (s *AutoSelector) NetworkReturned() {
	s.access.Lock()
	newReturn := s.externalNetworkOffline || s.recoveryReturnedAt.IsZero()
	if newReturn {
		since := s.recoveryFailureSince
		s.beginNetworkRecoveryLocked()
		if !since.IsZero() {
			s.recoveryFailureSince = since
		}
		s.recoveryReturnedAt = time.Now()
		s.recoveryUntil = s.recoveryReturnedAt.Add(2 * time.Minute)
		s.externalNetworkOffline = false
		s.suspended = false
		s.suspendedSince = time.Time{}
		s.restartNetworkProbesLocked()
	}
	s.access.Unlock()
	s.queueImmediateCheck(newReturn)
}

func (s *AutoSelector) restartNetworkProbesLocked() {
	if s.probeCancel != nil {
		s.probeCancel()
	}
	s.probeContext, s.probeCancel = context.WithCancel(s.ctx)
	s.probeEpoch++
}

func (s *AutoSelector) networkProbeState() (context.Context, uint64, bool) {
	s.access.Lock()
	ctx := s.probeContext
	if ctx == nil {
		s.probeContext, s.probeCancel = context.WithCancel(s.ctx)
		ctx = s.probeContext
	}
	epoch, offline := s.probeEpoch, s.externalNetworkOffline
	s.access.Unlock()
	return ctx, epoch, !offline && ctx.Err() == nil && !s.networkInterfaceDown() &&
		(s.pause == nil || !s.pause.IsPaused())
}

func (s *AutoSelector) queueImmediateCheck(confirmedReturn bool) {
	s.access.Lock()
	if confirmedReturn && s.checkPending {
		s.confirmedReturnSweep = true
	}
	if s.checkPending || s.externalNetworkOffline || s.ctx.Err() != nil {
		s.access.Unlock()
		return
	}
	s.checkPending = true
	s.confirmedReturnSweep = confirmedReturn
	// Preempt a scheduled round still waiting in its interval jitter.
	s.restartNetworkProbesLocked()
	s.access.Unlock()
	go func() {
		s.roundAccess.Lock()
		defer s.roundAccess.Unlock()
		for {
			probeCtx, epoch, allowed := s.networkProbeState()
			if allowed {
				started := time.Now()
				results := s.recoverFailedResults(s.probeBatchOnNetwork(s.tags, true, probeCtx, epoch))
				s.commitRound(results, started)
			}
			s.access.Lock()
			// A second outage/return superseded this sweep while it drained.
			if epoch != s.probeEpoch && !s.externalNetworkOffline && s.ctx.Err() == nil {
				s.access.Unlock()
				continue
			}
			s.checkPending = false
			s.confirmedReturnSweep = false
			s.access.Unlock()
			return
		}
	}()
}

// Publish the first successful external probe immediately. Slow/dead peers
// must not keep a working member unusable until the entire sweep completes.
func (s *AutoSelector) recordRecoverySuccess(result probeResult) bool {
	s.access.Lock()
	defer s.access.Unlock()
	if s.externalNetworkOffline || result.epoch != s.probeEpoch || s.recoveryReturnedAt.IsZero() ||
		result.at.Before(s.recoveryReturnedAt) || result.value == rttFailed || (result.at.After(s.recoveryUntil) && !s.confirmedReturnSweep) ||
		s.recoveryVerifiedMembers[result.tag] {
		return false
	}
	health := s.members[result.tag]
	if health == nil || !health.lastProbe.Before(result.at) {
		return false
	}
	rolled := health.rollbackFailuresSince(s.recoveryFailureSince)
	s.recoveryVerifiedMembers[result.tag] = true
	health.put(result.value, result.at)
	health.clearCooldown()
	health.lastDialErr = ""
	s.evaluateLocked(result.at)
	s.logger.Info("auto-selector: recovered member verified externally: ", result.tag, " outage failures removed=", rolled)
	return true
}

// Feed urgent checks in order. One goroutine per member can let slow peers
// occupy all slots before the first working member has even started.
func (s *AutoSelector) probeUrgentBatch(batch []string, ctx context.Context, epoch uint64) []probeResult {
	jobs := make(chan string)
	var wg sync.WaitGroup
	var resultMu sync.Mutex
	results := make([]probeResult, 0, len(batch))
	for worker := 0; worker < min(max(1, s.concurrency), len(batch)); worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for tag := range jobs {
				if ctx.Err() != nil {
					return
				}
				if s.networkInterfaceDown() || (s.pause != nil && s.pause.IsPaused()) {
					continue
				}
				detour := s.outbounds[tag]
				if detour == nil {
					continue
				}
				s.access.Lock()
				s.probesInFlight++
				s.access.Unlock()
				value, err := s.probeWithContext(ctx, detour)
				s.access.Lock()
				s.probesInFlight--
				s.access.Unlock()
				if ctx.Err() != nil {
					return
				}
				result := probeResult{tag: tag, value: value, err: err, at: time.Now(), epoch: epoch}
				if value == rttFailed {
					// A stale Hysteria client is repaired as soon as its own probe
					// fails; it must not wait for every dead peer in the sweep.
					fresh := s.recoverFailedResults([]probeResult{result})
					if len(fresh) > 0 {
						result = fresh[0]
					}
					if ctx.Err() != nil {
						return
					}
				} else {
					s.recordRecoverySuccess(result)
				}
				resultMu.Lock()
				results = append(results, result)
				resultMu.Unlock()
			}
		}()
	}
feed:
	for _, tag := range batch {
		select {
		case jobs <- tag:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	return results
}
