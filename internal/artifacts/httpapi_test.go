package artifacts_test

import (
	"github.com/phinnerhq/sloppa/internal/artifacts"
	"github.com/phinnerhq/sloppa/internal/httpapi"
)

var _ httpapi.Artifacts = (*artifacts.Service)(nil)
