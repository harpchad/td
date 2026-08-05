// Package msgraph holds the Microsoft identity plumbing the Planner mirror
// needs: a device code sign-in and a refresh token that renews itself.
//
// Device code rather than a client secret, for a reason worth writing down.
// Microsoft's own documentation currently disagrees with itself about whether
// Planner supports application permissions: the permissions reference says it
// does not, and the plannerPlan endpoint pages list Tasks.Read.All as an
// application permission. That looks like a per-endpoint rollout in progress.
// A delegated flow works whichever way that lands, and it also keeps td's
// model intact: one user, and everything acts as them.
//
// The interactive step happens once. After that the refresh token renews on
// every use, so a schedule that runs more often than the refresh token's
// inactivity window keeps itself alive indefinitely.
package msgraph

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultAuthority is the Microsoft identity platform. The tenant segment is
// filled in per configuration: "organizations" for a work account, or a
// specific tenant id.
const DefaultAuthority = "https://login.microsoftonline.com"

// Scopes are what the Planner mirror needs and nothing more.
//
// Tasks.Read to read plans and tasks, User.ReadBasic.All to turn the object
// ids inside a task into names and addresses, and offline_access, which is
// what makes a refresh token come back at all. Asking for write access would
// be asking for the ability to do the thing section 8 says v1 does not do.
var Scopes = []string{
	"https://graph.microsoft.com/Tasks.Read",
	"https://graph.microsoft.com/User.ReadBasic.All",
	"offline_access",
	// openid and profile are what make an id token come back, and the id
	// token is where the signed-in account's object id is. Without them the
	// mirror cannot tell which assignments are yours, and the settings page
	// cannot say who it is connected as.
	"openid",
	"profile",
}

// ErrAuthorizationPending is the poll result meaning the person has not
// finished signing in yet. It is not a failure and must not be reported as
// one.
var ErrAuthorizationPending = errors.New("waiting for the sign-in to finish")

// ErrSlowDown asks the poller to back off.
var ErrSlowDown = errors.New("polling too fast")

// ErrDeclined means the sign-in was refused or the code expired.
var ErrDeclined = errors.New("the sign-in was declined or the code expired")

// Config is what a plugin needs to talk to Microsoft.
type Config struct {
	// TenantID is a tenant GUID, a domain, or "organizations". A specific
	// tenant is better: it stops an account from another directory signing in
	// by accident and then failing confusingly at the first Graph call.
	TenantID string `json:"tenant_id"`
	// ClientID is an app registration with public client flows enabled. There
	// is no secret: a device code flow is a public client by definition, and
	// a secret stored here would be one more thing to leak for no benefit.
	ClientID string `json:"client_id"`
	// Authority overrides the login host for a sovereign cloud.
	Authority string `json:"authority,omitempty"`
	// Scopes are what this connection asks for. Empty means the Planner set
	// above, which is what every caller wanted until a second plugin existed.
	//
	// Per connection rather than per server on purpose. The mail plugin needs
	// Mail.Read and no more, the mirror needs Tasks.Read and no more, and a
	// single credential carrying the union would let either one read what the
	// other was granted. The cost is signing in once per plugin, which is the
	// honest price of least privilege rather than an oversight.
	Scopes []string `json:"scopes,omitempty"`
}

// scopes is what to ask the identity platform for.
func (c Config) scopes() []string {
	if len(c.Scopes) > 0 {
		return c.Scopes
	}
	return Scopes
}

func (c Config) authority() string {
	if c.Authority != "" {
		return strings.TrimRight(c.Authority, "/")
	}
	return DefaultAuthority
}

func (c Config) tenant() string {
	if c.TenantID == "" {
		return "organizations"
	}
	return c.TenantID
}

