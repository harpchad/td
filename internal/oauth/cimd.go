package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Client ID Metadata Documents, draft-ietf-oauth-client-id-metadata-document-00
// as the 2026-07-28 MCP revision requires.
//
// The client id is an https URL that serves the client's own metadata, so a
// client and a server with no prior relationship need no registration step at
// all. It is the mechanism that revision prefers; Dynamic Client Registration
// is deprecated and kept only for clients that cannot do this.
//
// This is the one place td makes an outbound request to a URL somebody else
// chose, which makes it the one place server-side request forgery is possible.
// The dialer below is the mitigation and it is not optional.

// ErrNotCIMD says a client id is not a metadata document URL, so the caller
// should look it up as an ordinary registered client instead.
var ErrNotCIMD = errors.New("not a client id metadata document url")

// Document is a Client ID Metadata Document.
//
// Only the fields td acts on are read. The draft allows more, and a client
// sending more is not an error: unknown fields are ignored rather than
// rejected, because refusing a document over a field we do not use would
// break clients for no security gain.
type Document struct {
	ClientID     string   `json:"client_id"`
	ClientName   string   `json:"client_name"`
	RedirectURIs []string `json:"redirect_uris"`

	ClientURI               string   `json:"client_uri,omitempty"`
	LogoURI                 string   `json:"logo_uri,omitempty"`
	GrantTypes              []string `json:"grant_types,omitempty"`
	ResponseTypes           []string `json:"response_types,omitempty"`
	Scope                   string   `json:"scope,omitempty"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method,omitempty"`
}

// IsClientIDDocumentURL reports whether a client id should be resolved as a
// metadata document rather than looked up in the client table.
//
// The draft requires the https scheme and a path component. The path is what
// separates a client id from a bare origin, and requiring it means a host can
// never accidentally become a client id for its whole domain.
func IsClientIDDocumentURL(id string) bool {
	u, err := url.Parse(id)
	if err != nil {
		return false
	}
	return u.Scheme == "https" && u.Host != "" && u.User == nil &&
		u.Fragment == "" && u.Path != "" && u.Path != "/"
}

// Resolver fetches and validates Client ID Metadata Documents.
type Resolver struct {
	// HTTP is the client used for the fetch. NewResolver builds one that
	// refuses to connect to anything but a public address; a caller that
	// replaces it is responsible for that itself.
	HTTP *http.Client
	// MaxBytes caps the response body. A metadata document is a few hundred
	// bytes and this is generous by three orders of magnitude.
	MaxBytes int64
	// MinTTL and MaxTTL bound what a Cache-Control header can ask for, so a
	// client cannot pin its own metadata in this server forever, nor force a
	// refetch on every authorization.
	MinTTL, MaxTTL, DefaultTTL time.Duration
}

// NewResolver builds a resolver whose dialer refuses private addresses.
func NewResolver() *Resolver {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	// Control runs after DNS resolution with the address actually being
	// dialed, which is what makes this hold against a name that resolves to
	// a public address once and a private one the next time.
	dialer.Control = func(_, address string, _ syscall.RawConn) error {
		return checkDialAddress(address)
	}
	return &Resolver{
		HTTP: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				DialContext:           dialer.DialContext,
				TLSHandshakeTimeout:   5 * time.Second,
				ResponseHeaderTimeout: 5 * time.Second,
				DisableKeepAlives:     true,
			},
			// A redirect would break the rule that the document's client_id
			// equals the URL it was fetched from, so there is nothing useful
			// to follow one to.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("a client id metadata document must not redirect")
			},
		},
		MaxBytes:   64 << 10,
		MinTTL:     5 * time.Minute,
		MaxTTL:     24 * time.Hour,
		DefaultTTL: time.Hour,
	}
}

// Resolve fetches the document at clientID and validates it.
//
// The returned duration is how long the answer may be cached, taken from the
// response's Cache-Control and clamped to the resolver's bounds.
func (r *Resolver) Resolve(ctx context.Context, clientID string) (Document, time.Duration, error) {
	if !IsClientIDDocumentURL(clientID) {
		return Document{}, 0, ErrNotCIMD
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clientID, nil)
	if err != nil {
		return Document{}, 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "td")

	resp, err := r.HTTP.Do(req)
	if err != nil {
		return Document{}, 0, fmt.Errorf("could not read the client metadata: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return Document{}, 0, fmt.Errorf("the client metadata returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, r.MaxBytes+1))
	if err != nil {
		return Document{}, 0, err
	}
	if int64(len(body)) > r.MaxBytes {
		return Document{}, 0, errors.New("the client metadata is too large")
	}

	var doc Document
	if err := json.Unmarshal(body, &doc); err != nil {
		return Document{}, 0, errors.New("the client metadata is not valid JSON")
	}
	if err := doc.Validate(clientID); err != nil {
		return Document{}, 0, err
	}
	return doc, r.ttl(resp.Header.Get("Cache-Control")), nil
}

