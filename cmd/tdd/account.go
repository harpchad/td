package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/harpchad/td/internal/auth"
	"github.com/harpchad/td/internal/store"
)

// minPasswordLength is a floor, not a policy. Composition rules push people
// toward predictable substitutions; length is the thing that helps.
const minPasswordLength = 12

// accountCreate is the only way an account comes into existence. There is no
// signup page and no route that makes one, so this command is the whole of
// first run.
//
// It prints the TOTP enrolment URI and the recovery codes exactly once. They
// are hashed at rest, so nothing can print them again.
func accountCreate(ctx context.Context, st *store.Store, args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("account create", flag.ContinueOnError)
	fs.SetOutput(stdout)
	username := fs.String("username", "", "the account username")
	issuer := fs.String("issuer", "td", "issuer name shown in the authenticator app")
	if err := fs.Parse(args); err != nil {
		return err
	}

	exists, err := st.HasAccount(ctx)
	if err != nil {
		return err
	}
	if exists {
		return errors.New("an account already exists. There is one, and this command does not replace it")
	}

	reader := bufio.NewReader(stdin)

	name := strings.TrimSpace(*username)
	if name == "" {
		fmt.Fprint(stdout, "Username: ")
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		name = strings.TrimSpace(line)
	}
	if name == "" {
		return errors.New("a username is required")
	}

	password, err := readPassword(reader, stdout, "Password: ")
	if err != nil {
		return err
	}
	if len(password) < minPasswordLength {
		return fmt.Errorf("password must be at least %d characters", minPasswordLength)
	}
	again, err := readPassword(reader, stdout, "Password again: ")
	if err != nil {
		return err
	}
	if password != again {
		return errors.New("the two passwords do not match")
	}

	hash, err := auth.HashPassword(password, auth.DefaultParams)
	if err != nil {
		return err
	}

	// TOTP is required at enrollment rather than offered afterwards. An
	// optional second factor on an internet-facing server is a second factor
	// you do not have.
	secret, uri, err := auth.NewTOTPSecret(*issuer, name)
	if err != nil {
		return err
	}
	codes, hashes, err := auth.NewRecoveryCodes(auth.RecoveryCodeCount)
	if err != nil {
		return err
	}

	now := time.Now()
	if _, err := st.CreateAccount(ctx, name, hash, secret, hashes, now); err != nil {
		return err
	}

	current, err := auth.GenerateTOTP(secret, now)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, `
Account %s created.

Add this to your authenticator app:

  %s

  secret: %s
  a code valid right now: %s

Check that your app shows that code before you close this window.

Recovery codes. Each works exactly once. They are hashed at rest, so this
is the only time they can be printed:

`, name, uri, secret, current)

	for _, code := range codes {
		fmt.Fprintf(stdout, "  %s\n", code)
	}
	fmt.Fprint(stdout, "\nStore them somewhere that is not this machine.\n")
	return nil
}

// readPassword reads without echo from a terminal, and falls back to a plain
// line when stdin is not one, so the command stays scriptable in a test.
func readPassword(reader *bufio.Reader, stdout io.Writer, prompt string) (string, error) {
	fmt.Fprint(stdout, prompt)
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		raw, err := term.ReadPassword(fd)
		fmt.Fprintln(stdout)
		return string(raw), err
	}
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// tokenCommand manages the API tokens non-browser clients authenticate with.
// The TUI, each plugin, and each MCP client gets its own, so one can be
// revoked without touching the others.
func tokenCommand(ctx context.Context, st *store.Store, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("token takes create, list, or revoke")
	}
	switch args[0] {
	case "create":
		return tokenCreate(ctx, st, args[1:], stdout)
	case "list":
		return tokenList(ctx, st, stdout)
	case "revoke":
		return tokenRevoke(ctx, st, args[1:], stdout)
	default:
		return fmt.Errorf("unknown token command %q", args[0])
	}
}

