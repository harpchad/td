package web

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/store"
)

// Consent renders the OAuth approval screen.
//
// It is a form on the same origin behind the ordinary login, which is what
// makes td an authorization server without a second identity system. The
// checkboxes exist because the point of a consent screen is that it can grant
// less than was asked for; a screen with only an Approve button is a
// notification, not a decision.
func (u *UI) Consent(w http.ResponseWriter, r *http.Request, clientName string, scopes []string, request string) {
	data := u.base(r, "Authorize "+clientName)
	data.ClientName = clientName
	data.Request = request
	for _, scope := range scopes {
		data.ConsentScopes = append(data.ConsentScopes, consentScope{
			Value:     scope,
			Label:     scopeLabel(scope),
			Checked:   true,
			Sensitive: scope == api.MCPScopeWrite,
		})
	}
	u.render(w, "consent", data)
}

// OAuthError renders a failure that must not be redirected anywhere.
//
// This is what a bad client id or an unregistered redirect_uri gets. Sending
// a code or even an error to a URI nobody validated is how an authorization
// code ends up somewhere it should not be, so the message stops here.
func (u *UI) OAuthError(w http.ResponseWriter, r *http.Request, message string) {
	data := u.base(r, "Authorization failed")
	data.Error = message
	w.WriteHeader(http.StatusBadRequest)
	u.render(w, "oautherror", data)
}

// consentScope is one checkbox on the approval form.
type consentScope struct {
	Value   string
	Label   string
	Checked bool
	// Sensitive marks the one that lets an agent change your list rather than
	// only read it and add to it. It is drawn differently because "can write"
	// and "can read" deserve different amounts of attention.
	Sensitive bool
}

// scopeLabel says what a scope allows in the words a person would use, not in
// the words the protocol uses.
func scopeLabel(scope string) string {
	switch scope {
	case api.MCPScopeRead:
		return "Read your tasks, people, and activity"
	case api.MCPScopeCapture:
		return "Add things to your inbox"
	case api.MCPScopeWrite:
		return "Change and complete your tasks"
	}
	return scope
}

// grantLister is the part of the store the settings page needs for OAuth
// grants. Narrow and optional, so a server built without the authorization
// server still renders the page.
type grantLister interface {
	Grants(ctx context.Context) ([]store.OAuthGrant, error)
	RevokeGrant(ctx context.Context, id string, now time.Time) error
}

// GrantRows loads the OAuth grants for the settings page. They sit next to
// the static tokens with the same revoke button, because claude.ai will be
// holding a refresh token for your task list and you want one place to cut it
// off.
func (u *UI) GrantRows(ctx context.Context) []grantRow {
	lister, ok := u.svc.(grantLister)
	if !ok {
		return nil
	}
	grants, err := lister.Grants(ctx)
	if err != nil {
		u.log.Error("loading oauth grants", "err", err)
		return nil
	}

	out := make([]grantRow, 0, len(grants))
	for _, g := range grants {
		row := grantRow{
			ID: g.ID, Client: g.ClientName, Resource: g.Resource,
			Scopes:  strings.Join(g.Scopes, ", "),
			Created: g.CreatedAt, LastUsed: "never",
			Revoked: g.RevokedAt != nil,
		}
		if g.LastUsedAt != nil {
			row.LastUsed = *g.LastUsedAt
		}
		out = append(out, row)
	}
	return out
}

// RevokeGrant cuts off one grant from the settings page.
func (u *UI) RevokeGrant(w http.ResponseWriter, r *http.Request) {
	if lister, ok := u.svc.(grantLister); ok {
		if err := lister.RevokeGrant(r.Context(), r.PathValue("id"), u.Now()); err != nil {
			u.log.Error("revoke grant", "err", err)
		}
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}
