package accounts

import "context"

// Account is a signed-in user, as whatever credential authenticated the request.
type Account struct {
	ID       int64
	Username string
	// CredentialID is the session or agent token's row in `credentials`.
	CredentialID string
	// Kind is KindSession or KindAgent.
	Kind string
}

const (
	KindSession = "session"
	KindAgent   = "agent"
	KindSetup   = "setup"
)

type accountKey struct{}

// WithAccount is ctx for a request made as a.
func WithAccount(ctx context.Context, a Account) context.Context {
	return context.WithValue(ctx, accountKey{}, a)
}

// FromContext is who the request was authenticated as, if anyone.
func FromContext(ctx context.Context) (Account, bool) {
	a, ok := ctx.Value(accountKey{}).(Account)
	return a, ok
}
