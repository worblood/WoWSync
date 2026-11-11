package wow

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// NormalizeRel turns a relative OS path into the canonical form used in
// snapshots: forward slashes, Unicode NFC (macOS may hand out NFD names),
// cleaned, without leading "./" or "/".
func NormalizeRel(p string) string {
	p = filepath.ToSlash(p)
	p = norm.NFC.String(p)
	p = path.Clean("/" + p)
	return strings.TrimPrefix(p, "/")
}

// Fold returns the comparison key for a normalized path. Windows and macOS
// file systems are case-insensitive by default, so paths are matched
// case-insensitively everywhere.
func Fold(p string) string {
	return strings.ToLower(norm.NFC.String(p))
}

var reservedNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// ValidateRel checks that a snapshot path is safe to write on every
// supported OS: relative, no "..", no characters Windows rejects, no
// reserved device names. Paths coming from the store are untrusted.
func ValidateRel(p string) error {
	if p == "" {
		return errors.New("empty path")
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return fmt.Errorf("%q: must be a relative slash path", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("%q: invalid path segment %q", p, seg)
		}
		for _, r := range seg {
			if r < 0x20 || strings.ContainsRune(`<>:"|?*`, r) {
				return fmt.Errorf("%q: invalid character %q", p, r)
			}
		}
		if strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, " ") {
			return fmt.Errorf("%q: segment %q ends with dot or space", p, seg)
		}
		base := strings.ToLower(seg)
		if i := strings.IndexByte(base, '.'); i >= 0 {
			base = base[:i]
		}
		if reservedNames[base] {
			return fmt.Errorf("%q: reserved name %q", p, seg)
		}
	}
	return nil
}

var textExtensions = map[string]bool{
	".lua": true, ".txt": true, ".wtf": true, ".toc": true, ".xml": true,
	".md": true, ".json": true, ".csv": true, ".html": true, ".htm": true,
	".yml": true, ".yaml": true, ".ini": true, ".cfg": true, ".pkgmeta": true,
}

// IsTextFile reports whether a file is treated as text, which means its
// line endings are normalized to LF in the store.
func IsTextFile(name string) bool {
	return textExtensions[strings.ToLower(path.Ext(name))]
}
