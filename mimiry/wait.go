package mimiry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// WaitOptions shapes a wait. The wait ends when ctx does: set a deadline on
// ctx to bound it.
type WaitOptions struct {
	// Interval between polls. Zero means 5 seconds.
	Interval time.Duration
	// NotFoundGrace is how long a resource that has never been seen may answer
	// 404 before the wait gives up. A create is accepted before the resource is
	// readable, so a 404 just after it is expected. Zero means 60 seconds.
	NotFoundGrace time.Duration
	// OnPoll, when set, is called after every poll: with the resource when the
	// read succeeded, or with the error of a read the wait is riding out
	// (a 5xx, a network failure, the early 404s).
	OnPoll func(resource any, transient error)
}

func (o WaitOptions) interval() time.Duration {
	if o.Interval > 0 {
		return o.Interval
	}
	return 5 * time.Second
}

func (o WaitOptions) grace() time.Duration {
	if o.NotFoundGrace > 0 {
		return o.NotFoundGrace
	}
	return 60 * time.Second
}

// SessionFailedError is returned by WaitReady when the session reached a state
// from which its workload will not run, or finished with a non-zero exit code.
type SessionFailedError struct {
	Session *Session
}

func (e *SessionFailedError) Error() string {
	s := e.Session
	msg := fmt.Sprintf("session %s reached state %s", s.ID, s.State)
	if s.ExitCode != nil {
		msg += fmt.Sprintf(" with exit code %d", *s.ExitCode)
	}
	if s.Error != nil && *s.Error != "" {
		msg += ": " + *s.Error
	}
	return msg
}

// WaitReady polls a session until it is ready to use or has finished.
//
// It returns the session, with no error, when:
//   - it is running and reachable — SSH is up, or SSH is disabled;
//   - its workload already finished cleanly (exited, idle, terminating or
//     terminated, with exit code 0 or none). A short command can finish
//     between two polls; that is a success, not a missed start.
//
// It returns *SessionFailedError when the session reaches provision_failed,
// pull_failed or start_failed — nothing further runs on that machine — or
// finishes with a non-zero exit code.
//
// boot_failed and setup_failed are NOT failures here: they are reported by a
// machine that carries on, and the container may still start, degraded. The
// wait continues; OnPoll sees the state.
func (s *SessionsService) WaitReady(ctx context.Context, id string, o WaitOptions) (*Session, error) {
	none := 0
	return poll(ctx, o, func(ctx context.Context) (*Session, bool, error) {
		sess, err := s.Get(ctx, id, GetSessionOptions{EventsTail: &none})
		if err != nil {
			return nil, false, err
		}
		switch sess.State {
		case SessionProvisionFailed, SessionPullFailed, SessionStartFailed:
			return sess, true, &SessionFailedError{Session: sess}
		case SessionExited, SessionIdle, SessionTerminating, SessionTerminated:
			if sess.ExitCode != nil && *sess.ExitCode != 0 {
				return sess, true, &SessionFailedError{Session: sess}
			}
			return sess, true, nil
		case SessionRunning:
			if !sess.SSHEnabled || (sess.SSH != nil && sess.SSH.Host != "") {
				return sess, true, nil
			}
		}
		return sess, false, nil
	})
}

// VolumeFailedError is returned by WaitProvisioned when the volume failed or
// was deleted.
type VolumeFailedError struct {
	Volume *Volume
}

func (e *VolumeFailedError) Error() string {
	msg := fmt.Sprintf("volume %s reached state %s", e.Volume.ID, e.Volume.State)
	if e.Volume.Reason != "" {
		msg += ": " + e.Volume.Reason
	}
	return msg
}

// WaitProvisioned polls a volume until it is provisioned with no operation in
// progress — after a create or an extend — or has failed.
func (v *VolumesService) WaitProvisioned(ctx context.Context, id string, o WaitOptions) (*Volume, error) {
	return poll(ctx, o, func(ctx context.Context) (*Volume, bool, error) {
		vol, err := v.Get(ctx, id)
		if err != nil {
			return nil, false, err
		}
		switch vol.State {
		case VolumeFailed, VolumeDeleted:
			return vol, true, &VolumeFailedError{Volume: vol}
		case VolumeProvisioned:
			if vol.Operation == "" {
				return vol, true, nil
			}
		}
		return vol, false, nil
	})
}

// poll runs check until it reports done, an error that is not transient, or
// ctx ends.
//
// Transient — ridden out: a network failure, a 5xx, a 429, and a 404 on a
// resource never yet seen, for up to the grace period. Anything else ends the
// wait with that error. A 404 after the resource was seen means it is gone.
func poll[T any](ctx context.Context, o WaitOptions, check func(context.Context) (*T, bool, error)) (*T, error) {
	start := time.Now()
	seen := false
	var last *T
	var lastErr error

	for {
		res, done, err := check(ctx)
		if res != nil {
			seen = true
			last = res
		}
		if done {
			return res, err
		}

		if err != nil {
			if ctx.Err() != nil {
				// The request the deadline cut short says nothing about the
				// resource; the last completed poll does.
				return last, waitEnded(ctx, lastErr)
			}
			if !transient(err, seen, time.Since(start), o.grace()) {
				return last, err
			}
			lastErr = err
		} else {
			lastErr = nil
		}
		if o.OnPoll != nil {
			if err != nil {
				o.OnPoll(nil, err)
			} else {
				o.OnPoll(res, nil)
			}
		}

		select {
		case <-ctx.Done():
			return last, waitEnded(ctx, lastErr)
		case <-time.After(o.interval()):
		}
	}
}

func transient(err error, seen bool, elapsed, grace time.Duration) bool {
	var e *APIError
	if !errors.As(err, &e) {
		return true // could not reach the API
	}
	switch {
	case e.StatusCode == http.StatusNotFound:
		return !seen && elapsed < grace
	case e.StatusCode == http.StatusTooManyRequests, e.StatusCode >= 500:
		return true
	}
	return false
}

func waitEnded(ctx context.Context, lastErr error) error {
	if lastErr != nil {
		return fmt.Errorf("mimiry: wait ended (%w); last poll: %v", ctx.Err(), lastErr)
	}
	return fmt.Errorf("mimiry: wait ended: %w", ctx.Err())
}
