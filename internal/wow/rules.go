package wow

// Category is a user-selectable group of files to sync.
type Category struct {
	ID          string
	Name        string
	Description string
	// Patterns are anchored globs relative to the flavor folder.
	Patterns []string
	// CVarMerge marks Config.wtf, which is merged per CVar instead of
	// being copied as a whole file.
	CVarMerge bool
}

// ConfigWTF is the client settings file merged per CVar.
const ConfigWTF = "WTF/Config.wtf"

// Categories in classification order: the first match wins, so "other" must
// stay last.
var Categories = []Category{
	{
		ID: "addons", Name: "AddOns",
		Description: "The Interface/AddOns folder and each character's AddOns.txt (enabled addons).",
		Patterns:    []string{"Interface/AddOns/**", "WTF/Account/*/*/*/AddOns.txt"},
	},
	{
		ID: "savedvariables", Name: "Addon settings (SavedVariables)",
		Description: "Addon data for the account and for every character.",
		Patterns:    []string{"WTF/Account/*/SavedVariables/**", "WTF/Account/*/*/*/SavedVariables/**"},
	},
	{
		ID: "keybindings", Name: "Key bindings",
		Description: "bindings-cache.wtf for the account and characters.",
		Patterns:    []string{"WTF/Account/**/bindings-cache.wtf"},
	},
	{
		ID: "macros", Name: "Macros",
		Description: "macros-cache.txt for the account and characters.",
		Patterns:    []string{"WTF/Account/**/macros-cache.txt"},
	},
	{
		ID: "chat", Name: "Chat windows",
		Description: "chat-cache.txt for every character.",
		Patterns:    []string{"WTF/Account/**/chat-cache.txt"},
	},
	{
		ID: "gamesettings", Name: "Account & character settings",
		Description: "config-cache.wtf and layout-local.txt.",
		Patterns:    []string{"WTF/Account/**/config-cache.wtf", "WTF/Account/**/layout-local.txt"},
	},
	{
		ID: "config", Name: "Client settings (Config.wtf)",
		Description: "Merged per CVar. Machine-specific CVars (graphics, window, sound device …) stay local.",
		Patterns:    []string{ConfigWTF},
		CVarMerge:   true,
	},
	{
		ID: "other", Name: "Other WTF files",
		Description: "Everything else inside WTF.",
		Patterns:    []string{"WTF/**"},
	},
}

// CategoryByID returns the category with the given id.
func CategoryByID(id string) (Category, bool) {
	for _, c := range Categories {
		if c.ID == id {
			return c, true
		}
	}
	return Category{}, false
}

// ScanRoots are the folders (relative to a flavor folder) that are scanned.
var ScanRoots = []string{"WTF", "Interface/AddOns"}

// DefaultExcludes are always applied, in addition to user patterns.
var DefaultExcludes = []string{
	"/Cache/", "/Logs/", "/Errors/", "/Screenshots/",
	"*.bak", "*.old", "*.tmp", "*.temp", "~*", "._*",
	".DS_Store", "Thumbs.db", "desktop.ini",
	".git/", ".svn/", ".hg/",
	"cache.md5",
	".wowsync-*", ".tmp-*", // WoWSync's own temp files
}

// Classify returns the category of a path relative to a flavor folder, or
// "" if the path is outside every category.
func Classify(rel string) string {
	for _, c := range Categories {
		for _, p := range c.Patterns {
			if MatchGlob(p, rel) {
				return c.ID
			}
		}
	}
	return ""
}

// Rules decide which files of a flavor are synced on this machine.
type Rules struct {
	enabled map[string]bool
	ex      *Excluder
}

// NewRules builds rules from the enabled category ids and extra exclude patterns.
func NewRules(enabled map[string]bool, extraExcludes []string) *Rules {
	return &Rules{enabled: enabled, ex: NewExcluder(DefaultExcludes, extraExcludes)}
}

// Enabled reports whether a category is switched on.
func (r *Rules) Enabled(id string) bool { return r.enabled[id] }

// ExcludedDir reports whether a folder can be skipped while scanning.
func (r *Rules) ExcludedDir(rel string) bool { return r.ex.Excluded(rel, true) }

// Managed reports whether this machine syncs the file at rel as a whole
// file. Config.wtf is never a managed file: it is handled per CVar.
func (r *Rules) Managed(rel string) bool {
	if r.ex.Excluded(rel, false) {
		return false
	}
	id := Classify(rel)
	if id == "" || !r.enabled[id] {
		return false
	}
	c, _ := CategoryByID(id)
	return !c.CVarMerge
}

// DefaultCategories returns every category switched on.
func DefaultCategories() map[string]bool {
	m := map[string]bool{}
	for _, c := range Categories {
		m[c.ID] = true
	}
	return m
}
