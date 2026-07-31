// Package auth holds the cryptographic primitives the server authenticates
// with: argon2id password hashing, TOTP, recovery codes, and API tokens. It
// deliberately knows nothing about HTTP or SQL, so each piece can be tested
// for the property it is supposed to have rather than through a request.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Params are the argon2id cost parameters.
//
// Defaults follow the OWASP recommendation of 19 MiB, two iterations, and one
// lane. On the 8 GB host this is a few tens of milliseconds per verification
// and a few tens of megabytes held briefly, which is affordable for a login
// route that is rate limited to ten attempts per minute.
type Params struct {
	Memory      uint32 // KiB
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultParams is what the server hashes with.
var DefaultParams = Params{
	Memory:      19 * 1024,
	Iterations:  2,
	Parallelism: 1,
	SaltLength:  16,
	KeyLength:   32,
}

// ErrMismatch is returned when a password does not match its hash. It is
// deliberately the same error whatever went wrong, so a caller cannot report
// the difference.
var ErrMismatch = errors.New("password does not match")

// HashPassword returns a PHC-format argon2id string carrying the parameters
// and salt alongside the digest, so a later parameter change does not
// invalidate existing hashes.
func HashPassword(password string, p Params) (string, error) {
	salt := make([]byte, p.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	digest := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, p.KeyLength)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Iterations, p.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(digest),
	), nil
}

// VerifyPassword checks a password against a PHC-format hash in constant time
// with respect to the digest.
func VerifyPassword(password, encoded string) error {
	p, salt, want, err := decodeHash(encoded)
	if err != nil {
		return err
	}
	got := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrMismatch
	}
	return nil
}

// DummyHash is a hash of a value nobody knows. Verifying against it is how the
// login route spends the same time on an unknown username as on a known one:
// without it, a failure that skips the hash returns in microseconds and
// answers the question of whether the account exists.
//
// It is computed once at startup rather than per request, because generating
// it is itself an argon2 call.
func DummyHash(p Params) (string, error) {
	filler := make([]byte, 32)
	if _, err := rand.Read(filler); err != nil {
		return "", err
	}
	return HashPassword(base64.RawStdEncoding.EncodeToString(filler), p)
}

func decodeHash(encoded string) (p Params, salt, digest []byte, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return p, nil, nil, errors.New("not an argon2id hash")
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return p, nil, nil, errors.New("unreadable argon2id version")
	}
	if version != argon2.Version {
		return p, nil, nil, fmt.Errorf("argon2id version %d is not supported", version)
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Iterations, &p.Parallelism); err != nil {
		return p, nil, nil, errors.New("unreadable argon2id parameters")
	}

	salt, err = base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil {
		return p, nil, nil, errors.New("unreadable argon2id salt")
	}
	digest, err = base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil {
		return p, nil, nil, errors.New("unreadable argon2id digest")
	}

	p.SaltLength = uint32(len(salt))
	p.KeyLength = uint32(len(digest))
	return p, salt, digest, nil
}
