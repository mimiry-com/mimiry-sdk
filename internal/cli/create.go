package cli

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mimiry-com/mimiry-sdk/mimiry"
)

// multi is a flag that may be given more than once.
type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

// ---- session create ----

func sessionCreateFlags(fs *flag.FlagSet) {
	fs.String("name", "", "session name (required)")
	fs.String("gpu", "", "comma-separated GPU types, in order of preference")
	fs.String("family", "", "comma-separated GPU families")
	fs.String("vram", "", "comma-separated GPU memory sizes to accept, GB")
	fs.String("location", "", "preferred location (a hint; see 'mimiry availability')")
	fs.Float64("max-rate", 0, "maximum price, EUR per hour")
	fs.Float64("min-rate", 0, "minimum price, EUR per hour")
	fs.Int("min-memory", 0, "minimum host memory, GB")
	fs.Int("min-vcpus", 0, "minimum vCPUs")
	fs.String("command", "", "command to run in the container")
	fs.Var(&multi{}, "env", "environment variable NAME=VALUE (repeatable)")
	fs.Var(&multi{}, "volume", "mount a volume, NAME:PATH (repeatable)")
	fs.String("org", "", "organisation ID the session belongs to (see 'Organisations' below)")
	fs.String("ssh-key-id", "", "ID of a registered SSH key that may log in")
	fs.String("ssh-public-key", "", "file holding the public key that may log in")
	fs.Bool("no-ssh", false, "no SSH access to the session")
	fs.String("auto-terminate", "", "on_complete (default), on_success or never")
	fs.Duration("max-duration", 0, "end the session after this long (default: the server's)")
	fs.Bool("wait", false, "wait until the session is ready, then show it")
	fs.Duration("timeout", 30*time.Minute, "with --wait, give up after this long")
}

const sessionCreateHelp = `
Without --ssh-key-id or --ssh-public-key, the key you sign in with is the one
that may log in to the session. At least one of --gpu, --family, --vram,
--max-rate or --min-rate is needed to choose a machine.

Organisations: a session that mounts volumes belongs to the organisation that
owns them; without volumes it is your own. Pass --org with --volume when you
belong to more than one organisation.`