// Validate checks a document against the URL it came from.
//
// The client_id match is the load-bearing one. Without it anybody who can host
// JSON could claim to be any client, because the consent screen shows the name
// out of this document and a person approves what the name says.
func (d Document) Validate(clientID string) error {
	if d.ClientID != clientID {
		return errors.New("the client metadata claims a different client_id than the URL it came from")
	}
	if strings.TrimSpace(d.ClientName) == "" {
		return errors.New("the client metadata has no client_name")
	}
	if len(d.RedirectURIs) == 0 {
		return errors.New("the client metadata lists no redirect_uris")
	}
	for _, uri := range d.RedirectURIs {
		if !validRedirectURI(uri) {
			return fmt.Errorf("redirect_uri %q is neither https nor a loopback address", uri)
		}
	}
	return nil
}

// OnlyLoopbackRedirects reports whether every redirect goes to this machine,
// which the spec says to warn about: a document cannot prove that the software
// listening on localhost is the software the document describes.
func (d Document) OnlyLoopbackRedirects() bool {
	for _, uri := range d.RedirectURIs {
		if u, err := url.Parse(uri); err == nil && !isLoopbackHost(u.Hostname()) {
			return false
		}
	}
	return len(d.RedirectURIs) > 0
}

// RedirectHosts is what the consent screen shows. The spec requires the
// redirect hostname be displayed during authorization, because the name in the
// document is a claim and the host receiving the code is a fact.
func (d Document) RedirectHosts() []string {
	seen := map[string]bool{}
	var out []string
	for _, uri := range d.RedirectURIs {
		u, err := url.Parse(uri)
		if err != nil || u.Host == "" {
			continue
		}
		if !seen[u.Host] {
			seen[u.Host] = true
			out = append(out, u.Host)
		}
	}
	return out
}

// validRedirectURI enforces the communication security rule: every redirect
// URI is either https or a loopback address.
func validRedirectURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Fragment != "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		return isLoopbackHost(u.Hostname())
	default:
		return false
	}
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ttl reads Cache-Control and clamps it. A document with no opinion gets the
// default rather than being refetched on every authorization.
func (r *Resolver) ttl(cacheControl string) time.Duration {
	out := r.DefaultTTL
	for _, part := range strings.Split(cacheControl, ",") {
		part = strings.TrimSpace(strings.ToLower(part))
		if part == "no-store" || part == "no-cache" {
			return r.MinTTL
		}
		if age, ok := strings.CutPrefix(part, "max-age="); ok {
			if n, err := strconv.Atoi(age); err == nil {
				out = time.Duration(n) * time.Second
			}
		}
	}
	return min(max(out, r.MinTTL), r.MaxTTL)
}

// checkDialAddress refuses to connect to anything that is not a public
// address. This is the SSRF mitigation the draft asks for, and it runs on the
// resolved IP at connect time rather than on the hostname, so a name that
// answers with a public address once and 169.254.169.254 the next time is
// still refused.
func checkDialAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("could not read the address to dial: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("refusing to dial %q", host)
	}
	if !isPublicIP(ip) {
		return fmt.Errorf("refusing to fetch client metadata from %s, which is not a public address", ip)
	}
	return nil
}

// isPublicIP reports whether an address is one the open internet can route to.
//
// Everything else is refused: the loopback interface, the private ranges, the
// link-local range that carries cloud metadata services at 169.254.169.254,
// carrier-grade NAT, and IPv6 unique-local. A metadata document lives on the
// public internet by definition, so nothing here costs a real client anything.
func isPublicIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	// 100.64.0.0/10, carrier-grade NAT, which net.IP has no predicate for.
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return false
		}
		// 0.0.0.0/8, "this network", which some stacks route to localhost.
		if v4[0] == 0 {
			return false
		}
	}
	return true
}

// TTLForTest exposes the cache clamp to the package's tests. The clamp is the
// part with a rule in it, and a rule with no test is a rule that drifts.
func TTLForTest(r *Resolver, cacheControl string) time.Duration { return r.ttl(cacheControl) }
