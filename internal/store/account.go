package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/auth"
)

// LockoutThreshold is how many failures of one kind lock the account.
const LockoutThreshold = 5

// LockoutWindow is how long a lockout lasts.
const LockoutWindow = 15 * time.Minute

// ErrNoAccount is returned when no account has been created yet. Every route
// but the health check answers 503 in that state: account creation is a
// command on the server, never something offered over HTTP.
var ErrNoAccount = errors.New("no account configured")

// ErrLocked is returned while an account is locked out.
var ErrLocked = errors.New("account is locked")

// Account is the single account. There is one, created by
// `tdd account create`, and there is no route that makes another.
type Account struct {
	ID             string
	Username       string
	PasswordHash   string
	TOTPSecret     string
	CreatedAt      string
	FailedPassword int
	FailedTOTP     int
	LockedUntil    *string
}

// Locked reports whether the account is locked at the given instant.
func (a *Account) Locked(now time.Time) bool {
	if a.LockedUntil == nil {
		return false
	}
	until, err := time.Parse(time.RFC3339, *a.LockedUntil)
	if err != nil {
		return false
	}
	return now.Before(until)
}

// HasAccount reports whether an account exists yet.
func (s *Store) HasAccount(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM account`).Scan(&n)
	return n > 0, err
}

// CreateAccount writes the one account, its TOTP secret, and its recovery
// codes. It refuses if an account already exists: there is exactly one, and
// replacing it is not something to do by accident.
func (s *Store) CreateAccount(ctx context.Context, username, passwordHash, totpSecret string, recoveryHashes []string, now time.Time) (Account, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM account`).Scan(&n); err != nil {
		return Account{}, err
	}
	if n > 0 {
		return Account{}, errors.New("an account already exists")
	}

	acct := Account{
		ID:           NewID(),
		Username:     username,
		PasswordHash: passwordHash,
		TOTPSecret:   totpSecret,
		CreatedAt:    now.UTC().Format(time.RFC3339),
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO account (id, username, password_hash, totp_secret, created_at)
		 VALUES (?,?,?,?,?)`,
		acct.ID, acct.Username, acct.PasswordHash, acct.TOTPSecret, acct.CreatedAt); err != nil {
		return Account{}, fmt.Errorf("create account: %w", err)
	}

	for _, hash := range recoveryHashes {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO recovery_code (id, account_id, code_hash, created_at) VALUES (?,?,?,?)`,
			NewID(), acct.ID, hash, acct.CreatedAt); err != nil {
			return Account{}, err
		}
	}

	if err := appendEvent(ctx, tx, now, "me", "", api.KindAuthAccountCreated,
		api.Patch{Meta: map[string]any{"username": username}}); err != nil {
		return Account{}, err
	}
	if err := tx.Commit(); err != nil {
		return Account{}, err
	}
	return acct, nil
}

// AccountByUsername looks the account up. It returns ErrNotFound for an
// unknown username, which the login route answers identically to a wrong
// password so the two cannot be told apart.
func (s *Store) AccountByUsername(ctx context.Context, username string) (Account, error) {
	var a Account
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, totp_secret, created_at,
		        failed_password, failed_totp, locked_until
		 FROM account WHERE username = ?`, username).
		Scan(&a.ID, &a.Username, &a.PasswordHash, &a.TOTPSecret, &a.CreatedAt,
			&a.FailedPassword, &a.FailedTOTP, &a.LockedUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	return a, err
}

// TheAccount returns the single account, or ErrNoAccount.
func (s *Store) TheAccount(ctx context.Context) (Account, error) {
	var a Account
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, totp_secret, created_at,
		        failed_password, failed_totp, locked_until
		 FROM account LIMIT 1`).
		Scan(&a.ID, &a.Username, &a.PasswordHash, &a.TOTPSecret, &a.CreatedAt,
			&a.FailedPassword, &a.FailedTOTP, &a.LockedUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNoAccount
	}
	return a, err
}

// FailureKind names which counter a failed login increments. The two are
// tracked apart, so four wrong passwords and four wrong codes do not add up
// to a lockout.
type FailureKind string

// The two independently counted failure kinds.
const (
	FailurePassword FailureKind = "password"
	FailureTOTP     FailureKind = "totp"
)

// RecordFailure increments one counter and locks the account when that
// counter alone reaches the threshold. It reports whether the account is now
// locked.
func (s *Store) RecordFailure(ctx context.Context, accountID string, kind FailureKind, now time.Time) (bool, error) {
	column := "failed_password"
	if kind == FailureTOTP {
		column = "failed_totp"
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	var count int
	if err := tx.QueryRowContext(ctx,
		`UPDATE account SET `+column+` = `+column+` + 1 WHERE id = ?
		 RETURNING `+column, accountID).Scan(&count); err != nil {
		return false, err
	}

	locked := count >= LockoutThreshold
	if locked {
		until := now.Add(LockoutWindow).UTC().Format(time.RFC3339)
		if _, err := tx.ExecContext(ctx,
			`UPDATE account SET locked_until = ? WHERE id = ?`, until, accountID); err != nil {
			return false, err
		}
		if err := appendEvent(ctx, tx, now, "me", "", api.KindAuthLocked,
			api.Patch{Meta: map[string]any{"kind": string(kind), "until": until}}); err != nil {
			return false, err
		}
	}
	return locked, tx.Commit()
}

// ClearFailures resets both counters and any lockout. A completed login is
// the only thing that does this: a lockout expiring leaves the counters
// alone, so a sixth wrong password after the window locks the account again
// immediately rather than granting four more tries.
func (s *Store) ClearFailures(ctx context.Context, accountID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE account SET failed_password = 0, failed_totp = 0, locked_until = NULL WHERE id = ?`,
		accountID)
	return err
}

// RedeemRecoveryCode spends a recovery code. Each works exactly once: the
// update is conditional on used_at still being null, so two concurrent
// attempts with the same code cannot both succeed.
func (s *Store) RedeemRecoveryCode(ctx context.Context, accountID, codeHash string, now time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE recovery_code SET used_at = ?
		 WHERE account_id = ? AND code_hash = ? AND used_at IS NULL`,
		now.UTC().Format(time.RFC3339), accountID, codeHash)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return auth.ErrBadTOTP
	}
	return nil
}

// RecoveryCodesLeft counts the unused codes, for the settings page.
func (s *Store) RecoveryCodesLeft(ctx context.Context, accountID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM recovery_code WHERE account_id = ? AND used_at IS NULL`,
		accountID).Scan(&n)
	return n, err
}

// LogAuthEvent appends an auth event. Section 15 requires every one of these
// to carry the source IP, and the event table has no column for it, so it
// travels in the patch's meta.
func (s *Store) LogAuthEvent(ctx context.Context, kind, actor, ip string, extra map[string]any, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	meta := map[string]any{"ip": ip}
	for k, v := range extra {
		meta[k] = v
	}
	if err := appendEvent(ctx, tx, now, actor, "", kind, api.Patch{Meta: meta}); err != nil {
		return err
	}
	return tx.Commit()
}
