package preview_test

import (
	"github.com/phinnerhq/sloppa/internal/httpapi"
	"github.com/phinnerhq/sloppa/internal/preview"
)

var _ httpapi.Previews = (*preview.Service)(nil)
