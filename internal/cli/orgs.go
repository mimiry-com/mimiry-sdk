package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mimiry-com/mimiry-sdk/mimiry"
)

func init() {
	groups["org"] = &group{summary: "Organisations, their members and invitations", cmds: map[string]*command{
		"list": {summary: "List your organisations", flags: func(fs *flag.FlagSet) {
			listFlags(fs)
			fs.String("sort", "", "name, slug, role, member_count or created_at")
			fs.String("dir", "", "asc or desc")
		}, run: runOrgList},
		"show": {summary: "Show an organisation and its members", usage: "<org>", help: orgHelp, run: runOrgShow},
		"create": {summary: "Create an organisation, owned by you", usage: "<name>", flags: func(fs *flag.FlagSet) {
			fs.String("slug", "", "o- followed by lowercase letters, digits and hyphens (required)")
			changeFlags(fs)
		}, run: runOrgCreate},
		"update": {summary: "Change an organisation's name or slug", usage: "<org>", help: orgHelp, elevated: true,
			flags: func(fs *flag.FlagSet) {
				fs.String("name", "", "a new name")
				fs.String("slug", "", "a new slug")
				changeFlags(fs)
			}, run: runOrgUpdate},
		"delete": {summary: "Delete an organisation", usage: "<org>", elevated: true, flags: changeFlags,
			help: orgDeleteHelp, run: runOrgDelete},
		"wind-down": {summary: "Start retiring an organisation that still has members", usage: "<org>", help: orgHelp, elevated: true,
			flags: func(fs *flag.FlagSet) {
				fs.Int("readable-days", 0, "days members keep read access before it can be deleted, 1-90 (required)")
				fs.Bool("drain", false, "let running sessions finish instead of ending them")
				fs.Int("volume-grace-days", 0, "days to keep volumes, 0-30; they are billed until then")
				fs.String("reason", "", "why, for the record")
				changeFlags(fs)
			}, run: runOrgWindDown},
		"cancel-wind-down": {summary: "Stop retiring an organisation", usage: "<org>", help: orgHelp, elevated: true,
			flags: changeFlags, run: orgChange(func(e *env, c *mimiry.Client, org string, _ *flag.FlagSet, _ []string) (*mimiry.OperationAccepted, error) {
				return c.Orgs().CancelWindDown(e.ctx, org)
			}, 0)},
		"transfer": {summary: "Hand an organisation to another member", usage: "<org> <member>", elevated: true,
			flags: changeFlags, help: memberHelp, run: orgChange(func(e *env, c *mimiry.Client, org string, _ *flag.FlagSet, args []string) (*mimiry.OperationAccepted, error) {
				user, err := memberID(e, c, org, args[0])
				if err != nil {
					return nil, err
				}
				return c.Orgs().TransferOwnership(e.ctx, org, user)
			}, 1)},
		"members": {summary: "List an organisation's members", usage: "<org>", help: orgHelp, run: runOrgMembers},
		"invite": {summary: "Invite someone by email address or @username", usage: "<org> <email|@username>", help: orgHelp, elevated: true,
			flags: func(fs *flag.FlagSet) {
				fs.String("role", "member", "member or admin")
				changeFlags(fs)
			}, run: orgChange(func(e *env, c *mimiry.Client, org string, fs *flag.FlagSet, args []string) (*mimiry.OperationAccepted, error) {
				req := mimiry.InviteRequest{Role: mimiry.OrgRole(fs.Lookup("role").Value.String())}
				if name, ok := strings.CutPrefix(args[0], "@"); ok {
					req.Username = name
				} else {
					req.Email = args[0]
				}
				return c.Orgs().Invite(e.ctx, org, req)
			}, 1)},
		"set-role": {summary: "Make a member an admin, or a member again", usage: "<org> <member> <admin|member>", elevated: true,
			flags: changeFlags, help: memberHelp, run: orgChange(func(e *env, c *mimiry.Client, org string, _ *flag.FlagSet, args []string) (*mimiry.OperationAccepted, error) {
				user, err := memberID(e, c, org, args[0])
				if err != nil {
					return nil, err
				}
				return c.Orgs().SetMemberRole(e.ctx, org, user, mimiry.OrgRole(args[1]))
			}, 2)},
		"remove": {summary: "Remove a member", usage: "<org> <member>", elevated: true,
			flags: changeFlags, help: memberHelp, run: orgChange(func(e *env, c *mimiry.Client, org string, _ *flag.FlagSet, args []string) (*mimiry.OperationAccepted, error) {
				user, err := memberID(e, c, org, args[0])
				if err != nil {
					return nil, err
				}
				return c.Orgs().RemoveMember(e.ctx, org, user)
			}, 1)},
		"leave": {summary: "Leave an organisation", usage: "<org>", help: orgHelp,
			flags: changeFlags, run: orgChange(func(e *env, c *mimiry.Client, org string, _ *flag.FlagSet, _ []string) (*mimiry.OperationAccepted, error) {
				return c.Orgs().Leave(e.ctx, org)
			}, 0)},
		"invitations": {summary: "List an organisation's invitations that can still be accepted", usage: "<org>", help: orgHelp,
			flags: listFlags, run: runOrgInvitations},
		"revoke": {summary: "Withdraw an invitation", usage: "<org> <invitation-id>", help: orgHelp, elevated: true,
			flags: changeFlags, run: orgChange(func(e *env, c *mimiry.Client, org string, _ *flag.FlagSet, args []string) (*mimiry.OperationAccepted, error) {
				return c.Orgs().RevokeInvitation(e.ctx, org, args[0])
			}, 1)},
		"invitation": {summary: "Check whether you can accept an invitation", usage: "<token>", run: runOrgInvitation},
		"accept":     {summary: "Accept an invitation", usage: "<token>", flags: changeFlags, run: runOrgAccept},
		"wait": {summary: "Wait for an organisation change to finish", usage: "<request-id>", flags: func(fs *flag.FlagSet) {
			fs.Duration("timeout", 2*time.Minute, "give up after this long")
		}, run: runOrgWait},
	}}
}

