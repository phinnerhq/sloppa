// Package s3 keeps artifacts as objects under `artifacts/<uuid>` in an S3 bucket, their metadata as
// object metadata. Made for RustFS and MinIO as much as AWS: the endpoint is given and buckets are
// addressed by path. Keys and metadata are the TypeScript server's, so its buckets keep working.
package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/google/uuid"

	"github.com/phinnerhq/sloppa/internal/api"
	"github.com/phinnerhq/sloppa/internal/config"
	"github.com/phinnerhq/sloppa/internal/storage"
)

const prefix = "artifacts/"

func keyOf(id string) string { return prefix + id }

// Uploads stream in parts of partSize, a few at a time: one upload holds at most about
// (uploadConcurrency+2) parts in memory, whatever the file's size. Smaller files go up in one request.
const (
	partSize          = 8 << 20
	uploadConcurrency = 3
)

type Store struct {
	client   *awss3.Client
	uploader *transfermanager.Client
	bucket   string
}

var _ storage.Store = (*Store)(nil)

// New connects to the bucket, creating it if it doesn't exist yet.
func New(ctx context.Context, cfg *config.S3) (*Store, error) {
	client := awss3.New(awss3.Options{
		Region:       cfg.Region,
		BaseEndpoint: aws.String(cfg.Endpoint),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKeyID.Value(), cfg.SecretAccessKey.Value(), ""),
		// Checksums only where S3 requires them: not every S3-compatible server takes the SDK's defaults.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	s := &Store{
		client: client,
		uploader: transfermanager.New(client, func(o *transfermanager.Options) {
			o.PartSizeBytes = partSize
			o.MultipartUploadThreshold = partSize
			o.Concurrency = uploadConcurrency
			o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		}),
		bucket: cfg.Bucket,
	}
	if err := s.ensureBucket(ctx); err != nil {
		return nil, fmt.Errorf("s3://%s at %s: %w", cfg.Bucket, cfg.Endpoint, err)
	}
	return s, nil
}

func (s *Store) ensureBucket(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: &s.bucket})
	if !isNotFound(err) {
		return wrap(err)
	}
	_, err = s.client.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: &s.bucket})
	var owned *types.BucketAlreadyOwnedByYou
	if errors.As(err, &owned) {
		return nil
	}
	if err != nil {
		return wrap(err)
	}
	slog.Info("Created the bucket", "bucket", s.bucket)
	return nil
}

// S3 user metadata must be ASCII, so free-form values are URI-encoded.
func encodeMeta(value string) string { return api.EncodeURIComponent(value) }

// decodeMeta is nil for a value that isn't there, and a value as it is when it isn't valid URI
// encoding, rather than failing the read.
func decodeMeta(metadata map[string]string, key string) *string {
	value, ok := metadata[key]
	if !ok {
		return nil
	}
	if decoded, err := url.PathUnescape(value); err == nil && utf8.ValidString(decoded) {
		return &decoded
	}
	return &value
}

func metadataOf(a storage.Artifact) map[string]string {
	return map[string]string{
		"name":        encodeMeta(a.Name),
		"uploaded-at": strconv.FormatInt(a.CreatedAt.UnixMilli(), 10),
		"collection":  encodeMeta(a.Collection),
	}
}

// fromHead is an object's metadata, as Put writes it, read back as an artifact; objects from older
// versions included.
func fromHead(id string, metadata map[string]string, contentType *string, contentLength *int64, lastModified *time.Time) storage.Artifact {
	// Header names are case-insensitive, and servers don't agree on the case they send back.
	lower := make(map[string]string, len(metadata))
	for k, v := range metadata {
		lower[strings.ToLower(k)] = v
	}
	a := storage.Artifact{
		ID:          id,
		Name:        id,
		ContentType: "application/octet-stream",
		Size:        aws.ToInt64(contentLength),
	}
	if name := decodeMeta(lower, "name"); name != nil {
		a.Name = *name
	}
	if contentType != nil {
		a.ContentType = *contentType
	}
	// S3's LastModified only has second precision; keep upload order exact.
	switch ms, err := strconv.ParseFloat(strings.TrimSpace(lower["uploaded-at"]), 64); {
	case err == nil && ms != 0 && !math.IsInf(ms, 0) && !math.IsNaN(ms):
		a.CreatedAt = api.Millis(int64(ms))
	case lastModified != nil:
		a.CreatedAt = api.Time(*lastModified)
	default:
		a.CreatedAt = api.Millis(0)
	}
	if collection := decodeMeta(lower, "collection"); collection != nil {
		a.Collection = *collection
	} else {
		// Artifacts from before collections have none.
		a.Collection = api.UnknownCollection
	}
	return a
}

