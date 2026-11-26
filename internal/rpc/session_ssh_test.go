package rpc

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSessionDataPlaneParamsAreCheckedBeforeAnyConnection(t *testing.T) {
	cases := map[string]string{
		"exec without argv":          `{"v":2,"op":"sessions.exec","params":{"id":"s-1"}}`,
		"exec with an empty program": `{"v":2,"op":"sessions.exec","params":{"id":"s-1","argv":[""]}}`,
		"exec stdin not base64":      `{"v":2,"op":"sessions.exec","params":{"id":"s-1","argv":["cat"],"stdin":"%%%"}}`,
		"upload without a path":      `{"v":2,"op":"sessions.upload","params":{"id":"s-1","local_path":"/etc/hostname"}}`,
		"upload with overwrite":      `{"v":2,"op":"sessions.upload","params":{"id":"s-1","local_path":"/a","remote_path":"/b","overwrite":true}}`,
		"download without a path":    `{"v":2,"op":"sessions.download","params":{"id":"s-1","remote_path":"/b"}}`,
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			f := api(t, func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("called %s %s for a request that should have been refused", r.Method, r.URL.Path)
			})
			code, lines := serve(t, f, req)
			if e := errOf(t, last(lines)); code != 1 || e["kind"] != "usage" {
				t.Fatalf("code %d, error %v", code, e)
			}
		})
	}
}

func TestSessionWithoutAHostKeyIsNotReachable(t *testing.T) {
	f := api(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sessions/s-1") {
			t.Errorf("called %s %s: only the session may be read", r.Method, r.URL.Path)
		}
		io.WriteString(w, `{"id":"s-1","state":"running","ssh_enabled":true,"ssh":{"host":"1.2.3.4","port":22,"username":"root"}}`)
	})
	_, lines := serve(t, f, `{"v":2,"op":"sessions.exec","params":{"id":"s-1","argv":["true"]}}`)
	if e := errOf(t, last(lines)); e["kind"] != "session" || e["code"] != "not_reachable" {
		t.Fatalf("error %v", e)
	}
}

func TestCappedKeepsTheStartAndSaysItCut(t *testing.T) {
	var c capped
	big := strings.Repeat("x", execOutputLimit-3)
	c.Write([]byte(big))
	if n, err := c.Write([]byte("abcdef")); n != 6 || err != nil {
		t.Fatalf("a write past the limit failed: %d %v", n, err)
	}
	c.Write([]byte("more"))
	if !c.cut || c.b.Len() != execOutputLimit || !strings.HasSuffix(c.b.String(), "abc") {
		t.Fatalf("cut %v, kept %d", c.cut, c.b.Len())
	}
}