const orgHelp = `
<org> is an organisation's ID or slug (o-…).`

const memberHelp = orgHelp + `
<member> is a member's user ID or email address.`

const orgDeleteHelp = orgHelp + `
An organisation is deleted only once it has no other members and no invitation
that can still be accepted. To retire one that does, use 'mimiry org wind-down'.`

// changeFlags are the flags of every command that changes an organisation.
// A change is carried out asynchronously and some checks are made only then,
// so a command waits for the outcome unless told not to: "accepted" is not
// "done", and not necessarily "allowed".
func changeFlags(fs *flag.FlagSet) {
	fs.Bool("no-wait", false, "print the request ID and return once the change is accepted")
	fs.Duration("timeout", 2*time.Minute, "give up waiting after this long")
}

// orgChange runs a change that takes <org> and then nargs more arguments.
func orgChange(do func(e *env, c *mimiry.Client, org string, fs *flag.FlagSet, args []string) (*mimiry.OperationAccepted, error), nargs int) func(*env, *flag.FlagSet, []string) error {
	return func(e *env, fs *flag.FlagSet, args []string) error {
		if len(args) != nargs+1 || args[0] == "" {
			return usage(fmt.Sprintf("expected %d argument(s)", nargs+1))
		}
		c, err := e.client()
		if err != nil {
			return err
		}
		org, err := orgID(e, c, args[0])
		if err != nil {
			return err
		}
		acc, err := do(e, c, org, fs, args[1:])
		if err != nil {
			return err
		}
		return finish(e, c, fs, acc)
	}
}

// finish reports an accepted change, or waits for its outcome.
func finish(e *env, c *mimiry.Client, fs *flag.FlagSet, acc *mimiry.OperationAccepted) error {
	if boolFlag(fs, "no-wait") {
		if e.json {
			return e.printJSON(acc)
		}
		fmt.Fprintf(e.stdout, "Accepted: %s\nRequest %s. Follow it with: mimiry org wait %s\n", acc.Message, acc.RequestID, acc.RequestID)
		return nil
	}
	return waitOperation(e, c, acc.RequestID, durationFlag(fs, "timeout"))
}

func waitOperation(e *env, c *mimiry.Client, id string, timeout time.Duration) error {
	ctx, cancel := contextWithTimeout(e.ctx, timeout)
	defer cancel()
	op, err := c.Orgs().WaitOperation(ctx, id, mimiry.WaitOptions{Interval: time.Second})
	if op != nil && (err == nil || e.json) {
		if e.json {
			e.printJSON(op)
		} else {
			fmt.Fprintln(e.stdout, "Done.")
		}
	}
	return err
}

