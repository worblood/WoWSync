package wow

import (
	"path"
	"strings"
)

// MatchGlob matches a slash path against a slash pattern, case-insensitively.
// Each segment supports path.Match syntax (* ? [..]); a "**" segment matches
// zero or more segments.
func MatchGlob(pattern, name string) bool {
	return matchSegs(splitSegs(strings.ToLower(pattern)), splitSegs(strings.ToLower(name)))
}

func splitSegs(s string) []string {
	s = strings.Trim(s, "/")
	if s == "" {
		return nil
	}
	return strings.Split(s, "/")
}

func matchSegs(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			rest := pat[1:]
			for i := 0; i <= len(name); i++ {
				if matchSegs(rest, name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], name[0])
		if err != nil || !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}

// Excluder applies gitignore-like exclude patterns to paths relative to a
// flavor folder:
//
//	name        matches a file or folder with that name at any depth
//	name/       matches only folders
//	/a/b, a/b   containing a slash: anchored at the flavor folder
//
// Matching is case-insensitive.
type Excluder struct {
	pats []exPattern
}

type exPattern struct {
	segs     []string
	dirOnly  bool
	anchored bool
}

// NewExcluder compiles patterns. Empty lines and lines starting with # are ignored.
func NewExcluder(patterns ...[]string) *Excluder {
	e := &Excluder{}
	for _, list := range patterns {
		for _, p := range list {
			p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
			if p == "" || strings.HasPrefix(p, "#") {
				continue
			}
			ep := exPattern{dirOnly: strings.HasSuffix(p, "/")}
			ep.anchored = strings.HasPrefix(p, "/") || strings.Contains(strings.Trim(p, "/"), "/")
			ep.segs = splitSegs(strings.ToLower(p))
			if len(ep.segs) > 0 {
				e.pats = append(e.pats, ep)
			}
		}
	}
	return e
}

// Excluded reports whether rel (slash path relative to the flavor folder)
// or any of its parent folders is excluded.
func (e *Excluder) Excluded(rel string, isDir bool) bool {
	segs := splitSegs(strings.ToLower(rel))
	for _, p := range e.pats {
		if p.anchored {
			for k := 1; k <= len(segs); k++ {
				prefixIsDir := k < len(segs) || isDir
				if p.dirOnly && !prefixIsDir {
					continue
				}
				if matchSegs(p.segs, segs[:k]) {
					return true
				}
			}
			continue
		}
		for i, s := range segs {
			segIsDir := i < len(segs)-1 || isDir
			if p.dirOnly && !segIsDir {
				continue
			}
			// Unanchored patterns contain no slash, so they have one segment.
			if ok, _ := path.Match(p.segs[0], s); ok {
				return true
			}
		}
	}
	return false
}