// DeviceCode is what to show a person so they can sign in elsewhere.
type DeviceCode struct {
	// UserCode is the short string typed into the verification page.
	UserCode string `json:"user_code"`
	// VerificationURI is where to type it, as Microsoft returned it. It is
	// not hardcoded because it differs per cloud.
	VerificationURI string `json:"verification_uri"`
	// Message is Microsoft's own wording, which is worth showing verbatim: it
	// is what their documentation and support articles will match.
	Message string `json:"message"`

	// DeviceCode is the secret half, polled with and never displayed.
	DeviceCode string `json:"device_code"`
	Interval   int    `json:"interval"`
	ExpiresIn  int    `json:"expires_in"`
}

// Credential is what gets stored after a successful sign-in.
type Credential struct {
	Config       Config `json:"config"`
	RefreshToken string `json:"refresh_token"`
	// Account is who signed in, shown on the settings page so it is obvious
	// which identity the mirror is reading as.
	Account string `json:"account,omitempty"`
	// UserID is the signed-in account's directory object id. Planner keys its
	// assignments by that id and nothing else, so it is what "assigned to me"
	// is decided on. It comes off the id token, which costs no extra call and
	// no extra permission.
	UserID string `json:"user_id,omitempty"`
	// AccessToken and ExpiresAt cache the short-lived half, so a run every
	// fifteen minutes is not a token request every fifteen minutes.
	AccessToken string `json:"access_token,omitempty"`
	ExpiresAt   string `json:"expires_at,omitempty"`
}

// Client talks to the identity platform.
type Client struct {
	HTTP *http.Client
}