func runSessionCreate(e *env, fs *flag.FlagSet, args []string) error {
	if len(args) != 1 || args[0] == "" {
		return usage("expected exactly one image, e.g. nvcr.io/nvidia/pytorch:24.01-py3")
	}
	req, err := sessionRequest(fs, args[0])
	if err != nil {
		return err
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	if req.SSHEnabled == nil && req.SSHKeyID == nil && req.SSHPublicKey == nil {
		if k := c.AuthorizedKey(); k != "" {
			req.SSHPublicKey = &k
		}
	}
	a, err := c.Sessions().Create(e.ctx, req)
	if err != nil {
		return err
	}
	if !boolFlag(fs, "wait") {
		if e.json {
			return e.printJSON(a)
		}
		fmt.Fprintf(e.stdout, "%s: %s\nFollow it with 'mimiry session wait %s'.\n", a.ID, a.Message, a.ID)
		return nil
	}
	fmt.Fprintf(e.stderr, "Session %s submitted.\n", a.ID)
	return waitSession(e, c, a.ID, durationFlag(fs, "timeout"))
}

// sessionRequest builds the create body from the flags, refusing what the
// server would refuse anyway when the CLI can say why more plainly.
func sessionRequest(fs *flag.FlagSet, image string) (mimiry.CreateSessionRequest, error) {
	req := mimiry.CreateSessionRequest{
		Name:  fs.Lookup("name").Value.String(),
		Image: mimiry.Image{URI: image},
		GPU: mimiry.GPUSpec{
			Types:  csv(fs, "gpu"),
			Family: csv(fs, "family"),
		},
	}
	if req.Name == "" {
		return req, usage("--name is required")
	}
	for _, v := range csv(fs, "vram") {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return req, usage(fmt.Sprintf("--vram: %q is not a size in GB", v))
		}
		req.GPU.VRAMGB = append(req.GPU.VRAMGB, n)
	}
	if l := fs.Lookup("location").Value.String(); l != "" {
		req.GPU.Location = &l
	}
	if r := floatFlag(fs, "max-rate"); r > 0 {
		req.GPU.MaxHourlyRate = &r
	}
	if r := floatFlag(fs, "min-rate"); r > 0 {
		req.GPU.MinHourlyRate = &r
	}
	if n := intFlag(fs, "min-memory"); n > 0 {
		req.GPU.MinMemoryGB = &n
	}
	if n := intFlag(fs, "min-vcpus"); n > 0 {
		req.GPU.MinVCPUs = &n
	}
	if len(req.GPU.Types)+len(req.GPU.Family)+len(req.GPU.VRAMGB) == 0 &&
		req.GPU.MaxHourlyRate == nil && req.GPU.MinHourlyRate == nil {
		return req, usage("choose a machine with --gpu, --family, --vram, --max-rate or --min-rate")
	}

	if cmd := fs.Lookup("command").Value.String(); cmd != "" {
		req.Command = &cmd
	}
	for _, kv := range *fs.Lookup("env").Value.(*multi) {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return req, usage(fmt.Sprintf("--env: %q is not NAME=VALUE", kv))
		}
		if req.EnvironmentVars == nil {
			req.EnvironmentVars = map[string]string{}
		}
		req.EnvironmentVars[k] = v
	}
	for _, m := range *fs.Lookup("volume").Value.(*multi) {
		name, path, ok := strings.Cut(m, ":")
		if !ok || name == "" || !strings.HasPrefix(path, "/") {
			return req, usage(fmt.Sprintf("--volume: %q is not NAME:/absolute/path", m))
		}
		req.VolumeMounts = append(req.VolumeMounts, mimiry.VolumeMount{VolumeName: name, MountPath: path})
	}
	if org := fs.Lookup("org").Value.String(); org != "" {
		req.OrgID = &org
	}

	keyID := fs.Lookup("ssh-key-id").Value.String()
	keyFile := fs.Lookup("ssh-public-key").Value.String()
	switch {
	case boolFlag(fs, "no-ssh"):
		if keyID != "" || keyFile != "" {
			return req, usage("--no-ssh cannot be combined with --ssh-key-id or --ssh-public-key")
		}
		off := false
		req.SSHEnabled = &off
	case keyID != "" && keyFile != "":
		return req, usage("give --ssh-key-id or --ssh-public-key, not both")
	case keyID != "":
		req.SSHKeyID = &keyID
	case keyFile != "":
		b, err := os.ReadFile(keyFile)
		if err != nil {
			return req, fmt.Errorf("reading --ssh-public-key: %w", err)
		}
		k := strings.TrimSpace(string(b))
		if strings.Contains(k, "PRIVATE KEY") {
			return req, usage("--ssh-public-key is a private key; pass the .pub file")
		}
		req.SSHPublicKey = &k
	}

	mode := fs.Lookup("auto-terminate").Value.String()
	switch mode {
	case "", mimiry.AutoTerminateOnComplete, mimiry.AutoTerminateOnSuccess, mimiry.AutoTerminateNever:
	default:
		return req, usage(fmt.Sprintf("--auto-terminate: %q is not on_complete, on_success or never", mode))
	}
	maxDur := durationFlag(fs, "max-duration")
	if maxDur < 0 {
		return req, usage("--max-duration must be positive")
	}
	if mode != "" || maxDur > 0 {
		if mode == "" {
			mode = mimiry.AutoTerminateOnComplete
		}
		req.AutoTerminate = &mimiry.AutoTerminate{Mode: mode, TimeoutSeconds: int64(maxDur / time.Second)}
	}
	return req, nil
}

// ---- volume create, extend, delete, wait ----

func volumeCreateFlags(fs *flag.FlagSet) {
	fs.String("name", "", "volume name (required)")
	fs.Int("size", 0, "size, GB (required)")
	fs.String("type", "", "nvme (default) or hdd")
	fs.String("location", "", "location; match the sessions that will mount it")
	fs.String("org", "", "organisation ID (needed when you belong to more than one)")
	fs.Bool("wait", false, "wait until the volume is ready, then show it")
	fs.Duration("timeout", 10*time.Minute, "with --wait, give up after this long")
}

