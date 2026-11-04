package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mimiry-com/mimiry-sdk/internal/apispec"
	"github.com/mimiry-com/mimiry-sdk/mimiry"
)

func init() {
	topLevel["auth"] = &command{
		summary: "Sign in and remember the host and key for next time",
		run:     runAuth,
	}
	topLevel["version"] = &command{
		summary: "Print the version",
		run: func(e *env, _ *flag.FlagSet, _ []string) error {
			fmt.Fprintf(e.stdout, "mimiry %s\n", mimiry.Version)
			return nil
		},
	}
	topLevel["availability"] = &command{
		summary: "GPU models in stock now, with prices",
		flags:   availabilityFlags,
		run:     runAvailability,
	}
	topLevel["balance"] = &command{
		summary: "Your balance",
		usage:   "[--org <id|slug>]",
		flags: func(fs *flag.FlagSet) {
			fs.String("org", "", "an organisation's balance "+apispec.Marker)
		},
		run: runBalance,
	}
	topLevel["quota"] = &command{summary: "Your usage limits", run: runQuota}
	topLevel["transactions"] = &command{
		summary: "Your credits and debits, newest first",
		flags: func(fs *flag.FlagSet) {
			listFlags(fs)
			fs.String("type", "", "credit or debit")
			fs.String("resource", "", "only rows charging this session or volume ID")
		},
		run: runTransactions,
	}

	groups["session"] = &group{summary: "Compute sessions", cmds: map[string]*command{
		"create": {summary: "Start a session", usage: "<image>", help: sessionCreateHelp,
			flags: sessionCreateFlags, run: runSessionCreate},
		"list": {summary: "List your sessions, newest first", flags: func(fs *flag.FlagSet) {
			listFlags(fs)
			fs.String("state", "", "comma-separated states to include")
			fs.String("state-not", "", "comma-separated states to exclude")
		}, run: runSessionList},
		"status": {summary: "Show one session", usage: "<id>", flags: func(fs *flag.FlagSet) {
			fs.Int("events", 3, "timeline events to show (-1 for all)")
		}, run: runSessionStatus},
		"logs": {summary: "Print a session's container output", usage: "<id>", flags: func(fs *flag.FlagSet) {
			fs.Int("tail", 100, "lines (at most 5000)")
			fs.String("since", "", "RFC 3339 time or a duration such as 30m (default 24h)")
		}, run: runSessionLogs},
		"terminate": {summary: "End a session", usage: "<id>", run: runSessionTerminate},
		"wait": {summary: "Wait until a session is ready or finished", usage: "<id>", flags: func(fs *flag.FlagSet) {
			fs.Duration("timeout", 30*time.Minute, "give up after this long")
		}, run: runSessionWait},
	}}
	groups["volume"] = &group{summary: "Persistent block volumes", cmds: map[string]*command{
		"create": {summary: "Create a volume", flags: volumeCreateFlags, run: runVolumeCreate},
		"list":   {summary: "List your volumes, newest first", flags: listFlags, run: runVolumeList},
		"status": {summary: "Show one volume", usage: "<id>", run: runVolumeStatus},
		"extend": {summary: "Grow a volume", usage: "<id>", flags: func(fs *flag.FlagSet) {
			fs.Int("size", 0, "the new size, GB (larger than now)")
		}, run: runVolumeExtend},
		"delete": {summary: "Delete a volume, detaching it first if needed", usage: "<id>", run: runVolumeDelete},
		"wait": {summary: "Wait until a volume is ready", usage: "<id>", flags: func(fs *flag.FlagSet) {
			fs.Duration("timeout", 10*time.Minute, "give up after this long")
		}, run: runVolumeWait},
	}}
}

// ---- auth ----

