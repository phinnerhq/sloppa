package artifacts

import "testing"

// The type an upload is served with, which decides whether a browser renders it, plays it or
// downloads it.
func TestResolveContentType(t *testing.T) {
	tests := []struct {
		why, name, declared, want string
	}{
		{"trusts the declared type", "notes.bin", "text/plain; charset=utf-8", "text/plain; charset=utf-8"},
		{"trusts the declared type, trimmed", "shot.png", " image/webp ", "image/webp"},
		{"goes by the extension without a type", "shot.png", "", "image/png"},
		{"goes by the extension for a blank type", "run.log", "  ", "text/plain; charset=utf-8"},
		{"goes by the extension for the generic type", "trace.json", "application/octet-stream", "application/json; charset=utf-8"},
		{"reads extensions in any case", "SHOT.PNG", "", "image/png"},
		{"reads only the last extension", "logs.tar.gz", "", "application/gzip"},
		{"falls back to a download for unknown extensions", "data.parquet", "", "application/octet-stream"},
		{"falls back to a download without an extension", "README", "", "application/octet-stream"},
		{"doesn't take a name that spells an extension for one", "json", "", "application/octet-stream"},
		{"doesn't take a name that spells an extension for one", "png", "", "application/octet-stream"},
		{"doesn't take a dotfile's name for an extension", ".env", "", "application/octet-stream"},
		{"doesn't take a trailing dot for an extension", "shot.", "", "application/octet-stream"},
	}
	for _, tt := range tests {
		if got := ResolveContentType(tt.name, tt.declared); got != tt.want {
			t.Errorf("%s: ResolveContentType(%q, %q) = %q, want %q", tt.why, tt.name, tt.declared, got, tt.want)
		}
	}
}