func runVolumeCreate(e *env, fs *flag.FlagSet, args []string) error {
	if len(args) != 0 {
		return usage("takes no arguments; use --name and --size")
	}
	req := mimiry.CreateVolumeRequest{
		Name:       fs.Lookup("name").Value.String(),
		SizeGB:     intFlag(fs, "size"),
		VolumeType: fs.Lookup("type").Value.String(),
		Location:   fs.Lookup("location").Value.String(),
		OrgID:      fs.Lookup("org").Value.String(),
	}
	if req.Name == "" {
		return usage("--name is required")
	}
	if req.SizeGB <= 0 {
		return usage("--size is required, in GB")
	}
	if req.VolumeType != "" && req.VolumeType != "nvme" && req.VolumeType != "hdd" {
		return usage(fmt.Sprintf("--type: %q is not nvme or hdd", req.VolumeType))
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	a, err := c.Volumes().Create(e.ctx, req)
	if err != nil {
		return err
	}
	if !boolFlag(fs, "wait") {
		if e.json {
			return e.printJSON(a)
		}
		fmt.Fprintf(e.stdout, "%s: %s\nFollow it with 'mimiry volume wait %s'.\n", a.ID, a.Message, a.ID)
		return nil
	}
	fmt.Fprintf(e.stderr, "Volume %s submitted.\n", a.ID)
	return waitVolume(e, c, a.ID, durationFlag(fs, "timeout"))
}

func runVolumeExtend(e *env, fs *flag.FlagSet, args []string) error {
	id, err := oneID(args)
	if err != nil {
		return err
	}
	size := intFlag(fs, "size")
	if size <= 0 {
		return usage("--size is required: the new size, GB")
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	a, err := c.Volumes().Extend(e.ctx, id, size)
	if err != nil {
		return err
	}
	if e.json {
		return e.printJSON(a)
	}
	fmt.Fprintf(e.stdout, "%s: %s\n", a.ID, a.Message)
	return nil
}

func runVolumeDelete(e *env, _ *flag.FlagSet, args []string) error {
	id, err := oneID(args)
	if err != nil {
		return err
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	a, err := c.Volumes().Delete(e.ctx, id)
	if err != nil {
		return err
	}
	if e.json {
		return e.printJSON(a)
	}
	fmt.Fprintf(e.stdout, "%s: %s\n", a.ID, a.Message)
	return nil
}

func runVolumeWait(e *env, fs *flag.FlagSet, args []string) error {
	id, err := oneID(args)
	if err != nil {
		return err
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	return waitVolume(e, c, id, durationFlag(fs, "timeout"))
}

// ---- waiting ----

// waitSession follows a session until it is ready or finished, showing its
// state on stderr as it goes, and prints where it ended.
func waitSession(e *env, c *mimiry.Client, id string, timeout time.Duration) error {
	ctx, cancel := contextWithTimeout(e.ctx, timeout)
	defer cancel()
	start := time.Now()
	s, err := c.Sessions().WaitReady(ctx, id, mimiry.WaitOptions{OnPoll: progress(e, start, func(r any) (string, bool) {
		s, ok := r.(*mimiry.Session)
		if !ok {
			return "", false
		}
		return stateText(string(s.State), s.Operation), true
	})})
	fmt.Fprintln(e.stderr)
	if s != nil && (err == nil || e.json) {
		if e.json {
			e.printJSON(s)
		} else {
			printSession(e.stdout, s)
		}
	}
	return err
}

func waitVolume(e *env, c *mimiry.Client, id string, timeout time.Duration) error {
	ctx, cancel := contextWithTimeout(e.ctx, timeout)
	defer cancel()
	start := time.Now()
	v, err := c.Volumes().WaitProvisioned(ctx, id, mimiry.WaitOptions{OnPoll: progress(e, start, func(r any) (string, bool) {
		v, ok := r.(*mimiry.Volume)
		if !ok {
			return "", false
		}
		return stateText(string(v.State), v.Operation), true
	})})
	fmt.Fprintln(e.stderr)
	if v != nil && (err == nil || e.json) {
		if e.json {
			e.printJSON(v)
		} else {
			printVolume(e.stdout, v)
		}
	}
	return err
}

// progress rewrites one stderr line per poll: the state, or "(retrying)"
// after a transient failure, and the time waited.
func progress(e *env, start time.Time, state func(any) (string, bool)) func(any, error) {
	return func(r any, transient error) {
		elapsed := time.Since(start).Round(time.Second)
		if s, ok := state(r); ok {
			fmt.Fprintf(e.stderr, "\r%-40s %s", s, elapsed)
		} else if transient != nil {
			fmt.Fprintf(e.stderr, "\r%-40s %s", "(retrying)", elapsed)
		}
	}
}

func durationFlag(fs *flag.FlagSet, name string) time.Duration {
	return fs.Lookup(name).Value.(flag.Getter).Get().(time.Duration)
}
