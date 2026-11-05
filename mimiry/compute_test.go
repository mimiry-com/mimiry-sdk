package mimiry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// computeClient returns a signed-in client whose API answers with h.
func computeClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	key := genKey(t)
	api, srv := newFakeAPI(t, key)
	api.handler = h
	return newTestClient(t, srv, key, nil)
}

// A session as api-compute serialises it (models.SessionResponse).
const sessionJSON = `{
  "id":"s-1","name":"train","org_id":"o-1","state":"running","operation":"",
  "created_at":"2026-10-28T09:00:00Z","updated_at":"2026-10-28T09:02:00Z",
  "started_at":"2026-10-28T09:01:30Z",
  "ssh":{"host":"1.2.3.4","port":22,"username":"root"},"ssh_enabled":true,
  "auto_terminate":{"mode":"on_complete"},"result_storage":"script_managed",
  "gpu_type":"H100_80G_SXM","gpu_count":1,
  "billing":{"hourly_rate":2.5,"currency":"EUR","billed_from":"2026-10-28T09:00:40Z",
             "running_cost":0.1,"duration_seconds":144},
  "events":[{"timestamp":"2026-10-28T09:01:30Z","event":"started","message":"ok"}],
  "events_total":7
}`

func TestSessionDecodesTheServersShape(t *testing.T) {
	c := computeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/compute/v1/sessions/s-1" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("events_tail"); got != "10" {
			t.Errorf("events_tail=%q", got)
		}
		io.WriteString(w, sessionJSON)
	})
	ten := 10
	s, err := c.Sessions().Get(context.Background(), "s-1", GetSessionOptions{EventsTail: &ten})
	if err != nil {
		t.Fatal(err)
	}
	if s.State != SessionRunning || s.SSH.Host != "1.2.3.4" || *s.GPUType != "H100_80G_SXM" ||
		s.Billing.BilledFrom == nil || s.Billing.Provider != nil || s.Billing.FinalCost != nil ||
		s.EventsTotal != 7 || len(s.Events) != 1 || s.AutoTerminate.Mode != AutoTerminateOnComplete {
		t.Fatalf("decoded %+v", s)
	}
}

func TestSessionListEncodesFiltersAndPaging(t *testing.T) {
	c := computeClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		want := map[string]string{
			"state": "running,idle", "operation_not": "terminating", "org_id": "o-1",
			"updated_after": "2026-10-28T08:00:00Z", "limit": "20", "offset": "40",
		}
		for k, v := range want {
			if q.Get(k) != v {
				t.Errorf("%s=%q, want %q", k, q.Get(k), v)
			}
		}
		for _, k := range []string{"state_not", "operation", "updated_before"} {
			if q.Has(k) {
				t.Errorf("%s sent although unset", k)
			}
		}
		io.WriteString(w, `{"sessions":null,"total_count":0,"limit":20,"offset":40,"unavailable":2}`)
	})
	p, err := c.Sessions().List(context.Background(), SessionListOptions{
		ListOptions:  ListOptions{Limit: 20, Offset: 40},
		State:        []SessionState{SessionRunning, SessionIdle},
		OperationNot: []string{"terminating"},
		OrgID:        "o-1",
		UpdatedAfter: time.Date(2026, 10, 28, 9, 0, 0, 0, time.FixedZone("CET", 3600)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Items == nil || len(p.Items) != 0 || p.Unavailable != 2 || p.Limit != 20 || p.Offset != 40 {
		t.Fatalf("page %+v", p)
	}
}

// All walks every page at the largest size and stops at total_count.
func TestAllWalksEveryPage(t *testing.T) {
	const total = 230
	var calls atomic.Int32
	c := computeClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		if limit != 100 {
			t.Errorf("limit=%d, want 100", limit)
		}
		var items []string
		for i := offset; i < total && i < offset+limit; i++ {
			items = append(items, fmt.Sprintf(`{"id":"v-%d"}`, i))
		}
		fmt.Fprintf(w, `{"volumes":[%s],"total_count":%d,"limit":%d,"offset":%d,"unavailable":0}`,
			strings.Join(items, ","), total, limit, offset)
	})
	seen := 0
	for v, err := range c.Volumes().All(context.Background(), VolumeListOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		if v.ID != fmt.Sprintf("v-%d", seen) {
			t.Fatalf("item %d is %s", seen, v.ID)
		}
		seen++
	}
	if seen != total || calls.Load() != 3 {
		t.Fatalf("saw %d items in %d calls, want %d in 3", seen, calls.Load(), total)
	}
}

func TestAllStopsWhenTheCallerDoes(t *testing.T) {
	var calls atomic.Int32
	c := computeClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, `{"transactions":[{"id":"a"},{"id":"b"}],"total_count":1000,"limit":100,"offset":0}`)
	})
	for range c.Account().AllTransactions(context.Background(), TransactionListOptions{}) {
		break
	}
	if calls.Load() != 1 {
		t.Fatalf("%d calls after stopping at the first item", calls.Load())
	}
}

