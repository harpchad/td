// Package oauth holds the token format and the PKCE arithmetic for td's
// authorization server.
//
// The JWT encoding is written here rather than taken from a library for one
// reason: this code is both the only issuer and the only verifier, it accepts
// exactly one algorithm, and the entire category of algorithm-confusion bugs
// that JWT libraries keep having comes from being flexible about that. Verify
// below does not read alg to decide what to do; it reads alg to refuse
// anything that is not ES256.
package oauth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// Alg is the only signature algorithm td issues or accepts.
//
// ES256 rather than RS256: the keys are 32 bytes instead of 2048 bits, the
// signatures are 64 bytes, and a JWKS with two live keys stays small enough
// to be uninteresting. "none" is not a value this package can represent.
const Alg = "ES256"

// Errors a verifier can distinguish. A caller answers 401 for all of them,
// but the log has to say which, because "audience mismatch" and "expired"
// send an operator to completely different places.
var (
	ErrMalformed    = errors.New("token is not a JWT")
	ErrAlgorithm    = errors.New("token is not signed with ES256")
	ErrUnknownKey   = errors.New("token names a key this server does not have")
	ErrSignature    = errors.New("token signature does not verify")
	ErrExpired      = errors.New("token has expired")
	ErrNotYetValid  = errors.New("token is not valid yet")
	ErrIssuer       = errors.New("token was issued by someone else")
	ErrAudience     = errors.New("token was minted for a different resource")
	ErrMissingClaim = errors.New("token is missing a required claim")
)

// Claims is the access token payload.
//
// Aud is a single string rather than the array RFC 7519 allows. td issues
// tokens for exactly one resource, and an array invites the comparison to be
// written as "contains", which is the check that lets a token minted for
// another server be replayed here.
type Claims struct {
	Issuer    string `json:"iss"`
	Subject   string `json:"sub"`
	Audience  string `json:"aud"`
	ClientID  string `json:"client_id"`
	Scope     string `json:"scope"`
	IssuedAt  int64  `json:"iat"`
	Expires   int64  `json:"exp"`
	NotBefore int64  `json:"nbf"`
	JTI       string `json:"jti"`
}

// Scopes splits the space separated scope claim.
func (c Claims) Scopes() []string {
	return strings.Fields(c.Scope)
}

type header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

// Key is one signing key with its identifier.
type Key struct {
	Kid     string
	Private *ecdsa.PrivateKey
}

// NewKey generates a P-256 key. The kid is derived from the public point, so
// two keys cannot collide and a kid is not a secret.
func NewKey() (Key, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Key{}, err
	}
	return Key{Kid: kidOf(&priv.PublicKey), Private: priv}, nil
}

func kidOf(pub *ecdsa.PublicKey) string {
	raw, err := pub.Bytes()
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}

// coordinates splits the uncompressed public point into the two 32 byte
// halves a JWK carries. The encoding is 0x04 followed by X and Y, fixed
// width, which is why this is a slice rather than arithmetic.
func coordinates(pub *ecdsa.PublicKey) (x, y []byte) {
	raw, err := pub.Bytes()
	if err != nil || len(raw) != 65 {
		return nil, nil
	}
	return raw[1:33], raw[33:]
}

// MarshalKey writes a key as PEM, which is what the database column holds.
func MarshalKey(k Key) (string, error) {
	der, err := x509.MarshalECPrivateKey(k.Private)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})), nil
}

// ParseKey reads a key back.
func ParseKey(text string) (Key, error) {
	block, _ := pem.Decode([]byte(text))
	if block == nil {
		return Key{}, errors.New("not a PEM block")
	}
	priv, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return Key{}, err
	}
	return Key{Kid: kidOf(&priv.PublicKey), Private: priv}, nil
}

// Sign encodes and signs a token.
func Sign(k Key, claims Claims) (string, error) {
	head, err := json.Marshal(header{Alg: Alg, Typ: "at+jwt", Kid: k.Kid})
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signing := encode(head) + "." + encode(body)

	digest := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, k.Private, digest[:])
	if err != nil {
		return "", err
	}

	// Fixed width, big-endian, as JWS requires. Encoding the two integers
	// with their natural lengths produces a signature that some verifiers
	// accept and others reject, which is the worst of both.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// Verifier holds the public half of every live key.
type Verifier struct {
	// Issuer is the exact iss this server issues under.
	Issuer string
	// Audience is the resource this server is. A token whose aud is anything
	// else is refused: that is what stops a token minted for another server
	// from being replayed here, and it is the failure people actually hit.
	Audience string
	// Keys is every key that may have signed a live token, newest first.
	// Keeping the previous one is what makes rotation not a logout.
	Keys []Key
}

// Verify checks a token and returns its claims.
//
// Every check the spec names, in an order chosen so a cheap refusal happens
// before an expensive one: shape, algorithm, key, signature, then the claims.
func (v Verifier) Verify(token string, now time.Time) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, ErrMalformed
	}

	rawHead, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Claims{}, ErrMalformed
	}
	var h header
	if err := json.Unmarshal(rawHead, &h); err != nil {
		return Claims{}, ErrMalformed
	}
	// Read to refuse, never to dispatch. This is the line that makes
	// algorithm confusion impossible rather than merely unlikely.
	if h.Alg != Alg {
		return Claims{}, fmt.Errorf("%w: %q", ErrAlgorithm, h.Alg)
	}

	key, ok := v.key(h.Kid)
	if !ok {
		return Claims{}, ErrUnknownKey
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return Claims{}, ErrSignature
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&key.Private.PublicKey, digest[:], r, s) {
		return Claims{}, ErrSignature
	}

	rawBody, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, ErrMalformed
	}
	var claims Claims
	if err := json.Unmarshal(rawBody, &claims); err != nil {
		return Claims{}, ErrMalformed
	}

	switch {
	case claims.Issuer == "":
		return Claims{}, fmt.Errorf("%w: iss", ErrMissingClaim)
	case claims.Audience == "":
		return Claims{}, fmt.Errorf("%w: aud", ErrMissingClaim)
	case claims.Expires == 0:
		return Claims{}, fmt.Errorf("%w: exp", ErrMissingClaim)
	}
	if claims.Issuer != v.Issuer {
		return Claims{}, ErrIssuer
	}
	// Exact match, never a prefix and never a contains. A token for
	// https://td.example.com/mcp must not satisfy a check against
	// https://td.example.com/mcp/evil.
	if claims.Audience != v.Audience {
		return Claims{}, ErrAudience
	}
	if now.Unix() >= claims.Expires {
		return Claims{}, ErrExpired
	}
	if claims.NotBefore != 0 && now.Unix() < claims.NotBefore {
		return Claims{}, ErrNotYetValid
	}
	return claims, nil
}

func (v Verifier) key(kid string) (Key, bool) {
	for _, k := range v.Keys {
		if k.Kid == kid {
			return k, true
		}
	}
	return Key{}, false
}

// JWK is one public key in the JWKS document.
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// JWKS is the document served from the AS metadata.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// PublicJWKS builds the document. Only public halves ever leave this process.
func PublicJWKS(keys []Key) JWKS {
	out := JWKS{Keys: make([]JWK, 0, len(keys))}
	for _, k := range keys {
		x, y := coordinates(&k.Private.PublicKey)
		out.Keys = append(out.Keys, JWK{
			Kty: "EC", Crv: "P-256", Kid: k.Kid, Alg: Alg, Use: "sig",
			X: base64.RawURLEncoding.EncodeToString(x),
			Y: base64.RawURLEncoding.EncodeToString(y),
		})
	}
	return out
}

func encode(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
