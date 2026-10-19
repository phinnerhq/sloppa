package httpapi

import (
	"context"
	"io/fs"
	"net/http"
	"time"

	"github.com/phinnerhq/sloppa/internal/accounts"
	"github.com/phinnerhq/sloppa/internal/api"
	"github.com/phinnerhq/sloppa/internal/artifacts"
	"github.com/phinnerhq/sloppa/internal/config"
	"github.com/phinnerhq/sloppa/internal/storage"
)

// What the HTTP layer serves, each implemented by its own package. Errors that should reach the
// client as such are *api.Error; anything else is a 500.

// Accounts is implemented by *accounts.Service.
type Accounts interface {
	// Login is throttled per username and per client address; client may be "".
	Login(ctx context.Context, username, password, client string) (a accounts.Account, session string, expires time.Time, err error)
	Logout(ctx context.Context, session string) error
	// ChangePassword is throttled like a sign-in, and signs out every other browser.
	ChangePassword(ctx context.Context, a accounts.Account, session, current, next string) error
	// Authenticate is who a session cookie or machine token (kind) belongs to; ok is false for neither.
	Authenticate(ctx context.Context, kind, token string) (a accounts.Account, ok bool, err error)

	Machines(ctx context.Context, a accounts.Account) ([]api.Machine, error)
	CreateMachine(ctx context.Context, a accounts.Account, name string) (api.CreatedMachine, error)
	RevokeMachine(ctx context.Context, a accounts.Account, id string) error
	RenameMachine(ctx context.Context, a accounts.Account, id, name string) error

	// CreateSetup makes a link that registers one machine, until it expires or is revoked.
	CreateSetup(ctx context.Context, a accounts.Account) (id, code string, expires time.Time, err error)
	// RedeemSetup spends a link on a new machine, or re-pairs the one current is the token of if it's
	// the link's user's; ok is false for a dead link.
	RedeemSetup(ctx context.Context, code, current string, facts api.MachineFacts) (r api.SetupResult, ok bool, err error)
	// SetupExpiry is when a live link stops working; ok is false for a dead one.
	SetupExpiry(ctx context.Context, code string) (expires time.Time, ok bool, err error)
	RevokeSetup(ctx context.Context, code string) (revoked bool, err error)
	RevokeSetupByID(ctx context.Context, a accounts.Account, id string) error
}

// Artifacts is implemented by *artifacts.Service.
type Artifacts interface {
	List(ctx context.Context, q artifacts.ListQuery) (api.ArtifactPage, error)
	Mosaic(ctx context.Context, cursor string, limit int) (api.GroupPage, error)
	Collections(ctx context.Context) ([]api.CollectionSummary, error)
	Stats(ctx context.Context) (api.Stats, error)
	Get(ctx context.Context, id string) (api.Artifact, error)
	Upload(ctx context.Context, u artifacts.Upload) (api.Artifact, error)
	Move(ctx context.Context, id, collection string) (api.Artifact, error)
	Remove(ctx context.Context, id string) error
	// Open reads the file for the raw route, honouring an HTTP Range header.
	Open(ctx context.Context, id, rangeHeader string) (*storage.Object, error)
	// Thumbnail is a small WebP of an image, or an error wrapping artifacts.ErrNoThumbnail.
	Thumbnail(ctx context.Context, id string) ([]byte, error)
}

// Previews is implemented by *preview.Service. Without PREVIEW_ORIGIN its methods fail with
// api.PreviewsUnavailable and it claims no hosts.
type Previews interface {
	List(ctx context.Context, a accounts.Account) ([]api.Preview, error)
	Start(ctx context.Context, a accounts.Account, in api.StartPreview) (api.StartedPreview, error)
	Stop(ctx context.Context, a accounts.Account, id string) error
	// Tunnel upgrades GET /api/previews/{id}/tunnel to the preview's tunnel. The request is already
	// authenticated: the account is in its context.
	Tunnel(w http.ResponseWriter, r *http.Request, id string)
	// IsPreviewHost tells whether a request's Host is a preview's, which ServeProxy handles.
	IsPreviewHost(host string) bool
	ServeProxy(w http.ResponseWriter, r *http.Request)
}

type Deps struct {
	Config    *config.Config
	Accounts  Accounts
	Artifacts Artifacts
	Previews  Previews
	// Static is the built dashboard; nil serves none.
	Static fs.FS
}
