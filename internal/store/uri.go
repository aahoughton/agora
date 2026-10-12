package store

import "strings"

// fileURI is the SQLite URI for the file at path. SQLite decodes %HH
// escapes in a URI's path, so a literal '%' is escaped to keep SQLite on
// the same file as Go; callers refuse '?' and '#'.
func fileURI(path, query string) string {
	return "file:" + strings.ReplaceAll(path, "%", "%25") + "?" + query
}
