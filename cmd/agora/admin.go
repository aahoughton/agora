package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/aahoughton/agora/internal/client"
	"github.com/aahoughton/agora/internal/config"
	"github.com/aahoughton/agora/internal/core"
	"github.com/aahoughton/agora/internal/store"
)

const adminUsage = `usage: agora admin <command> [flags]

Against the database file, for setting up and recovering a server in
auth mode token (they work while the server runs):
  bootstrap --db PATH --name NAME   create the first server admin and its token
  recover   --db PATH --name NAME   re-enable an account as server admin, with a new token

Through a running server, with a token (--token or AGORA_TOKEN):
  accounts list
  accounts add NAME [--server-admin]
  accounts disable|enable|promote|demote NAME
  tokens list ACCOUNT
  tokens issue ACCOUNT [--label TEXT] [--ttl DURATION]
  tokens revoke ID
  tokens revoke-all ACCOUNT
  members list NAMESPACE
  members set NAMESPACE ACCOUNT viewer|observer|member|admin
  members remove NAMESPACE ACCOUNT
  namespaces create NAME

A new token is printed once; store it where the client reads it
(AGORA_TOKEN). Server admins manage accounts and namespaces; an account
manages its own tokens; namespace admins manage their members.
`

func runAdmin(ctx context.Context, args []string, stdout, stderr io.Writer, getenv config.Getenv) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, adminUsage)
		return exitUsage
	}
	switch args[0] {
	case "bootstrap", "recover":
		return runAdminLocal(ctx, args[0], args[1:], stdout, stderr)
	case "accounts", "tokens", "members", "namespaces":
		return runAdminRemote(ctx, args, stdout, stderr, getenv)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, adminUsage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "agora admin: unknown command %q\n\n%s", args[0], adminUsage)
		return exitUsage
	}
}

func runAdminLocal(ctx context.Context, cmd string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("admin "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "", "the server's database file (required; it must exist)")
	name := fs.String("name", "", "account name (required)")
	if err := fs.Parse(args); err != nil {
		return flagError(stderr, err)
	}
	if fs.NArg() > 0 {
		return flagError(stderr, fmt.Errorf("unexpected argument %q", fs.Arg(0)))
	}
	if *dbPath == "" || *name == "" {
		return flagError(stderr, errors.New("--db and --name are required"))
	}
	db, err := store.OpenExisting(ctx, *dbPath)
	if err != nil {
		fmt.Fprintln(stderr, "agora admin:", err)
		return exitError
	}
	defer func() { _ = db.Close() }()
	svc := core.New(db, core.Options{Policy: core.RolePolicy()})
	var tok core.IssuedToken
	if cmd == "bootstrap" {
		tok, err = svc.Bootstrap(ctx, *name)
	} else {
		tok, err = svc.Recover(ctx, *name)
	}
	if err != nil {
		fmt.Fprintln(stderr, "agora admin:", err)
		return exitError
	}
	fmt.Fprintf(stdout, "%s is a server admin. Its token (shown once):\n%s\n", tok.Account, tok.Secret)
	return exitOK
}

// adminCommand is one remote admin command: its arity and what it does.
type adminCommand struct {
	args int
	run  func(ctx context.Context, cl *client.Client, args []string, out io.Writer) error
}

