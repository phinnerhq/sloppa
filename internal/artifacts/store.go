package artifacts

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/phinnerhq/sloppa/internal/config"
	"github.com/phinnerhq/sloppa/internal/storage"
	"github.com/phinnerhq/sloppa/internal/storage/fs"
	"github.com/phinnerhq/sloppa/internal/storage/s3"
)

// NewStore is where the configuration says artifacts live: the S3 bucket when S3_ENDPOINT is set,
// DATA_DIR/files otherwise, as the TypeScript server kept them.
func NewStore(ctx context.Context, cfg *config.Config) (storage.Store, error) {
	if cfg.S3 != nil {
		slog.Info("Storing artifacts in S3", "bucket", cfg.S3.Bucket, "endpoint", cfg.S3.Endpoint)
		// Returned only when set: a nil *s3.Store would make a non-nil storage.Store.
		s, err := s3.New(ctx, cfg.S3)
		if err != nil {
			return nil, err
		}
		return s, nil
	}
	dir := filepath.Join(cfg.DataDir, "files")
	slog.Info("S3_ENDPOINT not set, storing artifacts on disk", "dir", dir)
	s, err := fs.New(dir)
	if err != nil {
		return nil, err
	}
	return s, nil
}
