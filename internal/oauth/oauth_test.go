package oauth_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/harpchad/td/internal/oauth"
)

const (
	issuer   = "https://td.example.com"
	resource = "https://td.example.com/mcp"
)

func newVerifier(t *testing.T, keys ...oauth.Key) oauth.Verifier {
	t.Helper()
	return oauth.Verifier{Issuer: issuer, Audience: resource, Keys: keys}
}

func claims(now time.Time) oauth.Claims {
	return oauth.Claims{
		Issuer: issuer, Subject: "me", Audience: resource,
		ClientID: "client-1", Scope: "td:read td:capture",
		IssuedAt: now.Unix(), NotBefore: now.Unix(),
		Expires: now.Add(time.Hour).Unix(), JTI: "jti-1",
	}
}

func mustKey(t *testing.T) oauth.Key {
	t.Helper()
	k, err := oauth.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// TestSignAndVerifyRoundTrip is the ordinary path.
func TestSignAndVerifyRoundTrip(t *testing.T) {
	key := mustKey(t)
	now := time.Now()

	token, err := oauth.Sign(key, claims(now))
	if err != nil {
		t.Fatal(err)
	}
	got, err := newVerifier(t, key).Verify(token, now)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.Subject != "me" || got.ClientID != "client-1" {
		t.Errorf("claims = %+v", got)
	}
	if len(got.Scopes()) != 2 {
		t.Errorf("scopes = %v", got.Scopes())
	}
}

// TestAudienceIsAnExactMatch is the failure people actually hit, and the one
// that stops a token minted for another server from being replayed here.
func TestAudienceIsAnExactMatch(t *testing.T) {
	key := mustKey(t)
	now := time.Now()

	for _, aud := range []string{
		"https://td.example.com",               // the issuer, not the resource
		"https://td.example.com/mcp/",          // trailing slash
		"https://td.example.com/mcp/evil",      // a prefix match would pass
		"https://evil.example.com/mcp",         // another server entirely
		"https://td.example.com/mcp https://x", // a space separated list
		"",
	} {
		c := claims(now)
		c.Audience = aud
		token, err := oauth.Sign(key, c)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := newVerifier(t, key).Verify(token, now); err == nil {
			t.Errorf("aud %q was accepted", aud)
		}
	}
}

// TestAlgorithmConfusionIsImpossible covers the whole reason this encoding is
// written here rather than taken from a library. alg is read to refuse, never
// to dispatch.
func TestAlgorithmConfusionIsImpossible(t *testing.T) {
	key := mustKey(t)
	now := time.Now()

	token, err := oauth.Sign(key, claims(now))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")

	for _, alg := range []string{"none", "None", "HS256", "RS256", "ES384", ""} {
		head, err := json.Marshal(map[string]string{
			"alg": alg, "typ": "at+jwt", "kid": key.Kid,
		})
		if err != nil {
			t.Fatal(err)
		}
		forged := base64.RawURLEncoding.EncodeToString(head) + "." + parts[1] + "." + parts[2]
		_, err = newVerifier(t, key).Verify(forged, now)
		if !errors.Is(err, oauth.ErrAlgorithm) {
			t.Errorf("alg %q: err = %v, want ErrAlgorithm", alg, err)
		}
	}

	// And an unsigned token with the signature simply removed.
	if _, err := newVerifier(t, key).Verify(parts[0]+"."+parts[1]+".", now); err == nil {
		t.Error("a token with an empty signature verified")
	}
}

// TestATamperedPayloadDoesNotVerify, which is what the signature is for.
func TestATamperedPayloadDoesNotVerify(t *testing.T) {
	key := mustKey(t)
	now := time.Now()

	token, err := oauth.Sign(key, claims(now))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")

	// Widen the scope, keeping the original signature.
	c := claims(now)
	c.Scope = "td:read td:capture td:write"
	body, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	forged := parts[0] + "." + base64.RawURLEncoding.EncodeToString(body) + "." + parts[2]

	if _, err := newVerifier(t, key).Verify(forged, now); !errors.Is(err, oauth.ErrSignature) {
		t.Errorf("err = %v, want ErrSignature", err)
	}
}

// TestRotationIsNotALogout covers the two-live-keys requirement. A token
// signed by the outgoing key keeps verifying while it is still in the set.
func TestRotationIsNotALogout(t *testing.T) {
	old, fresh := mustKey(t), mustKey(t)
	now := time.Now()

	token, err := oauth.Sign(old, claims(now))
	if err != nil {
		t.Fatal(err)
	}

	// New key signs, both verify.
	if _, err := newVerifier(t, fresh, old).Verify(token, now); err != nil {
		t.Errorf("a token from the previous key was rejected: %v", err)
	}
	// Once the old key is dropped, it stops.
	if _, err := newVerifier(t, fresh).Verify(token, now); !errors.Is(err, oauth.ErrUnknownKey) {
		t.Errorf("err = %v, want ErrUnknownKey", err)
	}
}

// TestTheClockClaimsAreChecked covers exp and nbf.
func TestTheClockClaimsAreChecked(t *testing.T) {
	key := mustKey(t)
	now := time.Now()

	token, err := oauth.Sign(key, claims(now))
	if err != nil {
		t.Fatal(err)
	}
	v := newVerifier(t, key)

	if _, err := v.Verify(token, now.Add(2*time.Hour)); !errors.Is(err, oauth.ErrExpired) {
		t.Errorf("err = %v, want ErrExpired", err)
	}
	if _, err := v.Verify(token, now.Add(-time.Minute)); !errors.Is(err, oauth.ErrNotYetValid) {
		t.Errorf("err = %v, want ErrNotYetValid", err)
	}

	// Exactly at exp is expired: the token is valid up to but not including
	// it, which is what "expires at" means.
	c := claims(now)
	atExpiry := time.Unix(c.Expires, 0)
	if _, err := v.Verify(token, atExpiry); !errors.Is(err, oauth.ErrExpired) {
		t.Errorf("a token exactly at exp was accepted: %v", err)
	}
}

// TestTheIssuerIsChecked, so a token from another authorization server that
// happens to claim this audience is still refused.
func TestTheIssuerIsChecked(t *testing.T) {
	key := mustKey(t)
	now := time.Now()

	c := claims(now)
	c.Issuer = "https://evil.example.com"
	token, err := oauth.Sign(key, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newVerifier(t, key).Verify(token, now); !errors.Is(err, oauth.ErrIssuer) {
		t.Errorf("err = %v, want ErrIssuer", err)
	}
}

// TestAMissingRequiredClaimIsRefused rather than defaulted.
func TestAMissingRequiredClaimIsRefused(t *testing.T) {
	key := mustKey(t)
	now := time.Now()

	for name, mutate := range map[string]func(*oauth.Claims){
		"iss": func(c *oauth.Claims) { c.Issuer = "" },
		"aud": func(c *oauth.Claims) { c.Audience = "" },
		"exp": func(c *oauth.Claims) { c.Expires = 0 },
	} {
		c := claims(now)
		mutate(&c)
		token, err := oauth.Sign(key, c)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := newVerifier(t, key).Verify(token, now); err == nil {
			t.Errorf("a token with no %s verified", name)
		}
	}
}

// TestKeysRoundTripThroughPEM, which is what the database column holds.
func TestKeysRoundTripThroughPEM(t *testing.T) {
	key := mustKey(t)

	text, err := oauth.MarshalKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "PUBLIC") {
		t.Error("the private key marshalled as a public one")
	}

	back, err := oauth.ParseKey(text)
	if err != nil {
		t.Fatal(err)
	}
	if back.Kid != key.Kid {
		t.Errorf("kid = %q, want %q: the identifier is derived, so it must survive", back.Kid, key.Kid)
	}

	// A token signed before the round trip verifies after it.
	now := time.Now()
	token, err := oauth.Sign(key, claims(now))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newVerifier(t, back).Verify(token, now); err != nil {
		t.Errorf("verify after a PEM round trip: %v", err)
	}
}

// TestTheJWKSCarriesOnlyPublicHalves. Publishing a private key is the kind of
// mistake that only gets found by someone else.
func TestTheJWKSCarriesOnlyPublicHalves(t *testing.T) {
	keys := []oauth.Key{mustKey(t), mustKey(t)}

	doc := oauth.PublicJWKS(keys)
	if len(doc.Keys) != 2 {
		t.Fatalf("%d keys, want both", len(doc.Keys))
	}

	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	// The private scalar as base64url must not appear anywhere in it.
	for _, k := range keys {
		raw, err := k.Private.Bytes()
		if err != nil {
			t.Fatal(err)
		}
		d := base64.RawURLEncoding.EncodeToString(raw)
		if strings.Contains(string(body), d) {
			t.Fatal("the JWKS contains a private key")
		}
	}
	if strings.Contains(string(body), `"d"`) {
		t.Fatal("the JWKS has a d parameter")
	}

	for i, jwk := range doc.Keys {
		if jwk.Kid != keys[i].Kid {
			t.Errorf("kid %d = %q", i, jwk.Kid)
		}
		if jwk.Alg != oauth.Alg || jwk.Crv != "P-256" || jwk.Use != "sig" {
			t.Errorf("jwk = %+v", jwk)
		}
		// Both coordinates are fixed width, which some clients require.
		for _, coord := range []string{jwk.X, jwk.Y} {
			raw, err := base64.RawURLEncoding.DecodeString(coord)
			if err != nil || len(raw) != 32 {
				t.Errorf("coordinate %q is %d bytes", coord, len(raw))
			}
		}
	}
}

// TestPKCERejectsPlainAndMissing is a security assertion from section 15,
// stated there as "/authorize rejects PKCE plain and rejects a missing
// challenge".
func TestPKCERejectsPlainAndMissing(t *testing.T) {
	valid := challengeFor("a-verifier-long-enough-to-be-legal-0123456789")

	if err := oauth.CheckChallenge("", oauth.MethodS256); !errors.Is(err, oauth.ErrChallengeMissing) {
		t.Errorf("empty challenge: err = %v", err)
	}
	if err := oauth.CheckChallenge("   ", oauth.MethodS256); !errors.Is(err, oauth.ErrChallengeMissing) {
		t.Errorf("blank challenge: err = %v", err)
	}
	for _, method := range []string{"plain", "PLAIN", "", "s256", "S512"} {
		if err := oauth.CheckChallenge(valid, method); !errors.Is(err, oauth.ErrChallengePlain) {
			t.Errorf("method %q: err = %v, want ErrChallengePlain", method, err)
		}
	}
	// A missing method is refused rather than defaulting to plain, which is
	// what RFC 7636 says the default is.
	if err := oauth.CheckChallenge(valid, oauth.MethodS256); err != nil {
		t.Errorf("a valid S256 challenge was refused: %v", err)
	}
}

// TestPKCEVerifierMustMatch is the exchange itself.
func TestPKCEVerifierMustMatch(t *testing.T) {
	verifier := "a-verifier-long-enough-to-be-legal-0123456789"
	challenge := challengeFor(verifier)

	if err := oauth.VerifyChallenge(verifier, challenge); err != nil {
		t.Fatalf("the right verifier was refused: %v", err)
	}
	if err := oauth.VerifyChallenge(verifier+"x", challenge); !errors.Is(err, oauth.ErrVerifierMismatch) {
		t.Errorf("a wrong verifier was accepted: %v", err)
	}

	// The length bound is RFC 7636's, and a short verifier is a weak one.
	for _, short := range []string{"", "abc", strings.Repeat("a", 42)} {
		if err := oauth.VerifyChallenge(short, challengeFor(short)); !errors.Is(err, oauth.ErrVerifierLength) {
			t.Errorf("a %d character verifier was accepted", len(short))
		}
	}
	if err := oauth.VerifyChallenge(strings.Repeat("a", 129), challengeFor(strings.Repeat("a", 129))); !errors.Is(err, oauth.ErrVerifierLength) {
		t.Error("a 129 character verifier was accepted")
	}
}

func challengeFor(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
