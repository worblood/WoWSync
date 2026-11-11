package wow

import (
	"bytes"
	"path"
	"sort"
	"strings"
)

// DefaultLocalCVars are machine-specific CVars that are never synced:
// graphics, display, window, frame rate, sound devices and UI scale.
// Patterns use * and ? and match case-insensitively.
var DefaultLocalCVars = []string{
	"gx*", "graphics*", "raidGraphics*", "hwDetect", "hwPCF", "videoOptionsVersion",
	"renderScale", "Resample*", "MSAA*", "ffx*", "*AntiAliasing*", "textureFilteringMode",
	"farclip", "horizon*", "groundEffect*", "particle*", "projectedTextures", "shadow*",
	"SSAO*", "sunShafts", "weatherDensity", "waterDetail", "rippleDetail", "reflectionMode",
	"refraction*", "spellClutter", "terrainMipLevel", "worldBaseMip", "*LodDist", "lodObject*",
	"environmentDetail", "componentTexture*", "volumeFog*", "DepthBasedOpacity", "lowLatencyMode",
	"Gamma", "Contrast", "Brightness", "maxFPS*", "targetFPS", "useTargetFPS", "useMaxFPS*",
	"vsync", "monitor*", "Sound_OutputDriver*", "*Device*", "uiScale", "useUiScale",
	"engineSurvey*", "timingMethod", "hardwareCursor", "RAIDsettingsEnabled",
}

// CVarMatcher matches CVar names against glob patterns.
type CVarMatcher []string

// Match reports whether name matches any pattern (case-insensitive).
func (m CVarMatcher) Match(name string) bool {
	n := strings.ToLower(name)
	for _, p := range m {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if ok, _ := path.Match(p, n); ok {
			return true
		}
	}
	return false
}

// ConfigLine is one line of Config.wtf.
type ConfigLine struct {
	Raw   string // original text, used for non-SET lines
	Name  string // CVar name for SET lines
	Value string
	IsSet bool
}

// ParseConfigWTF parses Config.wtf ("SET name "value"" lines). Unknown
// lines are kept verbatim.
func ParseConfigWTF(data []byte) (lines []ConfigLine, crlf bool) {
	crlf = bytes.Contains(data, []byte("\r\n"))
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil, crlf
	}
	for _, raw := range strings.Split(text, "\n") {
		lines = append(lines, parseConfigLine(raw))
	}
	return lines, crlf
}

func parseConfigLine(raw string) ConfigLine {
	t := strings.TrimSpace(raw)
	if len(t) < 4 || !strings.EqualFold(t[:4], "SET ") {
		return ConfigLine{Raw: raw}
	}
	rest := strings.TrimSpace(t[4:])
	i := strings.IndexAny(rest, " \t")
	if i <= 0 {
		return ConfigLine{Raw: raw}
	}
	name := rest[:i]
	val := strings.TrimSpace(rest[i:])
	if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
		val = val[1 : len(val)-1]
	}
	return ConfigLine{Raw: raw, Name: name, Value: val, IsSet: true}
}

// FormatConfigWTF renders lines back into file content.
func FormatConfigWTF(lines []ConfigLine, crlf bool) []byte {
	var b strings.Builder
	nl := "\n"
	if crlf {
		nl = "\r\n"
	}
	for _, l := range lines {
		if l.IsSet {
			b.WriteString("SET " + l.Name + ` "` + l.Value + `"`)
		} else {
			b.WriteString(l.Raw)
		}
		b.WriteString(nl)
	}
	return []byte(b.String())
}

// SyncedCVars returns the CVars that this machine shares: every SET line
// whose name does not match local.
func SyncedCVars(lines []ConfigLine, local CVarMatcher) map[string]string {
	out := map[string]string{}
	for _, l := range lines {
		if l.IsSet && !local.Match(l.Name) {
			out[l.Name] = l.Value
		}
	}
	return out
}

// ChangeKind classifies a difference.
type ChangeKind string

const (
	Added    ChangeKind = "added"
	Modified ChangeKind = "modified"
	Removed  ChangeKind = "removed"
)

// CVarChange is one CVar that a merge changes.
type CVarChange struct {
	Name string
	Old  string
	New  string
	Kind ChangeKind
}

// MergeCVars applies the remote CVar set to the local file. CVars for which
// protected returns true are left untouched. Unprotected local CVars that
// are missing remotely are removed (WoW only writes non-default values).
func MergeCVars(local []ConfigLine, remote map[string]string, protected func(string) bool) ([]ConfigLine, []CVarChange) {
	rem := map[string]string{} // folded name -> remote name
	for name := range remote {
		rem[strings.ToLower(name)] = name
	}
	seen := map[string]bool{}
	var out []ConfigLine
	var changes []CVarChange
	for _, l := range local {
		if !l.IsSet || protected(l.Name) {
			out = append(out, l)
			continue
		}
		key := strings.ToLower(l.Name)
		if seen[key] { // duplicate SET line: drop, WoW uses the last one anyway
			continue
		}
		seen[key] = true
		rname, ok := rem[key]
		if !ok {
			changes = append(changes, CVarChange{Name: l.Name, Old: l.Value, Kind: Removed})
			continue
		}
		if v := remote[rname]; v != l.Value {
			changes = append(changes, CVarChange{Name: l.Name, Old: l.Value, New: v, Kind: Modified})
			l.Value = v
		}
		out = append(out, l)
	}
	var added []string
	for key, rname := range rem {
		if !seen[key] && !protected(rname) {
			added = append(added, rname)
		}
	}
	sort.Strings(added)
	for _, name := range added {
		out = append(out, ConfigLine{Name: name, Value: remote[name], IsSet: true})
		changes = append(changes, CVarChange{Name: name, New: remote[name], Kind: Added})
	}
	return out, changes
}
