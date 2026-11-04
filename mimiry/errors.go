package mimiry

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"
)

// APIError is a response the API answered with a status of 400 or above.
//
// The server's own words are kept, not replaced: Message and ErrorCode are
// what it said, and Body is the raw response when it was not JSON (a proxy's
// HTML error page, an empty 502). A client that turns those into "request
// failed" throws away the part that says what to do next.
type APIError struct {
	StatusCode int
	Method     string
	Path       string

	// ErrorCode is the response's "error" field: a snake_case code such as
	// "gpu_unavailable". Branch on it, never on Message.
	ErrorCode string
	// Message is the response's "message" field: what went wrong, for people.
	Message string
	// Fields and Errors carry per-field validation messages, when present;
	// endpoints use one shape or the other.
	Fields map[string]string
	Errors []FieldError
	// RetryAfterSeconds is the server's hint for a temporary failure, or 0.
	RetryAfterSeconds int

	// Raw is the whole response body when it was JSON. Errors carry details
	// beyond the common fields — insufficient_balance has required and
	// available, quota_exceeded its limit — and Raw keeps them all.
	Raw json.RawMessage

	// Body is the raw response body, truncated, when it was not a JSON error.
	Body string
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s: HTTP %d", e.Method, e.Path, e.StatusCode)
	if e.ErrorCode != "" {
		b.WriteString(": ")
		b.WriteString(e.ErrorCode)
	}
	if e.Message != "" && e.Message != e.ErrorCode {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	if e.ErrorCode == "" && e.Message == "" {
		if e.Body != "" {
			b.WriteString(": ")
			b.WriteString(e.Body)
		} else {
			b.WriteString(" with an empty body")
		}
	}
	return b.String()
}

const maxErrorBody = 300

func newAPIError(method, path string, status int, body []byte) *APIError {
	e := &APIError{StatusCode: status, Method: method, Path: path}
	var parsed struct {
		Error             string            `json:"error"`
		Message           string            `json:"message"`
		Fields            map[string]string `json:"fields"`
		Errors            []FieldError      `json:"errors"`
		RetryAfterSeconds int               `json:"retry_after_seconds"`
	}
	if json.Unmarshal(body, &parsed) == nil && (parsed.Error != "" || parsed.Message != "") {
		e.ErrorCode = parsed.Error
		e.Message = parsed.Message
		e.Fields = parsed.Fields
		e.Errors = parsed.Errors
		e.RetryAfterSeconds = parsed.RetryAfterSeconds
		e.Raw = json.RawMessage(body)
		return e
	}
	e.Body = truncate(strings.Join(strings.Fields(string(body)), " "), maxErrorBody)
	return e
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// FieldError is one field's validation failure.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// IsNotFound reports whether err is a 404 from the API. The API answers 404
// both for what does not exist and for what is not the caller's.
func IsNotFound(err error) bool { return hasStatus(err, http.StatusNotFound) }

func hasStatus(err error, status int) bool {
	var e *APIError
	return errors.As(err, &e) && e.StatusCode == status
}

// TransportError is a request that got no HTTP answer: DNS, a refused
// connection, TLS, a timeout. It is not an API answer, and is retryable in a
// way most API answers are not.
type TransportError struct {
	Method  string
	Path    string
	BaseURL string
	Err     error
}

func (e *TransportError) Error() string {
	return fmt.Sprintf("mimiry: %s %s: could not reach %s: %v", e.Method, e.Path, e.BaseURL, e.Err)
}

func (e *TransportError) Unwrap() error { return e.Err }
