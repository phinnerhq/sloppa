package s3

import (
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/phinnerhq/sloppa/internal/api"
	"github.com/phinnerhq/sloppa/internal/config"
	"github.com/phinnerhq/sloppa/internal/storage"
	"github.com/phinnerhq/sloppa/internal/storage/storagetest"
)

// TestContract runs against a real bucket, given one:
//
//	SLOPPA_TEST_S3_ENDPOINT=http://127.0.0.1:9000 SLOPPA_TEST_S3_ACCESS_KEY_ID=… \
//	SLOPPA_TEST_S3_SECRET_ACCESS_KEY=… SLOPPA_TEST_S3_BUCKET=sloppa-test go test ./internal/storage/s3
func TestContract(t *testing.T) {
	endpoint := os.Getenv("SLOPPA_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("SLOPPA_TEST_S3_ENDPOINT is not set")
	}
	bucket := os.Getenv("SLOPPA_TEST_S3_BUCKET")
	if bucket == "" {
		bucket = "sloppa-test"
	}
	cfg := &config.S3{
		Endpoint:        endpoint,
		Region:          "us-east-1",
		Bucket:          bucket,
		AccessKeyID:     config.Secret(os.Getenv("SLOPPA_TEST_S3_ACCESS_KEY_ID")),
		SecretAccessKey: config.Secret(os.Getenv("SLOPPA_TEST_S3_SECRET_ACCESS_KEY")),
	}
	s, err := New(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Twice: the second finds the bucket made by the first.
	if _, err := New(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	storagetest.Run(t, func(*testing.T) storage.Store { return s })
}

var id = "0a1b2c3d-0000-4000-8000-000000000001"

func at(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return &t
}

// How an object's metadata reads back as an artifact: what Put writes today, and what older versions
// left in the bucket.
func TestFromHeadReadsWhatPutWrites(t *testing.T) {
	written := storage.Artifact{
		ID:          id,
		Name:        "écran final ✓.png",
		ContentType: "image/png",
		Size:        2048,
		CreatedAt:   api.Millis(1792144800123),
		Collection:  "acme/web.app #42",
	}
	got := fromHead(id, metadataOf(written), aws.String("image/png"), aws.Int64(2048), at("2026-10-16T10:00:00Z"))
	if got != written {
		t.Errorf("got %+v\nwant %+v", got, written)
	}
	if meta := metadataOf(written); meta["name"] != "%C3%A9cran%20final%20%E2%9C%93.png" || meta["uploaded-at"] != "1792144800123" {
		t.Errorf("metadata %v", meta)
	}
}

func TestFromHeadKeepsTheExactMillisecond(t *testing.T) {
	got := fromHead(id, map[string]string{"uploaded-at": "1792144800999"}, nil, nil, at("2026-10-16T10:00:00Z"))
	if got.CreatedAt.UnixMilli() != 1792144800999 {
		t.Errorf("CreatedAt = %v", got.CreatedAt)
	}
}

func TestFromHeadReadsMetadataInAnyCase(t *testing.T) {
	got := fromHead(id, map[string]string{"Name": "a.txt", "Uploaded-At": "1000", "Collection": "c"}, nil, nil, nil)
	if got.Name != "a.txt" || got.CreatedAt.UnixMilli() != 1000 || got.Collection != "c" {
		t.Errorf("got %+v", got)
	}
}

func TestFromHeadSurvivesNoMetadata(t *testing.T) {
	got := fromHead(id, nil, nil, nil, at("2026-10-16T10:00:00Z"))
	want := storage.Artifact{
		ID:          id,
		Name:        id,
		ContentType: "application/octet-stream",
		Size:        0,
		CreatedAt:   api.Time(*at("2026-10-16T10:00:00Z")),
		Collection:  "unknown",
	}
	if got != want {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestFromHeadKeepsBadEncodingAsItIs(t *testing.T) {
	for _, tt := range []struct{ raw, want string }{
		{"100% done.txt", "100% done.txt"},
		{"%FF.txt", "%FF.txt"},
		{"a+b.txt", "a+b.txt"},
	} {
		got := fromHead(id, map[string]string{"name": tt.raw, "uploaded-at": "not a number"}, nil, nil, nil)
		if got.Name != tt.want {
			t.Errorf("name %q read as %q, want %q", tt.raw, got.Name, tt.want)
		}
		if got.CreatedAt.UnixMilli() != 0 {
			t.Errorf("CreatedAt = %v, want the epoch", got.CreatedAt)
		}
	}
	for _, bad := range []string{"Infinity", "NaN", "0", ""} {
		if got := fromHead(id, map[string]string{"uploaded-at": bad}, nil, nil, at("2026-10-16T10:00:00Z")); got.CreatedAt.Unix() != at("2026-10-16T10:00:00Z").Unix() {
			t.Errorf("uploaded-at %q read as %v, want LastModified", bad, got.CreatedAt)
		}
	}
}

func TestParseContentRange(t *testing.T) {
	r, size, ok := parseContentRange("bytes 0-4/12")
	if !ok || r != (storage.Range{Start: 0, End: 4}) || size != 12 {
		t.Errorf("got %+v %d %v", r, size, ok)
	}
	for _, bad := range []string{"", "bytes */12", "bytes 0-4/*", "items 0-4/12", "bytes 0--1/0"} {
		if _, _, ok := parseContentRange(bad); ok {
			t.Errorf("parsed %q", bad)
		}
	}
}