func (s *Store) Head(ctx context.Context, id string) (storage.Artifact, error) {
	if !storage.IsID(id) {
		return storage.Artifact{}, storage.ErrNotFound
	}
	out, err := s.client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: &s.bucket, Key: aws.String(keyOf(id))})
	if err != nil {
		return storage.Artifact{}, wrap(err)
	}
	return fromHead(id, out.Metadata, out.ContentType, out.ContentLength, out.LastModified), nil
}

func (s *Store) Put(ctx context.Context, in storage.PutInput) (storage.Artifact, error) {
	id := in.ID
	if id == "" {
		id = uuid.NewString()
	} else if !storage.IsID(id) {
		return storage.Artifact{}, fmt.Errorf("write artifact: %q is not a UUID", id)
	}
	a := storage.Artifact{
		ID:          id,
		Name:        in.Name,
		ContentType: in.ContentType,
		CreatedAt:   api.Time(time.Now()),
		Collection:  in.Collection,
	}
	// Streamed: the transfer manager reads a part at a time, never the whole file.
	body := &counter{r: in.Body}
	_, err := s.uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket:      &s.bucket,
		Key:         aws.String(keyOf(id)),
		Body:        body,
		ContentType: &in.ContentType,
		Metadata:    metadataOf(a),
	})
	if err != nil {
		return storage.Artifact{}, fmt.Errorf("write artifact: %w", wrap(err))
	}
	a.Size = body.n
	if in.Size >= 0 && a.Size != in.Size {
		_, _ = s.client.DeleteObject(context.WithoutCancel(ctx), &awss3.DeleteObjectInput{Bucket: &s.bucket, Key: aws.String(keyOf(id))})
		return storage.Artifact{}, fmt.Errorf("write artifact: expected %d bytes, got %d", in.Size, a.Size)
	}
	return a, nil
}

func (s *Store) getObject(ctx context.Context, id, rangeHeader string) (*awss3.GetObjectOutput, error) {
	in := &awss3.GetObjectInput{Bucket: &s.bucket, Key: aws.String(keyOf(id))}
	if rangeHeader != "" {
		in.Range = &rangeHeader
	}
	out, err := s.client.GetObject(ctx, in)
	if err != nil {
		return nil, wrap(err)
	}
	return out, nil
}

func (s *Store) Read(ctx context.Context, id, rangeHeader string) (*storage.Object, error) {
	if !storage.IsID(id) {
		return nil, storage.ErrNotFound
	}
	out, err := s.getObject(ctx, id, strings.TrimSpace(rangeHeader))
	// An unsatisfiable or malformed range just gets the whole file.
	if err != nil && rangeHeader != "" && !errors.Is(err, storage.ErrNotFound) {
		out, err = s.getObject(ctx, id, "")
	}
	if err != nil {
		return nil, err
	}
	a := fromHead(id, out.Metadata, out.ContentType, out.ContentLength, out.LastModified)
	o := &storage.Object{Artifact: a, Body: out.Body, ContentLength: aws.ToInt64(out.ContentLength)}
	if r, size, ok := parseContentRange(aws.ToString(out.ContentRange)); ok {
		o.Range = &r
		o.Artifact.Size = size
	}
	return o, nil
}

