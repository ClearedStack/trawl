package normalize

import "strings"

// Resolve turns a reference found in a document into an absolute address,
// given the canonical address of the document. It follows the reference
// resolution of RFC 3986: a reference with its own scheme stands alone,
// "//host/path" borrows the scheme, "/path" borrows the host, "?query"
// borrows the path, and anything else is taken relative to the document's
// folder, with "." and ".." segments worked out.
//
// base must be a canonical address from Normalize. The result is not yet
// canonical; pass it to Normalize.
func Resolve(base, ref string) string {
	ref = strings.Trim(ref, " \t\r\n")
	if hasScheme(ref) {
		return ref
	}

	cut := strings.Index(base, "://")
	scheme, rest := base[:cut], base[cut+3:]
	authority, path, query := rest, "/", ""
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		authority, path = rest[:i], rest[i:]
	}
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path, query = path[:i], path[i:]
	}
	origin := scheme + "://" + authority

	switch {
	case strings.HasPrefix(ref, "//"):
		return scheme + ":" + ref
	case ref == "" || ref[0] == '#':
		return origin + path + query
	case ref[0] == '?':
		return origin + path + ref
	}

	refPath, tail := ref, ""
	if i := strings.IndexAny(ref, "?#"); i >= 0 {
		refPath, tail = ref[:i], ref[i:]
	}
	if refPath[0] != '/' {
		refPath = path[:strings.LastIndexByte(path, '/')+1] + refPath
	}
	return origin + removeDots(refPath) + tail
}

// hasScheme reports whether ref starts with a scheme and a colon.
func hasScheme(ref string) bool {
	colon := strings.IndexByte(ref, ':')
	if colon <= 0 {
		return false
	}
	// A colon after the first slash, question mark or hash is not a
	// scheme separator.
	if i := strings.IndexAny(ref, "/?#"); i >= 0 && i < colon {
		return false
	}
	return validScheme(ref[:colon])
}

// removeDots works out "." and ".." segments in an absolute path.
func removeDots(path string) string {
	segments := strings.Split(path, "/")
	out := make([]string, 0, len(segments))
	for i, s := range segments {
		last := i == len(segments)-1
		switch s {
		case ".":
			if last {
				out = append(out, "")
			}
		case "..":
			if len(out) > 1 {
				out = out[:len(out)-1]
			}
			if last {
				out = append(out, "")
			}
		default:
			out = append(out, s)
		}
	}
	joined := strings.Join(out, "/")
	if !strings.HasPrefix(joined, "/") {
		joined = "/" + joined
	}
	return joined
}
