// Package config stores dok's non-secret settings per profile: the site,
// account email, privacy mode, the spaces dok may use and optionally the
// page trees it may write to. The API token lives in the Keychain.
// Profiles, validation and the development-build lock come from
// approvekit's profile package.
package config

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/nyactl/approvekit/profile"

	"github.com/nyactl/dok-for-confluence/internal/macros"
)

// PrivacyMode controls whether other people appear by name.
type PrivacyMode string

const (
	PrivacyOwn PrivacyMode = "own"
	PrivacyOff PrivacyMode = "off"
)

// Config is persisted as JSON in the user's config directory.
type Config struct {
	profile.Account
	Privacy PrivacyMode `json:"privacy"`
	// Spaces are the keys of the spaces dok may read and write. Nothing
	// else is visible.
	Spaces []string `json:"spaces,omitempty"`
	// Parents optionally narrows writes to the trees below these pages.
	Parents []string `json:"parents,omitempty"`
	// Macros are the macros dok may create on pages.
	Macros Macros `json:"macros,omitzero"`
}

// Macros lists the macros dok may create: built-in ones by name, and ones
// learned from pages. Fences turn fenced code of a language, such as
// mermaid, into a macro.
type Macros struct {
	Builtin []string                `json:"builtin,omitempty"`
	Learned []macros.Def            `json:"learned,omitempty"`
	Fences  map[string]macros.Fence `json:"fences,omitempty"`
}

// Allowed returns the definitions of the allowed macros. Forbidden macros
// are left out even if the file lists them.
func (m Macros) Allowed() []macros.Def {
	builtin := macros.Builtin()
	var out []macros.Def
	for _, name := range m.Builtin {
		if d, ok := builtin[name]; ok {
			out = append(out, d)
		}
	}
	for _, d := range m.Learned {
		if d.Validate() == nil {
			out = append(out, d)
		}
	}
	return out
}

// Def returns the allowed macro of that name.
func (m Macros) Def(name string) (macros.Def, bool) {
	for _, d := range m.Allowed() {
		if d.Name == name {
			return d, true
		}
	}
	return macros.Def{}, false
}

func (m Macros) validate() error {
	builtin := macros.Builtin()
	seen := map[string]bool{}
	for _, name := range m.Builtin {
		if _, ok := builtin[name]; !ok || seen[name] {
			return fmt.Errorf("unknown or repeated built-in macro %q", name)
		}
		seen[name] = true
	}
	for _, d := range m.Learned {
		if err := d.Validate(); err != nil {
			return err
		}
		if seen[d.Name] {
			return fmt.Errorf("two macros are named %s", d.Name)
		}
		seen[d.Name] = true
	}
	for lang, f := range m.Fences {
		d, ok := m.Def(f.Macro)
		if !macros.ValidName(lang) || !ok {
			return fmt.Errorf("fenced %q code maps to %q, which is not an allowed macro", lang, f.Macro)
		}
		if err := d.CheckFence(f); err != nil {
			return err
		}
	}
	return nil
}

// DefaultProfile is used when no profile is selected; it is meant for
// production.
const DefaultProfile = profile.Default

// ErrNotConfigured is returned by Load when no login has happened yet.
var ErrNotConfigured = errors.New("this profile is not set up yet; run `dok auth login` (add --profile <name> for other profiles)")

// Store locates dok's profiles.
var Store = profile.Store{App: "dok-for-confluence", ProfileEnv: "DOK_PROFILE", ConfigEnv: "DOK_CONFIG"}

// ResolveProfile returns name, or DOK_PROFILE, or DefaultProfile.
func ResolveProfile(name string) (string, error) { return Store.Resolve(name) }

// Path returns the config file of a profile. DOK_CONFIG overrides it.
func Path(name string) (string, error) { return Store.Path(name) }

// Profiles lists the profiles that have a config file.
func Profiles() ([]string, error) { return Store.List() }

// Load reads and validates a profile's config file.
func Load(name string) (*Config, error) {
	var c Config
	if err := Store.Load(name, &c); err != nil {
		if errors.Is(err, profile.ErrNotConfigured) {
			return nil, ErrNotConfigured
		}
		return nil, err
	}
	return &c, nil
}

// Save validates and writes a profile's config file with owner-only
// permissions.
func (c *Config) Save(name string) error { return Store.Save(name, c) }

var (
	spaceKeyRe = regexp.MustCompile(`^~?[A-Za-z0-9_-]{1,255}$`)
	pageIDRe   = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
)

// Validate checks every field. A missing privacy mode means own.
func (c *Config) Validate() error {
	if c.Privacy == "" {
		c.Privacy = PrivacyOwn
	}
	if err := c.Account.Validate(); err != nil {
		return err
	}
	switch c.Privacy {
	case PrivacyOwn, PrivacyOff:
	default:
		return fmt.Errorf("invalid privacy mode %q (want %q or %q)", c.Privacy, PrivacyOwn, PrivacyOff)
	}
	for _, k := range c.Spaces {
		if !spaceKeyRe.MatchString(k) {
			return fmt.Errorf("invalid space key %q", k)
		}
	}
	for _, id := range c.Parents {
		if !pageIDRe.MatchString(id) {
			return fmt.Errorf("invalid parent page ID %q", id)
		}
	}
	return c.Macros.validate()
}

// NormalizeSpaceKey checks a space key. Keys keep their case; Confluence
// matches them regardless of case.
func NormalizeSpaceKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if !spaceKeyRe.MatchString(key) {
		return "", fmt.Errorf("invalid space key %q", key)
	}
	return key, nil
}

// AddSpace adds a space key once, regardless of case; a new spelling
// replaces the old one.
func (c *Config) AddSpace(key string) {
	if i := c.spaceIndex(key); i >= 0 {
		c.Spaces[i] = key
		return
	}
	c.Spaces = append(c.Spaces, key)
}

// RemoveSpace removes a space key, regardless of case.
func (c *Config) RemoveSpace(key string) bool {
	i := c.spaceIndex(key)
	if i < 0 {
		return false
	}
	c.Spaces = slices.Delete(c.Spaces, i, i+1)
	return true
}

func (c *Config) spaceIndex(key string) int {
	return slices.IndexFunc(c.Spaces, func(k string) bool { return strings.EqualFold(k, key) })
}

// ExpiryWarning returns a message when the recorded token expiry is within
// two weeks or has passed, and "" otherwise.
func (c *Config) ExpiryWarning(now time.Time) string {
	return c.Account.ExpiryWarning(now, "run `dok auth login`")
}
