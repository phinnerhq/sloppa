package artifacts

import "strings"

// byExtension decides how a browser takes a file whose uploader didn't say: renders it, plays it or
// downloads it.
var byExtension = map[string]string{
	"png":    "image/png",
	"jpg":    "image/jpeg",
	"jpeg":   "image/jpeg",
	"gif":    "image/gif",
	"webp":   "image/webp",
	"avif":   "image/avif",
	"svg":    "image/svg+xml",
	"mp4":    "video/mp4",
	"webm":   "video/webm",
	"mov":    "video/quicktime",
	"m4v":    "video/mp4",
	"ogv":    "video/ogg",
	"mkv":    "video/x-matroska",
	"mp3":    "audio/mpeg",
	"wav":    "audio/wav",
	"ogg":    "audio/ogg",
	"oga":    "audio/ogg",
	"opus":   "audio/ogg",
	"flac":   "audio/flac",
	"m4a":    "audio/mp4",
	"aac":    "audio/aac",
	"pdf":    "application/pdf",
	"html":   "text/html; charset=utf-8",
	"htm":    "text/html; charset=utf-8",
	"json":   "application/json; charset=utf-8",
	"ndjson": "application/x-ndjson; charset=utf-8",
	"xml":    "application/xml; charset=utf-8",
	"yaml":   "text/yaml; charset=utf-8",
	"yml":    "text/yaml; charset=utf-8",
	"csv":    "text/csv; charset=utf-8",
	"md":     "text/markdown; charset=utf-8",
	"txt":    "text/plain; charset=utf-8",
	"log":    "text/plain; charset=utf-8",
	"diff":   "text/x-diff; charset=utf-8",
	"patch":  "text/x-diff; charset=utf-8",
	"zip":    "application/zip",
	"gz":     "application/gzip",
	"har":    "application/json; charset=utf-8",
	"trace":  "application/octet-stream",
}

// ResolveContentType trusts the type the uploader declared unless it's missing or the generic
// fallback; then it goes by the name's extension.
func ResolveContentType(name, declared string) string {
	if t := strings.TrimSpace(declared); t != "" && t != "application/octet-stream" {
		return t
	}
	// No dot, or only a dotfile's leading one: no extension, whatever the name spells.
	var extension string
	if dot := strings.LastIndexByte(name, '.'); dot > 0 {
		extension = strings.ToLower(name[dot+1:])
	}
	if t, ok := byExtension[extension]; ok {
		return t
	}
	return "application/octet-stream"
}
