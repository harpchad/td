package auth_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/harpchad/td/internal/auth"
)

// testParams keep the suite quick where the cost of hashing is not what is
// being tested. Anything asserting a timing property uses DefaultParams.
var testParams = auth.Params{
	Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
}

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := auth.HashPassword("correct horse battery staple", testParams)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.VerifyPassword("correct horse battery staple", hash); err != nil {
		t.Errorf("the right password did not verify: %v", err)
	}
	if err := auth.VerifyPassword("correct horse battery stapl", hash); !errors.Is(err, auth.ErrMismatch) {
		t.Errorf("the wrong password gave %v, want ErrMismatch", err)
	}
}

// TestPasswordHashIsSalted checks that two accounts with the same password do
// not share a digest, which is what stops one dump revealing both.
func TestPasswordHashIsSalted(t *testing.T) {
	a, err := auth.HashPassword("same password", testParams)
	if err != nil {
		t.Fatal(err)
	}
	b, err := auth.HashPassword("same password", testParams)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("two hashes of the same password are identical, so it is unsalted")
	}
}

// TestPasswordHashCarriesItsParameters covers the reason for the PHC format:
// raising the cost later must not invalidate an existing hash.
func TestPasswordHashCarriesItsParameters(t *testing.T) {
	hash, err := auth.HashPassword("hunter2", testParams)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("hash = %q, want a PHC argon2id string", hash)
	}
	// Verification reads the parameters out of the hash, so a hash written
	// under the light test parameters still verifies against a process
	// configured for the heavy production ones.
	if err := auth.VerifyPassword("hunter2", hash); err != nil {
		t.Errorf("verifying under different ambient parameters failed: %v", err)
	}
}

func TestVerifyRejectsAMalformedHash(t *testing.T) {
	for _, bad := range []string{
		"", "not a hash", "$argon2id$", "$bcrypt$v=19$m=1,t=1,p=1$c2FsdA$ZGlnZXN0",
		"$argon2id$v=19$m=1,t=1,p=1$!!!$ZGlnZXN0",
	} {
		if err := auth.VerifyPassword("anything", bad); err == nil {
			t.Errorf("VerifyPassword(%q) returned nil, want an error", bad)
		}
	}
}

// TestDummyHashCostsTheSameAsAReal one is the mechanism behind the
// no-enumeration assertion: an unknown username has to spend the same time as
// a known one, and it can only do that by verifying against something.
func TestDummyHashVerifiesAndFails(t *testing.T) {
	dummy, err := auth.DummyHash(testParams)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.VerifyPassword("any password at all", dummy); !errors.Is(err, auth.ErrMismatch) {
		t.Errorf("got %v, want a mismatch: the dummy hash must never accept anything", err)
	}
}

func TestTokenIsPrefixedAndUnrecoverable(t *testing.T) {
	secret, hash, display, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, auth.TokenPrefix) {
		t.Errorf("secret = %q, want the %s prefix", secret, auth.TokenPrefix)
	}
	if strings.Contains(hash, secret) || strings.Contains(secret, hash) {
		t.Error("the stored hash contains the secret")
	}
	if !strings.HasPrefix(secret, display) {
		t.Errorf("display prefix %q is not a prefix of the secret", display)
	}
	if len(display) >= len(secret) {
		t.Error("the display prefix reveals the whole secret")
	}
	if auth.HashSecret(secret) != hash {
		t.Error("hashing the secret again did not reproduce the stored hash")
	}
}

func TestTokensAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		secret, _, _, err := auth.NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if seen[secret] {
			t.Fatal("NewToken repeated a secret")
		}
		seen[secret] = true
	}
}

func TestRecoveryCodesAreDistinctAndNormalize(t *testing.T) {
	codes, hashes, err := auth.NewRecoveryCodes(auth.RecoveryCodeCount)
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != auth.RecoveryCodeCount || len(hashes) != auth.RecoveryCodeCount {
		t.Fatalf("got %d codes and %d hashes", len(codes), len(hashes))
	}

	seen := map[string]bool{}
	for i, code := range codes {
		if seen[code] {
			t.Fatal("a recovery code repeated")
		}
		seen[code] = true

		// A code typed off a card, in the wrong case and without the grouping
		// hyphens, has to match.
		typed := strings.ToLower(strings.ReplaceAll(code, "-", ""))
		if got := auth.HashSecret(auth.NormalizeRecoveryCode(typed)); got != hashes[i] {
			t.Errorf("code %d did not match when typed as %q", i, typed)
		}
	}
}

func TestTOTPRoundTrip(t *testing.T) {
	secret, uri, err := auth.NewTOTPSecret("td", "chad")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(uri, "otpauth://totp/") {
		t.Errorf("uri = %q, want an otpauth:// URI", uri)
	}

	at := time.Date(2026, 8, 3, 10, 30, 0, 0, time.UTC)
	code, err := auth.GenerateTOTP(secret, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.VerifyTOTP(code, secret, at); err != nil {
		t.Errorf("the current code did not verify: %v", err)
	}
	if err := auth.VerifyTOTP("000000", secret, at); !errors.Is(err, auth.ErrBadTOTP) {
		t.Errorf("got %v, want ErrBadTOTP", err)
	}
}

// TestTOTPSkew locks the window at one period either side: enough for a
// drifting phone, not enough to make the second factor decorative.
func TestTOTPSkew(t *testing.T) {
	secret, _, err := auth.NewTOTPSecret("td", "chad")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 3, 10, 30, 0, 0, time.UTC)
	code, err := auth.GenerateTOTP(secret, at)
	if err != nil {
		t.Fatal(err)
	}

	for _, offset := range []time.Duration{-30 * time.Second, 0, 30 * time.Second} {
		if err := auth.VerifyTOTP(code, secret, at.Add(offset)); err != nil {
			t.Errorf("offset %s should be inside the window: %v", offset, err)
		}
	}
	for _, offset := range []time.Duration{-90 * time.Second, 90 * time.Second} {
		if err := auth.VerifyTOTP(code, secret, at.Add(offset)); err == nil {
			t.Errorf("offset %s should be outside the window", offset)
		}
	}
}

// TestTOTPURIRebuildMatches checks that re-deriving the enrollment URI for a
// stored secret produces something an authenticator accepts, so the setup
// command can print it again without holding the original key object.
func TestTOTPURIRebuildMatches(t *testing.T) {
	secret, _, err := auth.NewTOTPSecret("td", "chad")
	if err != nil {
		t.Fatal(err)
	}
	uri := auth.TOTPURI("td", "chad", secret)
	if !strings.Contains(uri, "secret="+secret) {
		t.Errorf("uri = %q, want it to carry the secret", uri)
	}
	if !strings.Contains(uri, "period=30") || !strings.Contains(uri, "digits=6") {
		t.Errorf("uri = %q, want the parameters VerifyTOTP uses", uri)
	}
}
