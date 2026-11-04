// Package rpc is the machine interface of the mimiry binary: one JSON request
// on stdin, one JSON response on stdout. It is the contract the Python package
// is built on, and the only one — Python never reads the human CLI's output.
// PROTOCOL.md at the repository root specifies it; this package implements it.
//
// Every operation is a thin adapter over the library: decode the params into
// the library's own option types, call, encode the result. Behaviour lives in
// the library, so a fix there reaches every language without touching this.
package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/mimiry-com/mimiry-sdk/mimiry"
)

// Protocol is the version of the request and response shapes. It changes only
// on an incompatible change; adding an operation or a field does not change it.
const Protocol = 2

// Request is one call.
type Request struct {
	V      int             `json:"v"`
	Op     string          `json:"op"`
	Params json.RawMessage `json:"params,omitempty"`
	// Host and Key override MIMIRY_HOST / MIMIRY_SSH_KEY and the saved config.
	Host string `json:"host,omitempty"`
	Key  string `json:"key,omitempty"`
	// TimeoutSeconds bounds the whole call. Zero means no bound.
	TimeoutSeconds float64 `json:"timeout_seconds,omitempty"`
	// Progress asks for a progress line per poll during a wait.
	Progress bool `json:"progress,omitempty"`
}

// Response is the last line written for every request.
type Response struct {
	V      int    `json:"v"`
	OK     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	Error  *Error `json:"error,omitempty"`
}

// Progress is a line written before the Response during a wait, when asked for.
type Progress struct {
	V         int    `json:"v"`
	Progress  any    `json:"progress"`
	Transient string `json:"transient,omitempty"`
}

// Error says why a call failed. Kind is the first thing to branch on:
//
//	usage     the request itself is wrong: unknown op, bad params, bad version
//	config    no host, no key, a locked or unusable key, a host that is not a host
//	api       the API answered with an error; Status and Code are its own
//	network   no answer from the API at all
//	timeout   the call's timeout ended it
//	failed    a wait ended in failure; Resource is the session or volume
//	internal  anything else — a bug
type Error struct {
	Kind              string              `json:"kind"`
	Message           string              `json:"message"`
	Status            int                 `json:"status,omitempty"`
	Code              string              `json:"code,omitempty"`
	Fields            map[string]string   `json:"fields,omitempty"`
	Errors            []mimiry.FieldError `json:"errors,omitempty"`
	RetryAfterSeconds int                 `json:"retry_after_seconds,omitempty"`
	Details           json.RawMessage     `json:"details,omitempty"`
	Resource          any                 `json:"resource,omitempty"`
}

// ClientFactory builds the client a request runs against.
type ClientFactory func(host, key string) (*mimiry.Client, error)

// DefaultClientFactory resolves host and key the way every Mimiry tool does.
// A locked key has no one to ask for a passphrase here: it must be in ssh-agent.
func DefaultClientFactory(host, key string) (*mimiry.Client, error) {
	o, err := mimiry.ResolveOptions(host, key)
	if err != nil {
		return nil, err
	}
	o.UserAgent = "mimiry-rpc/" + fmt.Sprint(Protocol)
	return mimiry.New(o)
}

// Serve handles one request from in and writes to out. It returns the process
// exit code: 0 for success, 1 for an error response, 2 when the input was not
// a request at all. A response is written in every case.
func Serve(ctx context.Context, in io.Reader, out io.Writer, newClient ClientFactory) int {
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)

	var req Request
	dec := json.NewDecoder(in)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		enc.Encode(Response{V: Protocol, Error: &Error{Kind: "usage", Message: "request is not valid JSON: " + err.Error()}})
		return 2
	}
	if req.V != Protocol {
		enc.Encode(Response{V: Protocol, Error: &Error{Kind: "usage",
			Message: fmt.Sprintf("protocol version %d is not supported; this binary speaks %d", req.V, Protocol)}})
		return 1
	}

	result, err := dispatch(ctx, req, enc, newClient)
	if err != nil {
		enc.Encode(Response{V: Protocol, Error: classify(err)})
		return 1
	}
	enc.Encode(Response{V: Protocol, OK: true, Result: result})
	return 0
}

