package rpc

// Running commands in a session and copying files (RC-473). Each call opens
// its own connection through the gateway: rpc answers one request per
// process, so there is no connection to keep between calls.

import (
	"bytes"
	"encoding/base64"

	"github.com/mimiry-com/mimiry-sdk/mimiry"
)

// execOutputLimit is the most of each output stream returned inline. More is
// cut, and the response says so: a command with large output writes it to a
// file and the caller downloads it.
const execOutputLimit = 8 << 20

type execParams struct {
	ID   string   `json:"id"`
	Argv []string `json:"argv"`
	// Stdin is base64; empty sends nothing.
	Stdin string `json:"stdin,omitzero"`
}

type execResult struct {
	ExitStatus      int    `json:"exit_status"`
	Stdout          string `json:"stdout"` // base64
	Stderr          string `json:"stderr"` // base64
	StdoutTruncated bool   `json:"stdout_truncated,omitzero"`
	StderrTruncated bool   `json:"stderr_truncated,omitzero"`
}

func sessionsExec(cl *call) (any, error) {
	var p execParams
	if err := cl.decodeID(&p, &p.ID); err != nil {
		return nil, err
	}
	if len(p.Argv) == 0 || p.Argv[0] == "" {
		return nil, &usageError{"params: argv is required: the program and its arguments"}
	}
	stdin, err := base64.StdEncoding.DecodeString(p.Stdin)
	if err != nil {
		return nil, &usageError{"params: stdin is not base64: " + err.Error()}
	}
	x, err := cl.c.Sessions().Dial(cl.ctx, p.ID, mimiry.DialOptions{})
	if err != nil {
		return nil, err
	}
	defer x.Close()
	var out, errOut capped
	code, err := x.Exec(cl.ctx, p.Argv, mimiry.ExecIO{Stdin: bytes.NewReader(stdin), Stdout: &out, Stderr: &errOut})
	if err != nil {
		return nil, err
	}
	return execResult{
		ExitStatus:      code,
		Stdout:          base64.StdEncoding.EncodeToString(out.b.Bytes()),
		Stderr:          base64.StdEncoding.EncodeToString(errOut.b.Bytes()),
		StdoutTruncated: out.cut,
		StderrTruncated: errOut.cut,
	}, nil
}

// capped keeps the first execOutputLimit bytes written to it and notes
// whether there were more. It never fails a write, so the command is not
// stopped by its own output.
type capped struct {
	b   bytes.Buffer
	cut bool
}

func (c *capped) Write(p []byte) (int, error) {
	room := execOutputLimit - c.b.Len()
	if len(p) > room {
		c.cut = true
		c.b.Write(p[:max(room, 0)])
		return len(p), nil
	}
	return c.b.Write(p)
}

type copyParams struct {
	ID         string `json:"id"`
	LocalPath  string `json:"local_path"`
	RemotePath string `json:"remote_path"`
	// Overwrite lets a download replace an existing local file.
	Overwrite bool `json:"overwrite,omitzero"`
}

type copyResult struct {
	Bytes int64 `json:"bytes"`
}

func (p *copyParams) check() error {
	if p.LocalPath == "" || p.RemotePath == "" {
		return &usageError{"params: local_path and remote_path are required"}
	}
	return nil
}

func sessionsUpload(cl *call) (any, error) {
	var p copyParams
	if err := cl.decodeID(&p, &p.ID); err != nil {
		return nil, err
	}
	if err := p.check(); err != nil {
		return nil, err
	}
	if p.Overwrite {
		return nil, &usageError{"params: overwrite applies to downloads; an upload always replaces the file in the session"}
	}
	x, err := cl.c.Sessions().Dial(cl.ctx, p.ID, mimiry.DialOptions{})
	if err != nil {
		return nil, err
	}
	defer x.Close()
	n, err := x.UploadFile(cl.ctx, p.LocalPath, p.RemotePath)
	if err != nil {
		return nil, err
	}
	return copyResult{Bytes: n}, nil
}

func sessionsDownload(cl *call) (any, error) {
	var p copyParams
	if err := cl.decodeID(&p, &p.ID); err != nil {
		return nil, err
	}
	if err := p.check(); err != nil {
		return nil, err
	}
	x, err := cl.c.Sessions().Dial(cl.ctx, p.ID, mimiry.DialOptions{})
	if err != nil {
		return nil, err
	}
	defer x.Close()
	n, err := x.DownloadFile(cl.ctx, p.RemotePath, p.LocalPath, p.Overwrite)
	if err != nil {
		return nil, err
	}
	return copyResult{Bytes: n}, nil
}