func tokenCreate(ctx context.Context, st *store.Store, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("token create", flag.ContinueOnError)
	fs.SetOutput(stdout)
	name := fs.String("name", "", "what this token is for, shown on the settings page")
	actor := fs.String("actor", "me", `event log actor: "me", "mcp:<name>", or "plugin:<source>"`)
	scopes := fs.String("scopes", "read", "comma separated: read, write, capture, sync:<source>")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*name) == "" {
		return errors.New("a token needs a -name")
	}

	tok, err := st.CreateToken(ctx, *name, *actor, strings.Split(*scopes, ","), time.Now())
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, `
Token %q created, acting as %s with scopes %s.

  %s

This is the only time it is shown. Only its hash is stored, so it cannot be
printed again. Revoke it with: tdd token revoke %s
`, tok.Name, tok.Actor, strings.Join(tok.Scopes, ","), tok.Secret, tok.ID)
	return nil
}

func tokenList(ctx context.Context, st *store.Store, stdout io.Writer) error {
	tokens, err := st.Tokens(ctx)
	if err != nil {
		return err
	}
	if len(tokens) == 0 {
		fmt.Fprintln(stdout, "No tokens. `tdd token create -name \"tui\" -scopes read,write` makes one.")
		return nil
	}
	for _, t := range tokens {
		state := "live"
		if t.RevokedAt != nil {
			state = "revoked"
		}
		used := "never used"
		if t.LastUsedAt != nil {
			used = "last used " + *t.LastUsedAt
		}
		fmt.Fprintf(stdout, "%-26s  %-12s  %-10s  %-22s  %-8s  %s\n",
			t.ID, t.Prefix, t.Actor, strings.Join(t.Scopes, ","), state, used)
	}
	return nil
}

func tokenRevoke(ctx context.Context, st *store.Store, args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errors.New("revoke takes one token id")
	}
	if err := st.RevokeToken(ctx, args[0], time.Now()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errors.New("no live token with that id")
		}
		return err
	}
	fmt.Fprintf(stdout, "Revoked %s. Anything using it now gets a 401.\n", args[0])
	return nil
}

// accountShow prints what the settings page will show, so the state is
// inspectable before the web UI exists.
func accountShow(ctx context.Context, st *store.Store, stdout io.Writer) error {
	acct, err := st.TheAccount(ctx)
	if errors.Is(err, store.ErrNoAccount) {
		fmt.Fprintln(stdout, "No account configured. Run `tdd account create`.")
		return nil
	}
	if err != nil {
		return err
	}
	left, err := st.RecoveryCodesLeft(ctx, acct.ID)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "username        %s\ncreated         %s\nrecovery codes  %d of %d unused\n",
		acct.Username, acct.CreatedAt, left, auth.RecoveryCodeCount)
	fmt.Fprintf(stdout, "failed password %d\nfailed totp     %d\n", acct.FailedPassword, acct.FailedTOTP)
	if acct.LockedUntil != nil && acct.Locked(time.Now()) {
		fmt.Fprintf(stdout, "locked until    %s\n", *acct.LockedUntil)
	}
	return nil
}

// accountLog prints the authentication history. It is a server-side command
// rather than a route: the change feed at /api/v1/events deliberately does
// not carry auth events, because that feed is read by agents and your login
// history and the IPs behind it are not theirs to have.
func accountLog(ctx context.Context, st *store.Store, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("account log", flag.ContinueOnError)
	fs.SetOutput(stdout)
	limit := fs.Int("n", 50, "how many entries to show, newest first")
	if err := fs.Parse(args); err != nil {
		return err
	}

	events, err := st.AuthEvents(ctx, *limit)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		fmt.Fprintln(stdout, "No authentication events yet.")
		return nil
	}
	for _, e := range events {
		ip, _ := e.Patch.Meta["ip"].(string)
		if ip == "" {
			ip = "-"
		}
		fmt.Fprintf(stdout, "%-6d %-20s %-24s %-16s %s\n",
			e.Seq, e.At, e.Kind, ip, describeMeta(e.Patch.Meta))
	}
	return nil
}

// describeMeta renders the parts of an auth event that are not the IP.
func describeMeta(meta map[string]any) string {
	if len(meta) == 0 {
		return ""
	}
	keys := make([]string, 0, len(meta))
	for k := range meta {
		if k != "ip" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, meta[k]))
	}
	return strings.Join(parts, " ")
}

// usage is printed when tdd is run with a subcommand it does not know.
const usage = `tdd - the td server

  tdd                      serve
  tdd account create       create the one account, print TOTP and recovery codes
  tdd account show         show account state
  tdd account log          print the authentication history
  tdd token create         mint an API token
  tdd token list           list tokens
  tdd token revoke <id>    revoke one

Flags for the server: see tdd -h
`
