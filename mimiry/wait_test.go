package mimiry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// script answers successive GETs with the given responses, repeating the last.
// A response is "status body" or a JSON body (200).
func script(t *testing.T, responses ...string) (http.HandlerFunc, func() int) {
	var mu sync.Mutex
	n := 0
	return func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			i := n
			n++
			mu.Unlock()
			if i >= len(responses) {
				i = len(responses) - 1
			}
			resp := responses[i]
			if code, body, ok := strings.Cut(resp, " "); ok && len(code) == 3 && code[0] >= '1' && code[0] <= '5' {
				var status int
				fmt.Sscan(code, &status)
				w.WriteHeader(status)
				io.WriteString(w, body)
				return
			}
			io.WriteString(w, resp)
		}, func() int {
			mu.Lock()
			defer mu.Unlock()
			return n
		}
}

func sess(state string, extra string) string {
	return fmt.Sprintf(`{"id":"s-1","state":%q,"ssh_enabled":true%s}`, state, extra)
}

var fast = WaitOptions{Interval: time.Millisecond, NotFoundGrace: time.Second}

func TestWaitReadyFollowsTheSessionToRunning(t *testing.T) {
	h, calls := script(t,
		`404 {"error":"not_found"}`, // not readable yet just after create
		sess("submitted", ""),
		sess("boot_failed", `,"error":"driver check timed out"`), // degraded, carries on
		`502 <html>bad gateway</html>`,                           // transient
		sess("running", ""),                                      // running, SSH not up yet
		sess("running", `,"ssh":{"host":"1.2.3.4","port":22}`),
	)
	c := computeClient(t, h)
	var polled []string
	o := fast
	o.OnPoll = func(r any, err error) {
		if s, ok := r.(*Session); ok {
			polled = append(polled, string(s.State))
		} else {
			polled = append(polled, "err")
		}
	}
	s, err := c.Sessions().WaitReady(context.Background(), "s-1", o)
	if err != nil {
		t.Fatal(err)
	}
	if s.SSH == nil || s.SSH.Host != "1.2.3.4" || calls() != 6 {
		t.Fatalf("returned %+v after %d calls", s, calls())
	}
	if got := strings.Join(polled, " "); got != "err submitted boot_failed err running" {
		t.Fatalf("OnPoll saw %q", got)
	}
}

func TestWaitReadyOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answer  string
		wantErr bool
	}{
		{"running without SSH", `{"id":"s-1","state":"running","ssh_enabled":false}`, false},
		{"finished between polls", sess("exited", `,"exit_code":0`), false},
		{"parked after finishing", sess("idle", ""), false},
		{"terminated, no exit code", sess("terminated", ""), false},
		{"exited non-zero", sess("exited", `,"exit_code":3`), true},
		{"provision failed", sess("provision_failed", `,"error":"no capacity"`), true},
		{"pull failed", sess("pull_failed", ""), true},
		{"start failed", sess("start_failed", ""), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := script(t, tc.answer)
			c := computeClient(t, h)
			s, err := c.Sessions().WaitReady(context.Background(), "s-1", fast)
			var failed *SessionFailedError
			if tc.wantErr != errors.As(err, &failed) || s == nil {
				t.Fatalf("got %+v, %v", s, err)
			}
			if tc.wantErr && failed.Session != s {
				t.Fatal("the error does not carry the session")
			}
		})
	}
}

func TestWaitReadyGivesUpOnA404ThatLasts(t *testing.T) {
	h, _ := script(t, `404 {"error":"not_found"}`)
	c := computeClient(t, h)
	o := fast
	o.NotFoundGrace = 20 * time.Millisecond
	_, err := c.Sessions().WaitReady(context.Background(), "s-1", o)
	if !IsNotFound(err) {
		t.Fatalf("got %v, want the 404", err)
	}
}

// A session that was seen and then answers 404 is gone; no grace.
func TestWaitReadyStopsWhenASeenSessionDisappears(t *testing.T) {
	h, calls := script(t, sess("submitted", ""), `404 {"error":"not_found"}`)
	c := computeClient(t, h)
	o := fast
	o.NotFoundGrace = time.Hour
	_, err := c.Sessions().WaitReady(context.Background(), "s-1", o)
	if !IsNotFound(err) || calls() != 2 {
		t.Fatalf("got %v after %d calls", err, calls())
	}
}

func TestWaitStopsOnAnErrorThatIsNotTransient(t *testing.T) {
	h, calls := script(t, `403 {"error":"forbidden"}`)
	c := computeClient(t, h)
	_, err := c.Sessions().WaitReady(context.Background(), "s-1", fast)
	var e *APIError
	if !errors.As(err, &e) || e.StatusCode != 403 || calls() != 1 {
		t.Fatalf("got %v after %d calls", err, calls())
	}
}

func TestWaitEndsWithTheContextAndSaysWhatItLastSaw(t *testing.T) {
	h, _ := script(t, `503 {"error":"service_unavailable"}`)
	c := computeClient(t, h)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := c.Sessions().WaitReady(ctx, "s-1", fast)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "service_unavailable") {
		t.Fatalf("got %v", err)
	}
}

func TestWaitProvisioned(t *testing.T) {
	vol := func(state, op, extra string) string {
		return fmt.Sprintf(`{"id":"v-1","state":%q,"operation":%q%s}`, state, op, extra)
	}
	h, calls := script(t, vol("submitted", "provisioning", ""), vol("provisioned", "resizing", ""), vol("provisioned", "", ""))
	c := computeClient(t, h)
	v, err := c.Volumes().WaitProvisioned(context.Background(), "v-1", fast)
	if err != nil || v.State != VolumeProvisioned || calls() != 3 {
		t.Fatalf("got %+v, %v after %d calls", v, err, calls())
	}

	h, _ = script(t, vol("failed", "", `,"reason":"quota exceeded at vendor"`))
	c = computeClient(t, h)
	_, err = c.Volumes().WaitProvisioned(context.Background(), "v-1", fast)
	var failed *VolumeFailedError
	if !errors.As(err, &failed) || !strings.Contains(err.Error(), "quota exceeded at vendor") {
		t.Fatalf("got %v", err)
	}
}