// orgID takes an ID as it is and looks a slug up.
func orgID(e *env, c *mimiry.Client, idOrSlug string) (string, error) {
	if !strings.HasPrefix(idOrSlug, "o-") {
		return idOrSlug, nil
	}
	org, err := c.Orgs().Get(e.ctx, idOrSlug)
	if err != nil {
		return "", err
	}
	if org.ID == "" {
		return "", fmt.Errorf("the API answered for %s without an ID", idOrSlug)
	}
	return org.ID, nil
}

// memberID takes a user ID as it is and looks an email address up among the
// organisation's members.
func memberID(e *env, c *mimiry.Client, org, idOrEmail string) (string, error) {
	if !strings.Contains(idOrEmail, "@") {
		return idOrEmail, nil
	}
	members, err := c.Orgs().Members(e.ctx, org)
	if err != nil {
		return "", err
	}
	for _, m := range members {
		if strings.EqualFold(m.Email, idOrEmail) {
			return m.UserID, nil
		}
	}
	return "", fmt.Errorf("%s is not a member of this organisation", idOrEmail)
}

func runOrgList(e *env, fs *flag.FlagSet, _ []string) error {
	c, err := e.client()
	if err != nil {
		return err
	}
	p, err := c.Orgs().List(e.ctx, mimiry.OrgListOptions{ListOptions: listOptions(fs),
		Sort: fs.Lookup("sort").Value.String(), Dir: fs.Lookup("dir").Value.String()})
	if err != nil {
		return err
	}
	if e.json {
		return e.printJSON(p)
	}
	t, err := c.Token(e.ctx)
	if err != nil {
		return err
	}
	w := table(e.stdout)
	fmt.Fprintln(w, "ID\tSLUG\tNAME\tYOUR ROLE\tMEMBERS")
	for _, o := range p.Items {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\n", o.ID, o.Slug, o.Name, roleOf(&o, t.UserID), o.MemberCount)
	}
	w.Flush()
	pageFooter(e.stdout, len(p.Items), p.TotalCount, p.Offset, 0)
	return nil
}

func roleOf(o *mimiry.Org, userID string) mimiry.OrgRole {
	if o.OwnerID == userID {
		return mimiry.OrgRoleOwner
	}
	for _, m := range o.Members {
		if m.UserID == userID {
			return m.Role
		}
	}
	return ""
}

