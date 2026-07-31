package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"strings"
)

// TokenPrefix marks an API token so one pasted into a chat window or a log
// line is recognizable as a credential.
const TokenPrefix = "td_"

// secretAlphabet is Crockford-style base32 without padding: unambiguous when
// read aloud and safe in a URL path or a header.
var secretAlphabet = base32.NewEncoding("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567").WithPadding(base32.NoPadding)

// ErrBadSecret is returned when a secret is malformed.
var ErrBadSecret = errors.New("malformed secret")

// NewToken mints an API token and returns the secret to show once alongside
// the hash to store and the prefix to display on the settings page.
//
// The secret is never recoverable from what is stored, which is what makes
// "a database dump contains no usable token" true rather than aspirational.
func NewToken() (secret, hash, display string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", "", err
	}
	secret = TokenPrefix + secretAlphabet.EncodeToString(raw)
	return secret, HashSecret(secret), DisplayPrefix(secret), nil
}

// HashSecret returns the hex SHA-256 of a high-entropy secret.
//
// SHA-256 rather than argon2id on purpose. A slow KDF buys resistance to
// guessing a low-entropy human-chosen value; these are 256 random bits, so
// there is nothing to guess, and a slow hash on every API request would be a
// cost with no matching benefit. Passwords get argon2id; tokens, session
// cookies, and recovery codes get this.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// EqualSecretHash compares two hex digests in constant time.
func EqualSecretHash(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// DisplayPrefix is the leading fragment shown on the settings page so a token
// can be told from its siblings without revealing it.
func DisplayPrefix(secret string) string {
	body := strings.TrimPrefix(secret, TokenPrefix)
	if len(body) > 6 {
		body = body[:6]
	}
	return TokenPrefix + body
}

// NewSessionSecret mints the opaque value that goes in the session cookie.
func NewSessionSecret() (secret, hash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	secret = secretAlphabet.EncodeToString(raw)
	return secret, HashSecret(secret), nil
}

// RecoveryCodeCount is how many codes are generated at enrollment. Ten is
// enough to survive losing a phone more than once and few enough to write on
// one line of a card.
const RecoveryCodeCount = 10

// NewRecoveryCodes mints the one-time codes shown once at enrollment, and
// returns them alongside the hashes to store.
func NewRecoveryCodes(n int) (codes, hashes []string, err error) {
	codes = make([]string, 0, n)
	hashes = make([]string, 0, n)
	for range n {
		raw := make([]byte, 10)
		if _, err := rand.Read(raw); err != nil {
			return nil, nil, err
		}
		// Grouped for transcription off a printed card.
		body := secretAlphabet.EncodeToString(raw)
		code := body[:4] + "-" + body[4:8] + "-" + body[8:12] + "-" + body[12:]
		codes = append(codes, code)
		hashes = append(hashes, HashSecret(NormalizeRecoveryCode(code)))
	}
	return codes, hashes, nil
}

// NormalizeRecoveryCode makes a typed code comparable: case and the grouping
// hyphens carry no information, and requiring them exactly would fail a
// correct code typed off a card.
func NormalizeRecoveryCode(code string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
}
