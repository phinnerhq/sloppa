package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/phinnerhq/sloppa/internal/accounts"
	"github.com/phinnerhq/sloppa/internal/api"
)

// Security scheme names in the OpenAPI document, as the TypeScript server named them.
const (
	cookieScheme = "session"
	bearerScheme = "bearer"
)

// access is who may call an operation.
type access int

const (
	public access = iota
	// signedIn takes a browser's session cookie or an agent's bearer token. Everything needs one
	// except fetching a single artifact by id: its random UUID is what makes a shared link
	// unguessable, so listing (which would hand out every id) stays behind sign-in.
	signedIn
	// browser takes the session cookie only. Managing agents takes a person: a leaked agent token
	// mustn't be able to mint itself replacements before it's revoked.
	browser
)

func (a access) security() []map[string][]string {
	switch a {
	case signedIn:
		return []map[string][]string{{cookieScheme: {}}, {bearerScheme: {}}}
	case browser:
		return []map[string][]string{{cookieScheme: {}}}
	}
	return nil
}

func (a access) unauthorized() *api.Error {
	if a == browser {
		return api.Unauthorized("Sign in from a browser for this")
	}
	return api.Unauthorized("Sign in, or pass an agent token")
}

type sessionKey struct{}

// sessionOf is the session cookie a request was authenticated with, or "" for an agent's.
func sessionOf(ctx context.Context) string {
	s, _ := ctx.Value(sessionKey{}).(string)
	return s
}

// account is who ctx's request was authenticated as. Only called behind authentication.
func account(ctx context.Context) accounts.Account {
	a, ok := accounts.FromContext(ctx)
	if !ok {
		panic("httpapi: no account in the context of an authenticated route")
	}
	return a
}

// authenticate is the context of r as whoever its credentials belong to, or the api.Error to answer
// with. Unsafe requests signed in by cookie must come from the app itself: previews are served from
// subdomains, so a page on one is same-site to the app and SameSite=Lax alone would let it act as
// whoever opens it.
func (s *server) authenticate(r *http.Request, need access) (context.Context, error) {
	ctx := r.Context()
	if session := s.sessionToken(r); session != "" {
		a, ok, err := s.accounts.Authenticate(ctx, accounts.KindSession, session)
		if err != nil {
			return nil, err
		}
		if ok {
			if err := s.sameOrigin(r); err != nil {
				return nil, err
			}
			return context.WithValue(accounts.WithAccount(ctx, a), sessionKey{}, session), nil
		}
	}
	if need == signedIn {
		if token, ok := bearerToken(r); ok {
			a, ok, err := s.accounts.Authenticate(ctx, accounts.KindAgent, token)
			if err != nil {
				return nil, err
			}
			if ok {
				return accounts.WithAccount(ctx, a), nil
			}
		}
	}
	return nil, need.unauthorized()
}

// sameOrigin refuses unsafe requests a browser says came from another origin. Requests without the
// headers browsers send, like the CLI's, pass.
func (s *server) sameOrigin(r *http.Request) error {
	if err := s.cop.Check(r); err != nil {
		return api.Forbidden("Cross-origin requests can't act as you: use the dashboard itself")
	}
	return nil
}

func bearerToken(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	token = strings.TrimSpace(token)
	return token, ok && strings.EqualFold(scheme, "Bearer") && token != ""
}

// requireAuth is the Huma middleware for operations that need need.
func (s *server) requireAuth(need access) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		r, _ := humago.Unwrap(ctx)
		authed, err := s.authenticate(r, need)
		if err != nil {
			huma.WriteErr(s.api, ctx, http.StatusInternalServerError, "authenticate", err)
			return
		}
		next(huma.WithContext(ctx, authed))
	}
}

// requireSameOrigin is the Huma middleware for unsafe operations that need no sign-in but set the
// session cookie, so another site can't sign a browser in as someone else.
func (s *server) requireSameOrigin(ctx huma.Context, next func(huma.Context)) {
	r, _ := humago.Unwrap(ctx)
	if err := s.sameOrigin(r); err != nil {
		huma.WriteErr(s.api, ctx, http.StatusForbidden, "", err)
		return
	}
	next(ctx)
}

// withAuth wraps a plain handler in authentication.
func (s *server) withAuth(need access, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, err := s.authenticate(r, need)
		if err != nil {
			writeError(w, r, err)
			return
		}
		h(w, r.WithContext(ctx))
	}
}