// usageError is a request this package refused.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func dispatch(ctx context.Context, req Request, enc *json.Encoder, newClient ClientFactory) (any, error) {
	if req.Op == "describe" {
		return describe(), nil
	}
	op, ok := ops[req.Op]
	if !ok {
		return nil, &usageError{fmt.Sprintf("unknown op %q (op \"describe\" lists them)", req.Op)}
	}

	if req.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutSeconds*float64(time.Second)))
		defer cancel()
	}
	c, err := newClient(req.Host, req.Key)
	if err != nil {
		return nil, err
	}
	call := &call{ctx: ctx, c: c, params: req.Params}
	if req.Progress {
		call.progress = func(res any, transient error) {
			p := Progress{V: Protocol, Progress: res}
			if transient != nil {
				p.Transient = transient.Error()
			}
			enc.Encode(p)
		}
	}
	return op(call)
}

// call is what an operation gets.
type call struct {
	ctx      context.Context
	c        *mimiry.Client
	params   json.RawMessage
	progress func(any, error)
}

// decode reads the params into v, refusing unknown fields: a misspelt filter
// silently ignored is a filter that matches everything.
func (cl *call) decode(v any) error {
	if len(cl.params) == 0 || string(cl.params) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(cl.params))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return &usageError{"params: " + err.Error()}
	}
	return nil
}

func classify(err error) *Error {
	var (
		usage     *usageError
		apiErr    *mimiry.APIError
		transport *mimiry.TransportError
		sFailed   *mimiry.SessionFailedError
		vFailed   *mimiry.VolumeFailedError
		oFailed   *mimiry.OperationFailedError
	)
	e := &Error{Message: err.Error()}
	switch {
	case errors.As(err, &usage):
		e.Kind = "usage"
	case errors.As(err, &sFailed):
		e.Kind, e.Resource = "failed", sFailed.Session
	case errors.As(err, &vFailed):
		e.Kind, e.Resource = "failed", vFailed.Volume
	case errors.As(err, &oFailed):
		e.Kind, e.Resource = "failed", oFailed.Operation
	case errors.Is(err, context.DeadlineExceeded):
		e.Kind = "timeout"
	case errors.As(err, &apiErr):
		e.Kind = "api"
		e.Status = apiErr.StatusCode
		e.Code = apiErr.ErrorCode
		e.Fields = apiErr.Fields
		e.Errors = apiErr.Errors
		e.RetryAfterSeconds = apiErr.RetryAfterSeconds
		e.Details = apiErr.Raw
	case errors.As(err, &transport):
		e.Kind = "network"
	case errors.Is(err, mimiry.ErrNoHost):
		e.Kind, e.Code = "config", "no_host"
	case errors.Is(err, mimiry.ErrNoKey):
		e.Kind, e.Code = "config", "no_key"
	case errors.Is(err, mimiry.ErrKeyLocked):
		e.Kind, e.Code = "config", "key_locked"
	case errors.Is(err, mimiry.ErrInvalidHost):
		e.Kind, e.Code = "config", "invalid_host"
	case errors.Is(err, mimiry.ErrKeyUnavailable):
		e.Kind, e.Code = "config", "key_unavailable"
	default:
		e.Kind = "internal"
	}
	return e
}

func describe() any {
	names := make([]string, 0, len(ops)+1)
	for n := range ops {
		names = append(names, n)
	}
	names = append(names, "describe")
	sort.Strings(names)
	elevated := make([]string, 0, len(elevatedOps))
	for n := range elevatedOps {
		elevated = append(elevated, n)
	}
	sort.Strings(elevated)
	return map[string]any{"protocol": Protocol, "version": mimiry.Version, "ops": names, "elevated": elevated}
}