func runOrgShow(e *env, _ *flag.FlagSet, args []string) error {
	id, err := oneID(args)
	if err != nil {
		return err
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	o, err := c.Orgs().Get(e.ctx, id)
	if err != nil {
		return err
	}
	if e.json {
		return e.printJSON(o)
	}
	w := table(e.stdout)
	fmt.Fprintf(w, "ID\t%s\nSlug\t%s\nName\t%s\nOwner\t%s\nCreated\t%s\n",
		o.ID, o.Slug, o.Name, o.OwnerID, o.CreatedAt.Local().Format(time.DateTime))
	w.Flush()
	fmt.Fprintln(e.stdout)
	printMembers(e.stdout, o.Members)
	return nil
}

func runOrgMembers(e *env, _ *flag.FlagSet, args []string) error {
	arg, err := oneID(args)
	if err != nil {
		return err
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	org, err := orgID(e, c, arg)
	if err != nil {
		return err
	}
	members, err := c.Orgs().Members(e.ctx, org)
	if err != nil {
		return err
	}
	if e.json {
		return e.printJSON(members)
	}
	printMembers(e.stdout, members)
	return nil
}

func printMembers(out io.Writer, members []mimiry.Member) {
	w := table(out)
	fmt.Fprintln(w, "USER ID\tEMAIL\tROLE\tJOINED")
	for _, m := range members {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", m.UserID, m.Email, m.Role, m.JoinedAt.Local().Format(time.DateTime))
	}
	w.Flush()
}

func runOrgCreate(e *env, fs *flag.FlagSet, args []string) error {
	if len(args) != 1 || args[0] == "" {
		return usage("expected the organisation's name")
	}
	slug := fs.Lookup("slug").Value.String()
	if slug == "" {
		return usage("--slug is required")
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	acc, err := c.Orgs().Create(e.ctx, mimiry.CreateOrgRequest{Name: args[0], Slug: slug})
	if err != nil {
		return err
	}
	return finish(e, c, fs, acc)
}

func runOrgUpdate(e *env, fs *flag.FlagSet, args []string) error {
	var req mimiry.UpdateOrgRequest
	if v := fs.Lookup("name").Value.String(); v != "" {
		req.Name = &v
	}
	if v := fs.Lookup("slug").Value.String(); v != "" {
		req.Slug = &v
	}
	if req == (mimiry.UpdateOrgRequest{}) {
		return usage("nothing to change: give --name or --slug")
	}
	return orgChange(func(e *env, c *mimiry.Client, org string, _ *flag.FlagSet, _ []string) (*mimiry.OperationAccepted, error) {
		return c.Orgs().Update(e.ctx, org, req)
	}, 0)(e, fs, args)
}

func runOrgDelete(e *env, fs *flag.FlagSet, args []string) error {
	return orgChange(func(e *env, c *mimiry.Client, org string, _ *flag.FlagSet, _ []string) (*mimiry.OperationAccepted, error) {
		return c.Orgs().Delete(e.ctx, org)
	}, 0)(e, fs, args)
}

func runOrgWindDown(e *env, fs *flag.FlagSet, args []string) error {
	req := mimiry.WindDownRequest{
		ReadableDays:    intFlag(fs, "readable-days"),
		DrainSessions:   boolFlag(fs, "drain"),
		VolumeGraceDays: intFlag(fs, "volume-grace-days"),
		Reason:          fs.Lookup("reason").Value.String(),
	}
	if req.ReadableDays < 1 || req.ReadableDays > 90 {
		return usage("--readable-days is required, 1-90")
	}
	return orgChange(func(e *env, c *mimiry.Client, org string, _ *flag.FlagSet, _ []string) (*mimiry.OperationAccepted, error) {
		return c.Orgs().StartWindDown(e.ctx, org, req)
	}, 0)(e, fs, args)
}

func runOrgInvitations(e *env, fs *flag.FlagSet, args []string) error {
	arg, err := oneID(args)
	if err != nil {
		return err
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	org, err := orgID(e, c, arg)
	if err != nil {
		return err
	}
	p, err := c.Orgs().Invitations(e.ctx, org, listOptions(fs))
	if err != nil {
		return err
	}
	if e.json {
		return e.printJSON(p)
	}
	w := table(e.stdout)
	fmt.Fprintln(w, "ID\tTO\tROLE\tEXPIRES")
	for _, inv := range p.Items {
		to := inv.Email
		if to == "" {
			to = "user " + inv.InvitedUserID
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", inv.ID, to, inv.Role, inv.ExpiresAt.Local().Format(time.DateTime))
	}
	w.Flush()
	pageFooter(e.stdout, len(p.Items), p.TotalCount, p.Offset, 0)
	return nil
}

func runOrgInvitation(e *env, _ *flag.FlagSet, args []string) error {
	token, err := oneID(args)
	if err != nil {
		return err
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	p, err := c.Orgs().Invitation(e.ctx, token)
	if err != nil {
		return err
	}
	if e.json {
		return e.printJSON(p)
	}
	if !p.CanAccept {
		fmt.Fprintf(e.stdout, "You cannot accept this invitation: %s\n", p.Message)
		return nil
	}
	fmt.Fprintf(e.stdout, "You can accept this invitation to %s, as %s", p.OrgName, p.Role)
	if p.ExpiresAt != nil {
		fmt.Fprintf(e.stdout, ", until %s", p.ExpiresAt.Local().Format(time.DateTime))
	}
	fmt.Fprintln(e.stdout, ".")
	return nil
}

func runOrgAccept(e *env, fs *flag.FlagSet, args []string) error {
	token, err := oneID(args)
	if err != nil {
		return err
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	acc, err := c.Orgs().AcceptInvitation(e.ctx, token)
	if err != nil {
		return err
	}
	return finish(e, c, fs, acc)
}

func runOrgWait(e *env, fs *flag.FlagSet, args []string) error {
	id, err := oneID(args)
	if err != nil {
		return err
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	return waitOperation(e, c, id, durationFlag(fs, "timeout"))
}