// New builds a client with a timeout short enough not to hold a scheduler
// tick open.
func New() *Client {
	return &Client{HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// StartDeviceCode begins a sign-in.
func (c *Client) StartDeviceCode(ctx context.Context, cfg Config) (DeviceCode, error) {
	form := url.Values{
		"client_id": {cfg.ClientID},
		"scope":     {strings.Join(cfg.scopes(), " ")},
	}
	body, err := c.post(ctx, cfg.authority()+"/"+cfg.tenant()+"/oauth2/v2.0/devicecode", form)
	if err != nil {
		return DeviceCode{}, err
	}

	var out DeviceCode
	if err := json.Unmarshal(body, &out); err != nil {
		return DeviceCode{}, err
	}
	if out.DeviceCode == "" || out.UserCode == "" {
		return DeviceCode{}, fmt.Errorf("microsoft returned no device code: %s", truncate(body))
	}
	if out.Interval <= 0 {
		out.Interval = 5
	}
	return out, nil
}

// tokenResponse is the shape both grants answer with.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	IDToken      string `json:"id_token"`

	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// PollDeviceCode asks once whether the sign-in has completed.
//
// One attempt, not a loop. Who waits and for how long is the caller's
// decision, and burying a multi-minute loop inside a library is how a request
// handler ends up holding a connection open for ten minutes.
func (c *Client) PollDeviceCode(ctx context.Context, cfg Config, deviceCode string) (Credential, error) {
	form := url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"client_id":   {cfg.ClientID},
		"device_code": {deviceCode},
	}
	body, err := c.post(ctx, cfg.authority()+"/"+cfg.tenant()+"/oauth2/v2.0/token", form)

	var res tokenResponse
	// The error responses carry a JSON body that says which kind they are, so
	// the body is read whether or not the status said failure.
	if len(body) > 0 {
		_ = json.Unmarshal(body, &res)
	}
	switch res.Error {
	case "authorization_pending":
		return Credential{}, ErrAuthorizationPending
	case "slow_down":
		return Credential{}, ErrSlowDown
	case "authorization_declined", "expired_token", "bad_verification_code":
		return Credential{}, fmt.Errorf("%w: %s", ErrDeclined, res.ErrorDescription)
	}
	if err != nil {
		return Credential{}, err
	}
	if res.RefreshToken == "" {
		return Credential{}, errors.New("microsoft returned no refresh token: check that offline_access is consented and the app allows public client flows")
	}

	account, userID := identityFromIDToken(res.IDToken)
	return Credential{
		Config:       cfg,
		RefreshToken: res.RefreshToken,
		AccessToken:  res.AccessToken,
		Account:      account,
		UserID:       userID,
		ExpiresAt:    time.Now().Add(time.Duration(res.ExpiresIn) * time.Second).UTC().Format(time.RFC3339),
	}, nil
}

// AccessToken returns a usable token, refreshing when the cached one is spent.
//
// It returns the credential as well as the token because a refresh normally
// hands back a new refresh token, and dropping it is how a mirror works for
// ninety days and then stops. The caller stores what comes back.
func (c *Client) AccessToken(ctx context.Context, cred Credential, now time.Time) (string, Credential, error) {
	// A minute of slack, so a token that expires mid-request is renewed
	// before the request rather than retried after it.
	if cred.AccessToken != "" && cred.ExpiresAt != "" {
		if at, err := time.Parse(time.RFC3339, cred.ExpiresAt); err == nil && now.Add(time.Minute).Before(at) {
			return cred.AccessToken, cred, nil
		}
	}
	if cred.RefreshToken == "" {
		return "", cred, errors.New("no refresh token: connect this plugin again")
	}

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {cred.Config.ClientID},
		"refresh_token": {cred.RefreshToken},
		// The stored config's scopes, not the package default: a refreshed
		// mail credential must come back with Mail.Read rather than silently
		// widening to whatever the mirror asks for.
		"scope": {strings.Join(cred.Config.scopes(), " ")},
	}
	body, err := c.post(ctx, cred.Config.authority()+"/"+cred.Config.tenant()+"/oauth2/v2.0/token", form)
	if err != nil {
		var res tokenResponse
		if len(body) > 0 && json.Unmarshal(body, &res) == nil && res.ErrorDescription != "" {
			return "", cred, fmt.Errorf("refreshing the Microsoft token: %s", firstLine(res.ErrorDescription))
		}
		return "", cred, fmt.Errorf("refreshing the Microsoft token: %w", err)
	}

	var res tokenResponse
	if err := json.Unmarshal(body, &res); err != nil {
		return "", cred, err
	}
	if res.AccessToken == "" {
		return "", cred, errors.New("microsoft returned no access token")
	}

	cred.AccessToken = res.AccessToken
	cred.ExpiresAt = now.Add(time.Duration(res.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	if res.RefreshToken != "" {
		// Rotated. Keeping the old one would work until it did not.
		cred.RefreshToken = res.RefreshToken
	}
	return cred.AccessToken, cred, nil
}

func (c *Client) post(ctx context.Context, endpoint string, form url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode >= 300 {
		return body, fmt.Errorf("microsoft answered %s", resp.Status)
	}
	return body, nil
}

// identityFromIDToken reads who signed in out of the id token.
//
// The payload is read without verifying the signature, which is safe because
// of what it is used for: a label on a settings page and a filter on which
// tasks are yours. Nothing is authorized on the strength of it. The token came
// from a TLS connection to Microsoft in direct response to this request, and
// the thing that carries authority is the refresh token beside it.
func identityFromIDToken(idToken string) (account, userID string) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return "", ""
	}
	payload, err := base64URLDecode(parts[1])
	if err != nil {
		return "", ""
	}
	var claims struct {
		OID               string `json:"oid"`
		Subject           string `json:"sub"`
		PreferredUsername string `json:"preferred_username"`
		Email             string `json:"email"`
		Name              string `json:"name"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", ""
	}
	for _, candidate := range []string{claims.PreferredUsername, claims.Email, claims.Name} {
		if candidate != "" {
			account = candidate
			break
		}
	}
	// oid is the directory object id and is what Planner keys assignments by.
	// sub is per-application and is not, so it is deliberately not a fallback:
	// a wrong id here would silently mirror nothing.
	return account, claims.OID
}

func truncate(body []byte) string {
	if len(body) > 200 {
		return string(body[:200])
	}
	return string(body)
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i > 0 {
		return s[:i]
	}
	return s
}

// base64URLDecode accepts the unpadded form JWTs use.
func base64URLDecode(s string) ([]byte, error) {
	if pad := len(s) % 4; pad != 0 {
		s += strings.Repeat("=", 4-pad)
	}
	return base64.URLEncoding.DecodeString(s)
}