func runAuth(e *env, _ *flag.FlagSet, _ []string) error {
	c, err := e.client()
	if err != nil {
		return err
	}
	t, err := c.SignIn(e.ctx)
	if err != nil {
		return err
	}

	// Remember what worked. Only after a successful sign-in, so a typo is
	// never saved as the default.
	cfg, err := mimiry.LoadConfig()
	if err != nil {
		return err
	}
	if e.host != "" {
		cfg.Host = e.host
	}
	if e.key != "" {
		if cfg.Keys == nil {
			cfg.Keys = map[string]string{}
		}
		cfg.Keys[c.BaseURL()] = absPath(e.key)
	}
	if err := cfg.Save(); err != nil {
		return err
	}

	if e.json {
		return e.printJSON(map[string]any{"base_url": t.BaseURL, "user_id": t.UserID, "email": t.Email,
			"fingerprint": t.Fingerprint, "expires_at": t.ExpiresAt})
	}
	who := t.Email
	if who == "" {
		who = t.UserID
	}
	fmt.Fprintf(e.stdout, "Signed in to %s as %s\nKey %s\nToken valid until %s\n",
		t.BaseURL, who, t.Fingerprint, t.ExpiresAt.Local().Format(time.DateTime))
	return nil
}

func absPath(p string) string {
	if strings.HasPrefix(p, "~") || strings.HasPrefix(p, "/") {
		return p
	}
	if wd, err := os.Getwd(); err == nil {
		return wd + string(os.PathSeparator) + p
	}
	return p
}

// ---- sessions ----

func runSessionList(e *env, fs *flag.FlagSet, _ []string) error {
	c, err := e.client()
	if err != nil {
		return err
	}
	o := mimiry.SessionListOptions{ListOptions: listOptions(fs)}
	for _, s := range csv(fs, "state") {
		o.State = append(o.State, mimiry.SessionState(s))
	}
	for _, s := range csv(fs, "state-not") {
		o.StateNot = append(o.StateNot, mimiry.SessionState(s))
	}
	p, err := c.Sessions().List(e.ctx, o)
	if err != nil {
		return err
	}
	if e.json {
		return e.printJSON(p)
	}
	w := table(e.stdout)
	fmt.Fprintln(w, "ID\tNAME\tSTATE\tGPU\tCREATED\tCOST")
	for _, s := range p.Items {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", s.ID, s.Name, stateText(string(s.State), s.Operation),
			deref(s.GPUType), s.CreatedAt.Local().Format(time.DateTime), cost(s.Billing))
	}
	w.Flush()
	pageFooter(e.stdout, len(p.Items), p.TotalCount, p.Offset, p.Unavailable)
	return nil
}