// parseContentRange reads `bytes a-b/size`. An empty range isn't one: RustFS answers `bytes 0--1/0`
// for the last byte of an empty file.
func parseContentRange(header string) (r storage.Range, size int64, ok bool) {
	spec, found := strings.CutPrefix(header, "bytes ")
	span, total, found2 := strings.Cut(spec, "/")
	start, end, found3 := strings.Cut(span, "-")
	if !found || !found2 || !found3 {
		return storage.Range{}, 0, false
	}
	var err1, err2, err3 error
	r.Start, err1 = strconv.ParseInt(start, 10, 64)
	r.End, err2 = strconv.ParseInt(end, 10, 64)
	size, err3 = strconv.ParseInt(total, 10, 64)
	return r, size, err1 == nil && err2 == nil && err3 == nil && r.Start <= r.End
}

func (s *Store) Scan(ctx context.Context) iter.Seq2[storage.Artifact, error] {
	ids := func(yield func(string, error) bool) {
		pages := awss3.NewListObjectsV2Paginator(s.client, &awss3.ListObjectsV2Input{Bucket: &s.bucket, Prefix: aws.String(prefix)})
		for pages.HasMorePages() {
			page, err := pages.NextPage(ctx)
			if err != nil {
				yield("", fmt.Errorf("list the bucket: %w", wrap(err)))
				return
			}
			for _, object := range page.Contents {
				id := strings.TrimPrefix(aws.ToString(object.Key), prefix)
				// Anything else under the prefix isn't one of ours.
				if storage.IsID(id) && !yield(id, nil) {
					return
				}
			}
		}
	}
	return storage.HeadAll(ctx, ids, s.Head)
}

func (s *Store) Remove(ctx context.Context, id string) error {
	if _, err := s.Head(ctx, id); err != nil {
		return err
	}
	if _, err := s.client.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: &s.bucket, Key: aws.String(keyOf(id))}); err != nil {
		return fmt.Errorf("delete artifact: %w", wrap(err))
	}
	return nil
}

// SetCollection copies the object onto itself with the new metadata: S3 can't edit it in place.
func (s *Store) SetCollection(ctx context.Context, id, collection string) (storage.Artifact, error) {
	a, err := s.Head(ctx, id)
	if err != nil {
		return storage.Artifact{}, err
	}
	a.Collection = collection
	copied, err := s.client.CopyObject(ctx, &awss3.CopyObjectInput{
		Bucket:            &s.bucket,
		Key:               aws.String(keyOf(id)),
		CopySource:        aws.String(s.bucket + "/" + keyOf(id)),
		MetadataDirective: types.MetadataDirectiveReplace,
		ContentType:       &a.ContentType,
		Metadata:          metadataOf(a),
	})
	if err != nil {
		return storage.Artifact{}, fmt.Errorf("move %s: %w", id, wrap(err))
	}
	// S3 may answer a failed copy with a 200 carrying an error body: only a copy result means it worked.
	if copied.CopyObjectResult == nil || copied.CopyObjectResult.ETag == nil {
		return storage.Artifact{}, fmt.Errorf("move %s: S3 didn't confirm the copy", id)
	}
	return a, nil
}

// counter counts what a reader read, which an upload of unknown size learns its size from. It hides
// the reader's other methods on purpose: the transfer manager would seek one to find its size.
type counter struct {
	r io.Reader
	n int64
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// isNotFound tells a missing object (or, for HeadBucket, bucket) from other failures. A missing bucket
// otherwise is a storage failure: it's not that one artifact is gone.
func isNotFound(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		return code == "NotFound" || code == "NoSuchKey"
	}
	var respErr *awshttp.ResponseError
	return errors.As(err, &respErr) && respErr.HTTPStatusCode() == http.StatusNotFound
}

// wrap is storage.ErrNotFound for a missing object, and an error reading `Code: message` otherwise,
// rather than the SDK's line about the operation and request ids.
func wrap(err error) error {
	if err == nil {
		return nil
	}
	if isNotFound(err) {
		return storage.ErrNotFound
	}
	return &s3Error{err}
}

type s3Error struct{ err error }

func (e *s3Error) Error() string {
	var apiErr smithy.APIError
	if errors.As(e.err, &apiErr) {
		if msg := apiErr.ErrorMessage(); msg != "" {
			return apiErr.ErrorCode() + ": " + msg
		}
		return apiErr.ErrorCode()
	}
	return e.err.Error()
}

func (e *s3Error) Unwrap() error { return e.err }
