package httpapi

import (
	"context"
	"net"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/phinnerhq/sloppa/internal/api"
)

type loginInput struct {
	// Bounded, so a login can't make the throttle hold on to a huge username.
	Body struct {
		Username string `json:"username" maxLength:"200"`
		Password string `json:"password" maxLength:"1024"`
	}
	remoteAddr, forwardedFor string
}

func (in *loginInput) Resolve(ctx huma.Context) []error {
	in.remoteAddr = ctx.RemoteAddr()
	in.forwardedFor = ctx.Header("X-Forwarded-For")
	return nil
}

type userOutput struct {
	Body api.User
}

type loginOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      api.User
}

type logoutInput struct{}

type logoutOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
}

type changePasswordInput struct {
	Body struct {
		Current string `json:"current" maxLength:"1024"`
		Next    string `json:"next" minLength:"8" maxLength:"1024"`
	}
}

func (s *server) registerSession() {
	login := s.operation("Session", "session.login", http.MethodPost, "/api/session", public,
		api.KindInvalidCredentials, api.KindTooManyAttempts, api.KindForbidden)
	login.Middlewares = huma.Middlewares{s.requireSameOrigin}
	huma.Register(s.api, login, func(ctx context.Context, in *loginInput) (*loginOutput, error) {
		a, session, expires, err := s.accounts.Login(ctx, in.Body.Username, in.Body.Password, s.client(in.remoteAddr, in.forwardedFor))
		if err != nil {
			return nil, err
		}
		cookie := s.sessionCookie(session)
		cookie.Expires = expires
		return &loginOutput{SetCookie: cookie, Body: api.User{Username: a.Username}}, nil
	})

	huma.Register(s.api, s.operation("Session", "session.current", http.MethodGet, "/api/session", signedIn),
		func(ctx context.Context, _ *struct{}) (*userOutput, error) {
			a := account(ctx)
			return &userOutput{Body: api.User{Username: a.Username, Machine: a.MachineName}}, nil
		})

	huma.Register(s.api, s.operation("Session", "session.logout", http.MethodDelete, "/api/session", signedIn),
		func(ctx context.Context, _ *logoutInput) (*logoutOutput, error) {
			if session := sessionOf(ctx); session != "" {
				if err := s.accounts.Logout(ctx, session); err != nil {
					return nil, err
				}
			}
			cookie := s.sessionCookie("")
			cookie.MaxAge = -1
			return &logoutOutput{SetCookie: cookie}, nil
		})

	changePassword := s.operation("Session", "session.changePassword", http.MethodPut, "/api/session/password", browser,
		api.KindInvalidCredentials, api.KindTooManyAttempts)
	changePassword.Description = "Signs out every other browser."
	huma.Register(s.api, changePassword, func(ctx context.Context, in *changePasswordInput) (*struct{}, error) {
		return nil, s.accounts.ChangePassword(ctx, account(ctx), sessionOf(ctx), in.Body.Current, in.Body.Next)
	})
}

// sessionCookieName is the session cookie's name for an app at publicURL. Over HTTPS it takes the
// __Host- prefix: browsers only take such a cookie Secure, for Path=/ and from the app's own host,
// so a page on a sibling subdomain (a preview, where they share a registrable domain) can't plant
// one under the name the app reads. Plain HTTP can't have it.
func sessionCookieName(publicURL string) string {
	if strings.HasPrefix(publicURL, "https:") {
		return "__Host-" + api.SessionCookie
	}
	return api.SessionCookie
}

// sessionCookie keeps the session on links from elsewhere (a PR), but not on another site's POSTs.
func (s *server) sessionCookie(value string) http.Cookie {
	return http.Cookie{
		Name:     s.cookie,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   strings.HasPrefix(s.cfg.PublicURL, "https:"),
		SameSite: http.SameSiteLaxMode,
	}
}

// sessionToken is the session cookie r carries, or "". Over plain HTTP a sibling subdomain can still
// set a cookie of the same name for the parent domain, and the browser sends both with no telling
// which is the app's: then there's no session, rather than possibly someone else's.
func (s *server) sessionToken(r *http.Request) string {
	token := ""
	for _, c := range r.CookiesNamed(s.cookie) {
		if c.Value == "" {
			continue
		}
		if token != "" && c.Value != token {
			return ""
		}
		token = c.Value
	}
	return token
}

// client is the caller's address for the login throttle: the socket's, or behind a trusted proxy,
// the last X-Forwarded-For entry, the one the proxy added.
func (s *server) client(remoteAddr, forwardedFor string) string {
	if s.cfg.TrustProxy && forwardedFor != "" {
		parts := strings.Split(forwardedFor, ",")
		if last := strings.TrimSpace(parts[len(parts)-1]); last != "" {
			return last
		}
	}
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}
