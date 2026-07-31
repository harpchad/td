package oauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"
)

// MethodS256 is the only PKCE challenge method td accepts.
//
// OAuth 2.1 removes `plain`, and the MCP specification requires S256. `plain`
// is not weak arithmetic, it is no arithmetic: the verifier is the challenge,
// so anyone who intercepted the authorization request can complete the token
// exchange. Accepting it "for compatibility" would defeat the whole exchange.
const MethodS256 = "S256"

// PKCE errors, kept apart because they mean different things to an operator.
var (
	ErrChallengeMissing = errors.New("code_challenge is required")
	ErrChallengePlain   = errors.New("code_challenge_method must be S256; plain is not accepted")
	ErrVerifierLength   = errors.New("code_verifier must be 43 to 128 characters")
	ErrVerifierMismatch = errors.New("code_verifier does not match the challenge")
)

// CheckChallenge validates the authorization request's PKCE parameters.
//
// A missing method is refused rather than defaulting to plain, which is what
// RFC 7636 says the default is. Inheriting that default here would mean a
// client that simply omits the parameter gets the mode this server is
// supposed to reject.
func CheckChallenge(challenge, method string) error {
	if strings.TrimSpace(challenge) == "" {
		return ErrChallengeMissing
	}
	if method != MethodS256 {
		return ErrChallengePlain
	}
	// The challenge is base64url of a SHA-256, so it is always 43 characters.
	// Anything else is a client that computed something other than what it
	// will later have to prove.
	if len(challenge) != 43 {
		return ErrVerifierMismatch
	}
	if _, err := base64.RawURLEncoding.DecodeString(challenge); err != nil {
		return ErrVerifierMismatch
	}
	return nil
}

// VerifyChallenge checks a code_verifier against the stored challenge.
func VerifyChallenge(verifier, challenge string) error {
	// RFC 7636 fixes the length. A short verifier is a weak one, and the
	// bound is cheap to enforce here and impossible to enforce later.
	if len(verifier) < 43 || len(verifier) > 128 {
		return ErrVerifierLength
	}
	sum := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])

	// Constant time, because a timing oracle on this comparison turns a
	// guessing attack into a byte-at-a-time one.
	if subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) != 1 {
		return ErrVerifierMismatch
	}
	return nil
}
