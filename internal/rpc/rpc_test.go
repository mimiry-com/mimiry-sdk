package rpc

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/mimiry-com/mimiry-sdk/mimiry"
)

// api is a stand-in for the Mimiry API. Sign-in itself is tested in the
// library; here the token endpoint just answers, and h serves the rest.
func api(t *testing.T, h http.HandlerFunc) ClientFactory {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/token" {
			io.WriteString(w, `{"access_token":"tok","expires_in":300,"user_id":"u-1"}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	_, key, _ := ed25519.GenerateKey(nil)
	signer, _ := ssh.NewSignerFromKey(key)
	return func(host, keyPath string) (*mimiry.Client, error) {
		return mimiry.New(mimiry.Options{Host: srv.URL, Signer: signer})
	}
}

// serve runs one request and returns the exit code and every line written.
func serve(t *testing.T, factory ClientFactory, req string) (int, []map[string]any) {
	t.Helper()
	var out bytes.Buffer
	code := Serve(context.Background(), strings.NewReader(req), &out, factory)
	var lines []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("output line is not JSON: %q", l)
		}
		lines = append(lines, m)
	}
	return code, lines
}

func last(lines []map[string]any) map[string]any { return lines[len(lines)-1] }

func errOf(t *testing.T, resp map[string]any) map[string]any {
	t.Helper()
	if resp["ok"] != false {
		t.Fatalf("response %v is not an error", resp)
	}
	e, _ := resp["error"].(map[string]any)
	return e
}

func TestDescribeNeedsNoClient(t *testing.T) {
	code, lines := serve(t, func(string, string) (*mimiry.Client, error) {
		t.Fatal("describe built a client")
		return nil, nil
	}, `{"v":1,"op":"describe"}`)
	r := last(lines)
	if code != 0 || r["ok"] != true {
		t.Fatalf("code %d, %v", code, r)
	}
	res := r["result"].(map[string]any)
	if res["protocol"].(float64) != Protocol {
		t.Fatalf("protocol %v", res["protocol"])
	}
	ops := res["ops"].([]any)
	if len(ops) < 20 {
		t.Fatalf("ops %v", ops)
	}
}

func TestListPassesFiltersThroughAndAnswersAPage(t *testing.T) {
	f := api(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/compute/v1/sessions" || r.URL.Query().Get("state") != "running,idle" || r.URL.Query().Get("limit") != "5" {
			t.Errorf("got %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		io.WriteString(w, `{"sessions":[{"id":"s-1","state":"running"}],"total_count":9,"limit":5,"offset":0,"unavailable":1}`)
	})
	code, lines := serve(t, f, `{"v":1,"op":"sessions.list","params":{"state":["running","idle"],"limit":5}}`)
	r := last(lines)
	if code != 0 {
		t.Fatalf("code %d: %v", code, r)
	}
	res := r["result"].(map[string]any)
	items := res["items"].([]any)
	if len(items) != 1 || res["total_count"].(float64) != 9 || res["unavailable"].(float64) != 1 {
		t.Fatalf("result %v", res)
	}
}

func TestAllCollectsEveryPage(t *testing.T) {
	var calls atomic.Int32
	f := api(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			io.WriteString(w, `{"transactions":[{"id":"a"},{"id":"b"}],"total_count":3,"limit":100,"offset":0}`)
			return
		}
		io.WriteString(w, `{"transactions":[{"id":"c"}],"total_count":3,"limit":100,"offset":2}`)
	})
	code, lines := serve(t, f, `{"v":1,"op":"account.transactions","params":{"all":true}}`)
	res := last(lines)["result"].(map[string]any)
	if code != 0 || len(res["items"].([]any)) != 3 || res["total_count"].(float64) != 3 {
		t.Fatalf("code %d, result %v", code, res)
	}
}

// A misspelt filter is refused, not ignored: ignored, it matches everything.
func TestUnknownParamIsAUsageError(t *testing.T) {
	f := api(t, func(w http.ResponseWriter, r *http.Request) { t.Error("the API was called") })
	code, lines := serve(t, f, `{"v":1,"op":"sessions.list","params":{"sate":["running"]}}`)
	e := errOf(t, last(lines))
	if code != 1 || e["kind"] != "usage" || !strings.Contains(e["message"].(string), "sate") {
		t.Fatalf("code %d, error %v", code, e)
	}
}

func TestRequestShapeErrors(t *testing.T) {
	f := api(t, func(w http.ResponseWriter, r *http.Request) {})
	for _, tc := range []struct {
		req  string
		code int
		want string
	}{
		{`not json`, 2, "not valid JSON"},
		{`{"v":1,"op":"x","bogus":1}`, 2, "bogus"},
		{`{"v":2,"op":"describe"}`, 1, "protocol version 2"},
		{`{"v":1,"op":"sessions.explode"}`, 1, "unknown op"},
		{`{"v":1,"op":"sessions.get","params":{}}`, 1, "id is required"},
	} {
		code, lines := serve(t, f, tc.req)
		e := errOf(t, last(lines))
		if code != tc.code || e["kind"] != "usage" || !strings.Contains(e["message"].(string), tc.want) {
			t.Errorf("%s: code %d, error %v", tc.req, code, e)
		}
	}
}

func TestAPIErrorKeepsStatusCodeAndDetails(t *testing.T) {
	f := api(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(402)
		io.WriteString(w, `{"error":"insufficient_balance","message":"Need 2.50 EUR","required":2.5,"available":0.4}`)
	})
	code, lines := serve(t, f, `{"v":1,"op":"sessions.create","params":{"name":"n","image":{"uri":"docker.io/x/y"},"gpu":{"types":["H100"]}}}`)
	e := errOf(t, last(lines))
	if code != 1 || e["kind"] != "api" || e["status"].(float64) != 402 || e["code"] != "insufficient_balance" {
		t.Fatalf("code %d, error %v", code, e)
	}
	if d := e["details"].(map[string]any); d["required"].(float64) != 2.5 {
		t.Fatalf("details %v", d)
	}
}

func TestConfigAndNetworkErrorsAreTheirOwnKinds(t *testing.T) {
	code, lines := serve(t, func(string, string) (*mimiry.Client, error) { return nil, mimiry.ErrNoHost },
		`{"v":1,"op":"account.balance"}`)
	if e := errOf(t, last(lines)); code != 1 || e["kind"] != "config" || e["code"] != "no_host" {
		t.Fatalf("no host: %v", e)
	}

	_, key, _ := ed25519.GenerateKey(nil)
	signer, _ := ssh.NewSignerFromKey(key)
	dead := func(string, string) (*mimiry.Client, error) {
		return mimiry.New(mimiry.Options{Host: "http://127.0.0.1:1", Signer: signer})
	}
	code, lines = serve(t, dead, `{"v":1,"op":"account.balance"}`)
	if e := errOf(t, last(lines)); code != 1 || e["kind"] != "network" {
		t.Fatalf("unreachable: %v", e)
	}
}

// A wait writes a progress line per poll when asked, then the response; a
// failed wait carries the resource.
func TestWaitProgressAndFailure(t *testing.T) {
	var n atomic.Int32
	f := api(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) < 3 {
			io.WriteString(w, `{"id":"s-1","state":"booting","ssh_enabled":true}`)
			return
		}
		io.WriteString(w, `{"id":"s-1","state":"pull_failed","ssh_enabled":true,"error":"image not found"}`)
	})
	code, lines := serve(t, f, `{"v":1,"op":"sessions.wait_ready","params":{"id":"s-1","interval_seconds":0.001},"progress":true}`)
	if code != 1 || len(lines) != 3 {
		t.Fatalf("code %d, %d lines: %v", code, len(lines), lines)
	}
	for _, p := range lines[:2] {
		if p["progress"].(map[string]any)["state"] != "booting" {
			t.Fatalf("progress %v", p)
		}
	}
	e := errOf(t, last(lines))
	if e["kind"] != "failed" || e["resource"].(map[string]any)["state"] != "pull_failed" {
		t.Fatalf("error %v", e)
	}
}

func TestTimeoutIsItsOwnKind(t *testing.T) {
	f := api(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"v-1","state":"submitted","operation":"provisioning"}`)
	})
	code, lines := serve(t, f, `{"v":1,"op":"volumes.wait_provisioned","params":{"id":"v-1","interval_seconds":0.001},"timeout_seconds":0.05}`)
	if e := errOf(t, last(lines)); code != 1 || e["kind"] != "timeout" {
		t.Fatalf("code %d, error %v", code, e)
	}
}

func TestTokenAnswer(t *testing.T) {
	f := api(t, func(w http.ResponseWriter, r *http.Request) {})
	code, lines := serve(t, f, `{"v":1,"op":"auth.token"}`)
	res := last(lines)["result"].(map[string]any)
	if code != 0 || res["access_token"] != "tok" || res["user_id"] != "u-1" || res["expires_at"] == nil {
		t.Fatalf("code %d, result %v", code, res)
	}
}
