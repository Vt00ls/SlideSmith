package taskorchestration

import "sync"

type downstreamReplayShell[K comparable, F comparable, E any] struct {
	mu      sync.Mutex
	replays map[K]downstreamReplay[F, E]
}

type downstreamReplay[F comparable, E any] struct {
	fingerprint F
	evidence    E
}

func newDownstreamReplayShell[K comparable, F comparable, E any]() *downstreamReplayShell[K, F, E] {
	return &downstreamReplayShell[K, F, E]{replays: make(map[K]downstreamReplay[F, E])}
}

func invokeDownstreamWithReplay[K comparable, F comparable, R any, E any](
	shell *downstreamReplayShell[K, F, E],
	key K,
	fingerprint F,
	available bool,
	prepare func() *DownstreamError,
	invoke func() (R, error),
	validate func(R) *DownstreamError,
	convert func(R) E,
	clone func(E) E,
) (evidence E, err error) {
	shell.mu.Lock()
	defer shell.mu.Unlock()
	defer func() {
		if recover() != nil {
			var zero E
			evidence = zero
			err = newDownstreamError(DownstreamDependencyUnavailable)
		}
	}()

	if replay, exists := shell.replays[key]; exists {
		if replay.fingerprint != fingerprint {
			return evidence, newDownstreamError(DownstreamIntegrityConflict)
		}
		return clone(replay.evidence), nil
	}
	if !available {
		return evidence, newDownstreamError(DownstreamDependencyUnavailable)
	}
	if prepare != nil {
		if failure := prepare(); failure != nil {
			return evidence, failure
		}
	}
	record, callErr := invoke()
	if callErr != nil {
		return evidence, normalizeDownstreamError(callErr)
	}
	if failure := validate(record); failure != nil {
		return evidence, failure
	}
	evidence = convert(record)
	shell.replays[key] = downstreamReplay[F, E]{fingerprint: fingerprint, evidence: evidence}
	return clone(evidence), nil
}
