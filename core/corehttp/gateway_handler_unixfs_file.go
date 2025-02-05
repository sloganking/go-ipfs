package corehttp

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	gopath "path"
	"strings"
	"time"

	"github.com/gabriel-vasile/mimetype"
	files "github.com/ipfs/go-ipfs-files"
	"github.com/ipfs/go-ipfs/tracing"
	ipath "github.com/ipfs/interface-go-ipfs-core/path"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// serveFile returns data behind a file along with HTTP headers based on
// the file itself, its CID and the contentPath used for accessing it.
func (i *gatewayHandler) serveFile(w http.ResponseWriter, r *http.Request, resolvedPath ipath.Resolved, contentPath ipath.Path, file files.File, begin time.Time) {
	_, span := tracing.Span(r.Context(), "Gateway", "ServeFile", trace.WithAttributes(attribute.String("path", resolvedPath.String())))
	defer span.End()

	// Set Cache-Control and read optional Last-Modified time
	modtime := addCacheControlHeaders(w, r, contentPath, resolvedPath.Cid())

	// Set Content-Disposition
	name := addContentDispositionHeader(w, r, contentPath)

	// Prepare size value for Content-Length HTTP header (set inside of http.ServeContent)
	size, err := file.Size()
	if err != nil {
		http.Error(w, "cannot serve files with unknown sizes", http.StatusBadGateway)
		return
	}

	// Lazy seeker enables efficient range-requests and HTTP HEAD responses
	content := &lazySeeker{
		size:   size,
		reader: file,
	}

	// Calculate deterministic value for Content-Type HTTP header
	// (we prefer to do it here, rather than using implicit sniffing in http.ServeContent)
	var ctype string
	if _, isSymlink := file.(*files.Symlink); isSymlink {
		// We should be smarter about resolving symlinks but this is the
		// "most correct" we can be without doing that.
		ctype = "inode/symlink"
	} else {
		ctype = mime.TypeByExtension(gopath.Ext(name))
		if ctype == "" {
			// uses https://github.com/gabriel-vasile/mimetype library to determine the content type.
			// Fixes https://github.com/ipfs/go-ipfs/issues/7252
			mimeType, err := mimetype.DetectReader(content)
			if err != nil {
				http.Error(w, fmt.Sprintf("cannot detect content-type: %s", err.Error()), http.StatusInternalServerError)
				return
			}

			ctype = mimeType.String()
			_, err = content.Seek(0, io.SeekStart)
			if err != nil {
				http.Error(w, "seeker can't seek", http.StatusInternalServerError)
				return
			}
		}
		// Strip the encoding from the HTML Content-Type header and let the
		// browser figure it out.
		//
		// Fixes https://github.com/ipfs/go-ipfs/issues/2203
		if strings.HasPrefix(ctype, "text/html;") {
			ctype = "text/html"
		}
	}
	// Setting explicit Content-Type to avoid mime-type sniffing on the client
	// (unifies behavior across gateways and web browsers)
	w.Header().Set("Content-Type", ctype)

	// special fixup around redirects
	w = &statusResponseWriter{w}

	// ServeContent will take care of
	// If-None-Match+Etag, Content-Length and range requests
	_, dataSent, _ := ServeContent(w, r, name, modtime, content)

	// Was response successful?
	if dataSent {
		// Update metrics
		i.unixfsFileGetMetric.WithLabelValues(contentPath.Namespace()).Observe(time.Since(begin).Seconds())
	}
}
