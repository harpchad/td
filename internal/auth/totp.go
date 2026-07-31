package auth

import (
	"errors"
	"net/url"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// ErrBadTOTP is returned when a code does not validate. It carries no detail
// about why.
var ErrBadTOTP = errors.New("code is not valid")

// NewTOTPSecret generates an enrollment secret and the otpauth:// URI to feed
// an authenticator app.
//
// TOTP is required at enrollment rather than offered, which is why this runs
// inside `tdd account create` and not behind a settings toggle.
func NewTOTPSecret(issuer, username string) (secret, uri string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: username,
		Period:      30,
		Digits:      otp.DigitsSix,
		Algorithm:   otp.AlgorithmSHA1,
	})
	if err != nil {
		return "", "", err
	}
	return key.Secret(), key.URL(), nil
}

// TOTPURI rebuilds the enrollment URI for an existing secret.
func TOTPURI(issuer, username, secret string) string {
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", issuer)
	v.Set("algorithm", "SHA1")
	v.Set("digits", "6")
	v.Set("period", "30")

	u := url.URL{
		Scheme:   "otpauth",
		Host:     "totp",
		Path:     "/" + issuer + ":" + username,
		RawQuery: v.Encode(),
	}
	return u.String()
}

// VerifyTOTP checks a code against a secret at the given instant.
//
// One period of skew either side is allowed, which covers a phone whose clock
// has drifted and a code typed as it rolls over. Wider windows trade the
// point of the second factor for convenience.
func VerifyTOTP(code, secret string, at time.Time) error {
	ok, err := totp.ValidateCustom(code, secret, at, totp.ValidateOpts{
		Period:    30,
		Skew:      1,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil || !ok {
		return ErrBadTOTP
	}
	return nil
}

// GenerateTOTP produces the code valid at an instant. It exists for tests and
// for `tdd account create` to print a first code so enrollment can be checked
// before the command exits.
func GenerateTOTP(secret string, at time.Time) (string, error) {
	return totp.GenerateCodeCustom(secret, at, totp.ValidateOpts{
		Period:    30,
		Skew:      1,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
}