func runAdminRemote(ctx context.Context, args []string, stdout, stderr io.Writer, getenv config.Getenv) int {
	if len(args) < 2 {
		fmt.Fprint(stderr, adminUsage)
		return exitUsage
	}
	group, verb := args[0], args[1]
	fs := flag.NewFlagSet("admin "+group+" "+verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	resolveClient := config.ClientFlags(fs)
	serverAdmin := fs.Bool("server-admin", false, "accounts add: make the account a server admin")
	label := fs.String("label", "", "tokens issue: a label to recognize the token by")
	ttl := fs.Duration("ttl", 0, "tokens issue: lifetime, e.g. 720h (default: never expires)")

	setAccount := func(disabled, admin *bool) adminCommand {
		return adminCommand{1, func(ctx context.Context, cl *client.Client, a []string, out io.Writer) error {
			acct, err := cl.UpdateAccount(ctx, a[0], disabled, admin)
			if err == nil {
				printAccounts(out, []core.Account{acct})
			}
			return err
		}}
	}
	yes, no := true, false
	commands := map[string]adminCommand{
		"accounts list": {0, func(ctx context.Context, cl *client.Client, _ []string, out io.Writer) error {
			as, err := cl.ListAccounts(ctx)
			if err == nil {
				printAccounts(out, as)
			}
			return err
		}},
		"accounts add": {1, func(ctx context.Context, cl *client.Client, a []string, out io.Writer) error {
			acct, err := cl.CreateAccount(ctx, a[0], *serverAdmin)
			if err == nil {
				printAccounts(out, []core.Account{acct})
			}
			return err
		}},
		"accounts disable": setAccount(&yes, nil),
		"accounts enable":  setAccount(&no, nil),
		"accounts promote": setAccount(nil, &yes),
		"accounts demote":  setAccount(nil, &no),
		"tokens list": {1, func(ctx context.Context, cl *client.Client, a []string, out io.Writer) error {
			ts, err := cl.ListTokens(ctx, a[0])
			if err == nil {
				printTokens(out, ts)
			}
			return err
		}},
		"tokens issue": {1, func(ctx context.Context, cl *client.Client, a []string, out io.Writer) error {
			t, err := cl.IssueToken(ctx, a[0], *label, *ttl)
			if err == nil {
				fmt.Fprintf(out, "token %d for %s (shown once):\n%s\n", t.ID, t.Account, t.Secret)
			}
			return err
		}},
		"tokens revoke": {1, func(ctx context.Context, cl *client.Client, a []string, out io.Writer) error {
			id, err := strconv.ParseInt(a[0], 10, 64)
			if err != nil {
				return fmt.Errorf("token id %q is not a number", a[0])
			}
			if err := cl.RevokeToken(ctx, id); err != nil {
				return err
			}
			fmt.Fprintf(out, "revoked token %d\n", id)
			return nil
		}},
		"tokens revoke-all": {1, func(ctx context.Context, cl *client.Client, a []string, out io.Writer) error {
			n, err := cl.RevokeAllTokens(ctx, a[0])
			if err == nil {
				fmt.Fprintf(out, "revoked %d tokens of %s\n", n, a[0])
			}
			return err
		}},
		"members list": {1, func(ctx context.Context, cl *client.Client, a []string, out io.Writer) error {
			ms, err := cl.ListMembers(ctx, a[0])
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ACCOUNT\tROLE")
			for _, m := range ms {
				fmt.Fprintf(tw, "%s\t%s\n", m.Account, m.Role)
			}
			return tw.Flush()
		}},
		"members set": {3, func(ctx context.Context, cl *client.Client, a []string, out io.Writer) error {
			m, err := cl.SetMember(ctx, a[0], a[1], core.Role(a[2]))
			if err == nil {
				fmt.Fprintf(out, "%s is %s in %s\n", m.Account, m.Role, m.Namespace)
			}
			return err
		}},
		"members remove": {2, func(ctx context.Context, cl *client.Client, a []string, out io.Writer) error {
			if err := cl.RemoveMember(ctx, a[0], a[1]); err != nil {
				return err
			}
			fmt.Fprintf(out, "%s has no role in %s\n", a[1], a[0])
			return nil
		}},
		"namespaces create": {1, func(ctx context.Context, cl *client.Client, a []string, out io.Writer) error {
			ns, err := cl.CreateNamespace(ctx, a[0])
			if err == nil {
				fmt.Fprintf(out, "created namespace %s\n", ns.Name)
			}
			return err
		}},
	}
	cmd, ok := commands[group+" "+verb]
	if !ok {
		fmt.Fprintf(stderr, "agora admin: unknown command %q\n\n%s", group+" "+verb, adminUsage)
		return exitUsage
	}
	if err := fs.Parse(args[2:]); err != nil {
		return flagError(stderr, err)
	}
	// Flags may also follow the arguments.
	rest := fs.Args()
	var positional []string
	for len(rest) > 0 {
		if strings.HasPrefix(rest[0], "-") {
			if err := fs.Parse(rest); err != nil {
				return flagError(stderr, err)
			}
			rest = fs.Args()
			continue
		}
		positional, rest = append(positional, rest[0]), rest[1:]
	}
	if len(positional) != cmd.args {
		return flagError(stderr, fmt.Errorf("%s %s takes %d arguments, got %d", group, verb, cmd.args, len(positional)))
	}
	cc, err := resolveClient(getenv)
	if err != nil {
		return flagError(stderr, err)
	}
	cl, err := client.New(cc.URL, cc.Token)
	if err != nil {
		return flagError(stderr, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := cmd.run(ctx, cl, positional, stdout); err != nil {
		fmt.Fprintln(stderr, "agora admin:", err)
		return exitError
	}
	return exitOK
}

func printAccounts(out io.Writer, as []core.Account) {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSERVER ADMIN\tDISABLED\tCREATED")
	for _, a := range as {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", a.Name, yesNo(a.ServerAdmin), yesNo(a.Disabled), a.CreatedAt.Format(time.DateOnly))
	}
	_ = tw.Flush()
}

func printTokens(out io.Writer, ts []core.Token) {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tLABEL\tCREATED\tLAST USED\tEXPIRES\tREVOKED")
	for _, t := range ts {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n", t.ID, shown(t.Label), t.CreatedAt.Format(time.DateOnly),
			dateOr(t.LastUsedAt, "never"), dateOr(t.ExpiresAt, "never"), dateOr(t.RevokedAt, "-"))
	}
	_ = tw.Flush()
}

// shown replaces the characters a terminal would act on (controls and
// bidirectional formatting) with '?', for printing text other people chose.
func shown(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || (r >= 0x7f && r < 0xa0) || unicode.Is(unicode.Bidi_Control, r) {
			return '?'
		}
		return r
	}, s)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func dateOr(t *time.Time, none string) string {
	if t == nil {
		return none
	}
	return t.Format(time.DateOnly)
}