func TestAvailabilityQueryAndNullAnswer(t *testing.T) {
	c := computeClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		want := map[string]string{
			"gpu_family": "H100,A100", "min_vram_gb": "40", "max_hourly_rate": "2.75",
			"available_only": "false", "detail": "full", "include_cpu": "true",
		}
		for k, v := range want {
			if q.Get(k) != v {
				t.Errorf("%s=%q, want %q", k, q.Get(k), v)
			}
		}
		if q.Has("min_hourly_rate") || q.Has("location") {
			t.Error("unset filter sent")
		}
		io.WriteString(w, `{"gpu_models":null}`)
	})
	rate := 2.75
	got, err := c.GPUs().Availability(context.Background(), AvailabilityOptions{
		Families: []string{"H100", "A100"}, MinVRAMGB: 40, MaxHourlyRate: &rate,
		IncludeUnavailable: true, Full: true, IncludeCPU: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("got %#v, want an empty non-nil slice", got)
	}
}

// The default query leaves every server default alone: no params at all.
func TestAvailabilityDefaultsSendNothing(t *testing.T) {
	c := computeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Errorf("query %q, want none", r.URL.RawQuery)
		}
		io.WriteString(w, `{"gpu_models":[{"name":"H100_80G_SXM","providers":[{"hourly_rate":2.1,"locations":["FI-1"],"available":true}],"skus":[{"sku":"H100-HOPPER-1x","architecture":"HOPPER","listings":[{"hourly_rate":2.1,"available":true}]}]}]}`)
	})
	got, err := c.GPUs().Availability(context.Background(), AvailabilityOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Providers[0].Provider != nil || got[0].SKUs[0].Architecture != "HOPPER" {
		t.Fatalf("got %+v", got)
	}
}

func TestMutationsUseTheRightMethodAndBody(t *testing.T) {
	type seen struct{ method, path, body string }
	var mu sync.Mutex
	var calls []seen
	c := computeClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, seen{r.Method, r.URL.Path, string(b)})
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"id":"x","status":"pending","message":"m","status_url":"https://h/x"}`)
	})
	ctx := context.Background()
	cmd := "nvidia-smi"
	if _, err := c.Sessions().Create(ctx, CreateSessionRequest{Name: "n", Image: Image{URI: "docker.io/x/y"},
		GPU: GPUSpec{Types: []string{"H100"}}, Command: &cmd}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Sessions().Terminate(ctx, "s 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Volumes().Extend(ctx, "v-1", 200); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Volumes().Delete(ctx, "v-1"); err != nil {
		t.Fatal(err)
	}
	want := []seen{
		{"POST", "/api/compute/v1/sessions", `{"name":"n","image":{"uri":"docker.io/x/y"},"gpu":{"types":["H100"]},"command":"nvidia-smi"}`},
		{"DELETE", "/api/compute/v1/sessions/s 1", ""},
		{"PATCH", "/api/compute/v1/volumes/v-1", `{"size_gb":200}`},
		{"DELETE", "/api/compute/v1/volumes/v-1", ""},
	}
	if len(calls) != len(want) {
		t.Fatalf("calls %+v", calls)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Errorf("call %d: %+v, want %+v", i, calls[i], want[i])
		}
	}
}

func TestErrorDetailsSurvive(t *testing.T) {
	body := `{"error":"insufficient_balance","message":"Need 2.50 EUR","required":2.5,"available":0.4,"currency":"EUR"}`
	c := computeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/logs") {
			w.WriteHeader(503)
			io.WriteString(w, `{"error":"logs_unavailable","message":"try later","retry_after_seconds":5}`)
			return
		}
		if r.Method == http.MethodPost {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"validation_failed","message":"Request validation failed","errors":[{"field":"gpu","message":"at least one selector"}]}`)
			return
		}
		w.WriteHeader(402)
		io.WriteString(w, body)
	})
	ctx := context.Background()

	_, err := c.Sessions().Get(ctx, "s", GetSessionOptions{})
	var e *APIError
	if !errors.As(err, &e) || e.ErrorCode != "insufficient_balance" {
		t.Fatalf("got %v", err)
	}
	var detail struct{ Required, Available float64 }
	if json.Unmarshal(e.Raw, &detail) != nil || detail.Required != 2.5 || detail.Available != 0.4 {
		t.Fatalf("details lost: %s", e.Raw)
	}

	_, err = c.Sessions().Create(ctx, CreateSessionRequest{Name: "n"})
	if !errors.As(err, &e) || len(e.Errors) != 1 || e.Errors[0].Field != "gpu" {
		t.Fatalf("got %#v", err)
	}

	_, err = c.Sessions().Logs(ctx, "s", LogsOptions{})
	if !errors.As(err, &e) || e.RetryAfterSeconds != 5 {
		t.Fatalf("got %#v", err)
	}
}

func TestIsNotFound(t *testing.T) {
	c := computeClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		io.WriteString(w, `{"error":"not_found","message":"Session not found"}`)
	})
	_, err := c.Sessions().Get(context.Background(), "nope", GetSessionOptions{})
	if !IsNotFound(err) {
		t.Fatalf("IsNotFound(%v) = false", err)
	}
	if IsNotFound(errors.New("other")) {
		t.Fatal("IsNotFound(non-API error) = true")
	}
}
