package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/harpchad/td/internal/auth"
	"github.com/harpchad/td/internal/seed"
	"github.com/harpchad/td/internal/store"
)

func scratchStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:", store.Options{Location: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// seededStore is a scratch store with the fixture's tasks in it, for the
// commands whose job is removing them.
func seededStore(t *testing.T) *store.Store {
	t.Helper()
	st := scratchStore(t)
	d, err := seed.Load(filepath.Join("..", "..", "testdata", "seed.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Seed(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	return st
}

// TestAccountCreate covers first run. There is no signup page and no route
// that makes an account, so this command is the whole of it.
func TestAccountCreate(t *testing.T) {
	st := scratchStore(t)
	ctx := context.Background()

	var out bytes.Buffer
	stdin := strings.NewReader("a long enough password\na long enough password\n")
	if err := accountCreate(ctx, st, []string{"-username", "chad"}, stdin, &out); err != nil {
		t.Fatalf("account create: %v", err)
	}

	acct, err := st.TheAccount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if acct.Username != "chad" {
		t.Errorf("username = %q", acct.Username)
	}

	printed := out.String()

	// The enrolment URI and the recovery codes are shown exactly once,
	// because nothing can print them again.
	if !strings.Contains(printed, "otpauth://totp/") {
		t.Error("no TOTP enrolment URI was printed")
	}
	if !strings.Contains(printed, acct.TOTPSecret) {
		t.Error("the TOTP secret was not printed, so the account cannot be enrolled")
	}

	left, err := st.RecoveryCodesLeft(ctx, acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if left != auth.RecoveryCodeCount {
		t.Errorf("%d recovery codes stored, want %d", left, auth.RecoveryCodeCount)
	}

	// The password is nowhere: not printed, not stored.
	if strings.Contains(printed, "a long enough password") {
		t.Error("the password was echoed")
	}
	if strings.Contains(acct.PasswordHash, "a long enough password") {
		t.Error("the stored hash contains the password")
	}
	if err := auth.VerifyPassword("a long enough password", acct.PasswordHash); err != nil {
		t.Errorf("the stored hash does not verify the password: %v", err)
	}
}

// TestAccountCreateRefusesASecondAccount covers the "one account" rule. There
// is one, and this command does not quietly replace it.
func TestAccountCreateRefusesASecondAccount(t *testing.T) {
	st := scratchStore(t)
	ctx := context.Background()

	var out bytes.Buffer
	first := strings.NewReader("a long enough password\na long enough password\n")
	if err := accountCreate(ctx, st, []string{"-username", "chad"}, first, &out); err != nil {
		t.Fatal(err)
	}

	second := strings.NewReader("another long password\nanother long password\n")
	err := accountCreate(ctx, st, []string{"-username", "someone-else"}, second, &out)
	if err == nil {
		t.Fatal("a second account was created")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %v, it should say why", err)
	}
}

func TestAccountCreateValidatesInput(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		stdin string
		want  string
	}{
		{
			name: "a short password", args: []string{"-username", "chad"},
			stdin: "short\nshort\n", want: "at least",
		},
		{
			name: "two different passwords", args: []string{"-username", "chad"},
			stdin: "a long enough password\na different password\n", want: "do not match",
		},
		{
			name: "no username", args: nil,
			stdin: "\na long enough password\na long enough password\n", want: "username is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := scratchStore(t)
			var out bytes.Buffer
			err := accountCreate(context.Background(), st, tc.args, strings.NewReader(tc.stdin), &out)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
			if exists, _ := st.HasAccount(context.Background()); exists {
				t.Error("a rejected input still created an account")
			}
		})
	}
}

// TestTokenLifecycle covers minting, listing, and revoking. The secret is
// shown once and is not recoverable afterwards.
func TestTokenLifecycle(t *testing.T) {
	st := scratchStore(t)
	ctx := context.Background()

	var out bytes.Buffer
	err := tokenCommand(ctx, st, []string{
		"create", "-name", "tui", "-scopes", "read,write", "-actor", "me",
	}, &out)
	if err != nil {
		t.Fatal(err)
	}

	printed := out.String()
	if !strings.Contains(printed, auth.TokenPrefix) {
		t.Fatal("no token was printed")
	}
	if !strings.Contains(printed, "only time it is shown") {
		t.Error("the output does not say the secret cannot be printed again")
	}

	tokens, err := st.Tokens(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 {
		t.Fatalf("%d tokens stored, want 1", len(tokens))
	}
	if tokens[0].Secret != "" {
		t.Error("listing tokens returned a secret")
	}

	// Revoking is what the settings page's button does.
	out.Reset()
	if err := tokenCommand(ctx, st, []string{"revoke", tokens[0].ID}, &out); err != nil {
		t.Fatal(err)
	}
	tokens, err = st.Tokens(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tokens[0].RevokedAt == nil {
		t.Error("the token was not revoked")
	}

	// A second revoke of the same token is an error rather than a silent
	// success, so a typo in an id does not look like it worked.
	if err := tokenCommand(ctx, st, []string{"revoke", tokens[0].ID}, &out); err == nil {
		t.Error("revoking an already-revoked token succeeded")
	}
}

func TestTokenCreateValidatesActorAndScopes(t *testing.T) {
	st := scratchStore(t)
	var out bytes.Buffer

	bad := [][]string{
		{"create", "-name", "x", "-actor", "root"},
		{"create", "-name", "x", "-actor", "mcp:"},
		{"create", "-name", "x", "-actor", "mcp:two words"},
		{"create", "-name", "x", "-scopes", "admin"},
		{"create", "-name", "x", "-scopes", ""},
		{"create", "-name", "", "-scopes", "read"},
	}
	for _, args := range bad {
		if err := tokenCommand(context.Background(), st, args, &out); err == nil {
			t.Errorf("token %v was accepted", args)
		}
	}

	// The forms that are meant to work.
	good := [][]string{
		{"create", "-name", "claude", "-actor", "mcp:claude", "-scopes", "read,capture"},
		{"create", "-name", "planner", "-actor", "plugin:planner", "-scopes", "sync:planner"},
	}
	for _, args := range good {
		if err := tokenCommand(context.Background(), st, args, &out); err != nil {
			t.Errorf("token %v was rejected: %v", args, err)
		}
	}
}

// TestAccountLogReadsTheAuthHistory covers the audit trail. It is a
// server-side command because the change feed at /api/v1/events deliberately
// does not carry auth events.
func TestAccountLogReadsTheAuthHistory(t *testing.T) {
	st := scratchStore(t)
	ctx := context.Background()

	var out bytes.Buffer
	if err := accountCreate(ctx, st, []string{"-username", "chad"},
		strings.NewReader("a long enough password\na long enough password\n"), &out); err != nil {
		t.Fatal(err)
	}
	if err := st.LogAuthEvent(ctx, "auth.login_failed", "anonymous", "203.0.113.9",
		map[string]any{"stage": "password"}, time.Now()); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if err := accountLog(ctx, st, nil, &out); err != nil {
		t.Fatal(err)
	}
	printed := out.String()

	if !strings.Contains(printed, "auth.login_failed") {
		t.Error("the log does not show a failed login")
	}
	if !strings.Contains(printed, "203.0.113.9") {
		t.Error("the log does not show the source IP, which is the point of keeping it")
	}
	if !strings.Contains(printed, "auth.account_created") {
		t.Error("the log does not show account creation")
	}

	// And none of it is in the change feed.
	events, err := st.Events(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if strings.HasPrefix(e.Kind, "auth.") {
			t.Errorf("the change feed carries %s", e.Kind)
		}
	}
}

// TestResetAsksBeforeItDestroysAnything. This is the only operation in the
// product that removes something permanently, so a reflexive keystroke must
// not be enough to do it.
func TestResetAsksBeforeItDestroysAnything(t *testing.T) {
	ctx := context.Background()

	for _, answer := range []string{"y\n", "yes\n", "\n", "DELETE\n", "no\n"} {
		st := seededStore(t)
		var out bytes.Buffer
		if err := resetTasks(ctx, st, "test.db", nil, strings.NewReader(answer), &out); err != nil {
			t.Fatal(err)
		}
		left, err := st.List(ctx, "", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if len(left) == 0 {
			t.Errorf("answering %q deleted everything", strings.TrimSpace(answer))
		}
		if !strings.Contains(out.String(), "Left alone") {
			t.Errorf("answering %q did not say it left things alone", strings.TrimSpace(answer))
		}
	}

	// Only the word itself goes through.
	st := seededStore(t)
	var out bytes.Buffer
	if err := resetTasks(ctx, st, "test.db", nil, strings.NewReader("delete\n"), &out); err != nil {
		t.Fatal(err)
	}
	left, err := st.List(ctx, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("%d tasks left after confirming", len(left))
	}
	if !strings.Contains(out.String(), "Removed") {
		t.Errorf("no summary of what went:\n%s", out.String())
	}
}

// TestResetSaysWhatItWillKeep, because the reason it exists is that the blunt
// alternative destroys the account and the connections too.
func TestResetSaysWhatItWillKeep(t *testing.T) {
	st := seededStore(t)
	var out bytes.Buffer
	if err := resetTasks(context.Background(), st, "/data/td.db", nil,
		strings.NewReader("no\n"), &out); err != nil {
		t.Fatal(err)
	}

	prompt := out.String()
	for _, want := range []string{"/data/td.db", "Kept:", "account", "identity mappings", "plugin settings"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt does not mention %q:\n%s", want, prompt)
		}
	}
}
