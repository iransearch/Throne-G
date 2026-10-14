package autoselector

import "time"

// SetMemberRecovery binds this selector to its own Xray instance before Start.
// The callback resets a failed transport without reloading the box or TUN.
func (s *AutoSelector) SetMemberRecovery(recover func(string) bool) {
	s.access.Lock()
	s.recoverMember = recover
	s.access.Unlock()
}

func (s *AutoSelector) beginNetworkRecoveryLocked() {
	keepProof := !s.recoveryReturnedAt.IsZero() && time.Now().Before(s.recoveryUntil)
	s.recoveryEpoch++
	s.recoveredMembers = make(map[string]uint64)
	if !keepProof {
		s.recoveryVerifiedMembers = make(map[string]bool)
		s.recoveryUntil = time.Time{} // Arm only after the physical route returns.
		s.recoveryFailureSince = rollbackSince(time.Now(), time.Time{})
	}
}

// A network return buys one repair per failing member. Successful probes never
// reset their transports, and unreachable servers do not cause a reset loop.
func (s *AutoSelector) recoverFailedResults(results []probeResult) []probeResult {
	_, epochOfProbe, allowed := s.networkProbeState()
	if !allowed || len(results) == 0 || results[0].epoch != epochOfProbe {
		return results
	}
	s.access.Lock()
	epoch, recover := s.recoveryEpoch, s.recoverMember
	if epoch != 0 && s.recoveryUntil.IsZero() {
		s.recoveryUntil = time.Now().Add(2 * time.Minute)
	}
	expired := !s.recoveryUntil.IsZero() && time.Now().After(s.recoveryUntil)
	s.access.Unlock()
	if epoch == 0 || recover == nil || expired {
		return results
	}
	repaired := make([]string, 0, len(results))
	for _, result := range results {
		if result.value != rttFailed || isLocalNetworkError(result.err) {
			continue
		}
		if s.ctx.Err() != nil || s.networkInterfaceDown() || (s.pause != nil && s.pause.IsPaused()) {
			break
		}
		s.access.Lock()
		if s.recoveryEpoch != epoch || s.recoveredMembers[result.tag] == epoch {
			s.access.Unlock()
			continue
		}
		if health := s.members[result.tag]; health != nil && health.lastOK.After(result.at) {
			s.access.Unlock()
			continue // Another end-to-end probe already proved it recovered.
		}
		s.recoveredMembers[result.tag] = epoch
		s.access.Unlock()
		if !recover(result.tag) {
			continue
		}
		s.logger.Info("auto-selector: reset failed transport after network return: ", result.tag)
		repaired = append(repaired, result.tag)
	}
	if len(repaired) == 0 || s.ctx.Err() != nil || s.networkInterfaceDown() || (s.pause != nil && s.pause.IsPaused()) {
		return results
	}
	// Retest under the same concurrency bound as ordinary probes.
	for _, fresh := range s.probeBatch(repaired, true) {
		for i := range results {
			if results[i].tag == fresh.tag {
				results[i] = fresh
				break
			}
		}
		if fresh.value != rttFailed {
			s.access.Lock()
			if s.recoveryEpoch == epoch {
				if health := s.members[fresh.tag]; health != nil {
					// This is a new transport, verified externally. Failure
					// samples from its predecessor cannot quarantine it for
					// the five-hour window. Other members/history stay intact.
					health.rollbackFailuresSince(s.recoveryFailureSince)
				}
			}
			s.access.Unlock()
		}
	}
	return results
}