func runSessionStatus(e *env, fs *flag.FlagSet, args []string) error {
	id, err := oneID(args)
	if err != nil {
		return err
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	events := intFlag(fs, "events")
	s, err := c.Sessions().Get(e.ctx, id, mimiry.GetSessionOptions{EventsTail: &events})
	if err != nil {
		return err
	}
	if e.json {
		return e.printJSON(s)
	}
	printSession(e.stdout, s)
	return nil
}

func printSession(out io.Writer, s *mimiry.Session) {
	w := table(out)
	row := func(k, v string) {
		if v != "" {
			fmt.Fprintf(w, "%s\t%s\n", k, v)
		}
	}
	row("ID", s.ID)
	row("Name", s.Name)
	row("State", stateText(string(s.State), s.Operation))
	if s.GPUType != nil {
		row("GPU", fmt.Sprintf("%s × %d", *s.GPUType, max(s.GPUCount, 1)))
	}
	if s.SSH != nil && s.SSH.Host != "" {
		row("SSH", fmt.Sprintf("ssh -p %d %s@%s", s.SSH.Port, s.SSH.Username, s.SSH.Host))
	}
	if s.ExitCode != nil {
		row("Exit code", strconv.Itoa(*s.ExitCode))
	}
	row("Stop reason", deref(s.StopReason))
	row("Error", deref(s.Error))
	row("Created", s.CreatedAt.Local().Format(time.DateTime))
	if b := s.Billing; b != nil {
		row("Rate", fmt.Sprintf("%.4f %s/h", b.HourlyRate, b.Currency))
		row("Cost", cost(b))
	}
	w.Flush()
	if len(s.Events) > 0 {
		fmt.Fprintf(out, "\nEvents (newest first, %d of %d):\n", len(s.Events), s.EventsTotal)
		for _, ev := range s.Events {
			fmt.Fprintf(out, "  %s  %s  %s\n", ev.Timestamp, ev.Event, ev.Message)
		}
	}
}

func runSessionLogs(e *env, fs *flag.FlagSet, args []string) error {
	id, err := oneID(args)
	if err != nil {
		return err
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	l, err := c.Sessions().Logs(e.ctx, id, mimiry.LogsOptions{Tail: intFlag(fs, "tail"), Since: fs.Lookup("since").Value.String()})
	if err != nil {
		return err
	}
	if e.json {
		return e.printJSON(l)
	}
	fmt.Fprint(e.stdout, l.Logs)
	if l.Logs != "" && !strings.HasSuffix(l.Logs, "\n") {
		fmt.Fprintln(e.stdout)
	}
	return nil
}

func runSessionTerminate(e *env, _ *flag.FlagSet, args []string) error {
	id, err := oneID(args)
	if err != nil {
		return err
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	a, err := c.Sessions().Terminate(e.ctx, id)
	if err != nil {
		return err
	}
	if e.json {
		return e.printJSON(a)
	}
	fmt.Fprintf(e.stdout, "%s: %s\n", a.ID, a.Message)
	return nil
}

func runSessionWait(e *env, fs *flag.FlagSet, args []string) error {
	id, err := oneID(args)
	if err != nil {
		return err
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	return waitSession(e, c, id, durationFlag(fs, "timeout"))
}

// ---- volumes ----

func runVolumeList(e *env, fs *flag.FlagSet, _ []string) error {
	c, err := e.client()
	if err != nil {
		return err
	}
	p, err := c.Volumes().List(e.ctx, mimiry.VolumeListOptions{ListOptions: listOptions(fs)})
	if err != nil {
		return err
	}
	if e.json {
		return e.printJSON(p)
	}
	w := table(e.stdout)
	fmt.Fprintln(w, "ID\tNAME\tSTATE\tSIZE\tTYPE\tLOCATION\tATTACHED TO")
	for _, v := range p.Items {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d GB\t%s\t%s\t%s\n", v.ID, v.Name, stateText(string(v.State), v.Operation),
			v.SizeGB, v.VolumeType, v.Location, v.AttachedTo)
	}
	w.Flush()
	pageFooter(e.stdout, len(p.Items), p.TotalCount, p.Offset, p.Unavailable)
	return nil
}

func runVolumeStatus(e *env, _ *flag.FlagSet, args []string) error {
	id, err := oneID(args)
	if err != nil {
		return err
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	v, err := c.Volumes().Get(e.ctx, id)
	if err != nil {
		return err
	}
	if e.json {
		return e.printJSON(v)
	}
	printVolume(e.stdout, v)
	return nil
}

func printVolume(out io.Writer, v *mimiry.Volume) {
	w := table(out)
	for _, kv := range [][2]string{
		{"ID", v.ID}, {"Name", v.Name}, {"State", stateText(string(v.State), v.Operation)},
		{"Size", fmt.Sprintf("%d GB %s", v.SizeGB, v.VolumeType)}, {"Location", v.Location},
		{"Attached to", v.AttachedTo}, {"Reason", v.Reason},
		{"Rate", fmt.Sprintf("%g per GB-hour", v.RatePerGBHour)}, {"Created", v.CreatedAt.Local().Format(time.DateTime)},
	} {
		if kv[1] != "" {
			fmt.Fprintf(w, "%s\t%s\n", kv[0], kv[1])
		}
	}
	w.Flush()
}

// ---- GPUs and account ----

func availabilityFlags(fs *flag.FlagSet) {
	fs.String("family", "", "comma-separated GPU families")
	fs.Int("min-vram", 0, "minimum GPU memory, GB")
	fs.Int("min-gpus", 0, "minimum GPUs per machine")
	fs.String("location", "", "location")
	fs.Float64("max-rate", 0, "maximum price, EUR per hour")
	fs.Float64("min-rate", 0, "minimum price, EUR per hour")
	fs.Bool("all", false, "include models with no stock")
}

func runAvailability(e *env, fs *flag.FlagSet, _ []string) error {
	c, err := e.client()
	if err != nil {
		return err
	}
	o := mimiry.AvailabilityOptions{
		Families:           csv(fs, "family"),
		MinVRAMGB:          intFlag(fs, "min-vram"),
		MinGPUCount:        intFlag(fs, "min-gpus"),
		Location:           fs.Lookup("location").Value.String(),
		IncludeUnavailable: boolFlag(fs, "all"),
	}
	if r := floatFlag(fs, "max-rate"); r > 0 {
		o.MaxHourlyRate = &r
	}
	if r := floatFlag(fs, "min-rate"); r > 0 {
		o.MinHourlyRate = &r
	}
	models, err := c.GPUs().Availability(e.ctx, o)
	if err != nil {
		return err
	}
	if e.json {
		return e.printJSON(models)
	}
	w := table(e.stdout)
	fmt.Fprintln(w, "MODEL\tVRAM\tFROM\tSKU\tARCH\tGPUS\tVCPUS\tRAM\tLOCATIONS")
	for _, m := range models {
		for _, s := range m.SKUs {
			if !s.Available && !o.IncludeUnavailable {
				continue
			}
			fmt.Fprintf(w, "%s\t%d GB\t%.4f %s/h\t%s\t%s\t%d\t%d\t%d GB\t%s\n", m.Name, m.VRAMGB, s.FromHourlyRate,
				s.Currency, s.SKU, s.Architecture, s.GPUCount, s.VCPUs, s.MemoryGB, strings.Join(listingLocations(s), ","))
		}
	}
	return w.Flush()
}

func listingLocations(s mimiry.SKU) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range s.Listings {
		if l.Location != "" && !seen[l.Location] && (l.Available || !s.Available) {
			seen[l.Location] = true
			out = append(out, l.Location)
		}
	}
	return out
}

func runBalance(e *env, fs *flag.FlagSet, _ []string) error {
	c, err := e.client()
	if err != nil {
		return err
	}
	var b *mimiry.Balance
	if org := fs.Lookup("org").Value.String(); org != "" {
		b, err = c.Account().OrgBalance(e.ctx, org)
	} else {
		b, err = c.Account().Balance(e.ctx)
	}
	if err != nil {
		return err
	}
	if e.json {
		return e.printJSON(b)
	}
	active := "unknown"
	if b.ActiveSessions != nil {
		active = strconv.FormatInt(*b.ActiveSessions, 10)
	}
	w := table(e.stdout)
	fmt.Fprintf(w, "Account\t%s %s\n", b.AccountType, b.AccountID)
	fmt.Fprintf(w, "Balance\t%.2f %s\n", b.Balance, b.Currency)
	fmt.Fprintf(w, "Available\t%.2f %s (%.2f reserved)\n", b.Available, b.Currency, b.Reserved)
	fmt.Fprintf(w, "Active sessions\t%s\n", active)
	return w.Flush()
}

func runQuota(e *env, _ *flag.FlagSet, _ []string) error {
	c, err := e.client()
	if err != nil {
		return err
	}
	q, err := c.Account().Quota(e.ctx)
	if err != nil {
		return err
	}
	if e.json {
		return e.printJSON(q)
	}
	limit := func(used, max float64) string {
		if max <= 0 {
			return fmt.Sprintf("%g (no limit)", used)
		}
		return fmt.Sprintf("%g of %g", used, max)
	}
	w := table(e.stdout)
	fmt.Fprintf(w, "Concurrent sessions\t%s\n", limit(float64(q.CurrentSessions), float64(q.MaxConcurrentSessions)))
	fmt.Fprintf(w, "GPU hours today\t%s\n", limit(q.GPUHoursUsedToday, q.MaxGPUHoursDaily))
	fmt.Fprintf(w, "GPU hours this month\t%s\n", limit(q.GPUHoursUsedMonth, q.MaxGPUHoursMonthly))
	return w.Flush()
}

func runTransactions(e *env, fs *flag.FlagSet, _ []string) error {
	c, err := e.client()
	if err != nil {
		return err
	}
	p, err := c.Account().Transactions(e.ctx, mimiry.TransactionListOptions{
		ListOptions: listOptions(fs),
		Type:        fs.Lookup("type").Value.String(),
		ResourceID:  fs.Lookup("resource").Value.String(),
	})
	if err != nil {
		return err
	}
	if e.json {
		return e.printJSON(p)
	}
	w := table(e.stdout)
	fmt.Fprintln(w, "TIME\tTYPE\tAMOUNT\tBALANCE AFTER\tDESCRIPTION")
	for _, t := range p.Items {
		fmt.Fprintf(w, "%s\t%s\t%.4f %s\t%.4f\t%s\n", t.CreatedAt.Local().Format(time.DateTime), t.Type,
			t.Amount, t.Currency, t.BalanceAfter, t.Description)
	}
	w.Flush()
	pageFooter(e.stdout, len(p.Items), p.TotalCount, p.Offset, 0)
	return nil
}

// ---- helpers ----

func listFlags(fs *flag.FlagSet) {
	fs.Int("limit", 0, "items per page (server default 50, at most 100)")
	fs.Int("offset", 0, "items to skip")
}

func listOptions(fs *flag.FlagSet) mimiry.ListOptions {
	return mimiry.ListOptions{Limit: intFlag(fs, "limit"), Offset: intFlag(fs, "offset")}
}

func pageFooter(w io.Writer, shown, total, offset, unavailable int) {
	if shown < total {
		fmt.Fprintf(w, "\n%d–%d of %d (use --offset for more)\n", offset+1, offset+shown, total)
	}
	if unavailable > 0 {
		fmt.Fprintf(w, "\n⚠ %d could not be read by the server and are missing from this list.\n", unavailable)
	}
}

func table(w io.Writer) *tabwriter.Writer { return tabwriter.NewWriter(w, 0, 0, 2, ' ', 0) }

func stateText(state, op string) string {
	if op == "" {
		return state
	}
	return state + " (" + op + ")"
}

func cost(b *mimiry.SessionBilling) string {
	switch {
	case b == nil:
		return ""
	case b.FinalCost != nil:
		return fmt.Sprintf("%.4f %s", *b.FinalCost, b.Currency)
	case b.BilledFrom != nil:
		return fmt.Sprintf("%.4f %s so far", b.RunningCost, b.Currency)
	}
	return ""
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func oneID(args []string) (string, error) {
	if len(args) != 1 || args[0] == "" {
		return "", usage("expected exactly one ID")
	}
	return args[0], nil
}

func csv(fs *flag.FlagSet, name string) []string {
	var out []string
	for _, p := range strings.Split(fs.Lookup(name).Value.String(), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func intFlag(fs *flag.FlagSet, name string) int {
	return fs.Lookup(name).Value.(flag.Getter).Get().(int)
}

func floatFlag(fs *flag.FlagSet, name string) float64 {
	return fs.Lookup(name).Value.(flag.Getter).Get().(float64)
}

func boolFlag(fs *flag.FlagSet, name string) bool {
	return fs.Lookup(name).Value.(flag.Getter).Get().(bool)
}
