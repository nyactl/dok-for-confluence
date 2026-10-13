// Package macros describes the Confluence macros dok may create: a few
// built-in ones and those learned from a page where a macro was inserted in
// the editor. A definition holds the macro's identity and a template of its
// parameters, never page content or people. Pages show allowed macros as
//
//	```macro:toc
//	maxLevel: 3
//	```
//
// and the block is turned back into the macro node, with every value
// checked against the definition.
package macros

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	adf "github.com/nyactl/go-adf"
)

// Kinds of parameter values.
const (
	String  = "string"
	Number  = "number"
	Boolean = "boolean"
	Enum    = "enum"
	// URL values are checked by the caller, e.g. against the allowed spaces.
	URL = "url"
)

// Param is one parameter a macro block may set.
type Param struct {
	Name string `json:"name"`
	// Path leads to the value in the macro's parameters.
	Path []string `json:"path"`
	Kind string   `json:"kind"`
	// Values are the allowed values of an Enum.
	Values []string `json:"values,omitempty"`
	// Wrapped values are stored as {"value": "..."}, as legacy and
	// built-in macros do; Path ends before the wrapper.
	Wrapped bool `json:"wrapped,omitempty"`
}

// Def is a macro dok may create.
type Def struct {
	Name          string `json:"name"`
	Title         string `json:"title"`
	ExtensionType string `json:"extensionType"`
	ExtensionKey  string `json:"extensionKey"`
	// Template holds the attributes of a new macro other than its
	// parameters, and Parameters the parameters before values are set.
	Template   map[string]any `json:"template,omitempty"`
	Parameters map[string]any `json:"parameters"`
	Params     []Param        `json:"params"`
	// Builtin is set for definitions that come with dok.
	Builtin bool `json:"-"`
}

// Fence maps fenced code of a language to a macro, whose parameter Param
// holds the code.
type Fence struct {
	Macro string `json:"macro"`
	Param string `json:"param"`
}

// CheckFence checks that fenced code can go into the parameter.
func (d Def) CheckFence(f Fence) error {
	p, ok := d.param(f.Param)
	if !ok || p.Kind != String {
		return fmt.Errorf("macro %s has no text parameter %q for fenced code; its parameters: %s", d.Name, f.Param, d.ParamNames())
	}
	return nil
}

// Builtin returns the macros that come with dok, by name.
func Builtin() map[string]Def {
	core := func(name, key, title, schema string, params ...Param) Def {
		for i := range params {
			params[i].Wrapped = true
			params[i].Path = []string{"macroParams", params[i].Name}
		}
		return Def{Name: name, Title: title, ExtensionType: "com.atlassian.confluence.macro.core", ExtensionKey: key, Builtin: true,
			Template:   map[string]any{"layout": "default"},
			Parameters: map[string]any{"macroParams": map[string]any{}, "macroMetadata": map[string]any{"schemaVersion": map[string]any{"value": schema}, "title": title}},
			Params:     params}
	}
	defs := []Def{
		// Titles and schema versions as Confluence stores them.
		core("toc", "toc", "Table of Contents", "1",
			Param{Name: "maxLevel", Kind: Number}, Param{Name: "minLevel", Kind: Number},
			Param{Name: "style", Kind: Enum, Values: []string{"default", "none", "disc", "circle", "square", "decimal", "lower-alpha", "lower-roman", "upper-roman"}},
			Param{Name: "type", Kind: Enum, Values: []string{"list", "flat"}},
			Param{Name: "outline", Kind: Boolean}, Param{Name: "printable", Kind: Boolean},
			Param{Name: "include", Kind: String}, Param{Name: "exclude", Kind: String}),
		core("children", "children", "Child pages", "2",
			Param{Name: "depth", Kind: Number}, Param{Name: "first", Kind: Number}, Param{Name: "all", Kind: Boolean},
			Param{Name: "sort", Kind: Enum, Values: []string{"creation", "title", "modified"}}, Param{Name: "reverse", Kind: Boolean},
			Param{Name: "style", Kind: Enum, Values: []string{"h1", "h2", "h3", "h4", "h5", "h6"}},
			Param{Name: "excerptType", Kind: Enum, Values: []string{"none", "simple", "rich content"}}),
		core("whiteboard", "native-embed:whiteboard", "native-embed:whiteboard", "1", Param{Name: "url", Kind: URL}),
	}
	out := map[string]Def{}
	for _, d := range defs {
		out[d.Name] = d
	}
	return out
}

// forbiddenRe matches the keys of macros that show content from elsewhere,
// such as other pages or spaces, or that embed HTML or scripts. They are
// never created, whether built in, learned or listed by hand.
var forbiddenRe = regexp.MustCompile(`(?i)(include|excerpt|iframe|html|script|widget|rss|embed-url|gadget|content-by-label|contentbylabel|blog-posts|recently-updated|livesearch|search)`)

// Forbidden reports whether a macro key may never be created. Confluence's
// own embeds of whiteboards and the like are not forbidden.
func Forbidden(key string) bool {
	if key == "native-embed:whiteboard" {
		return false
	}
	return forbiddenRe.MatchString(key)
}

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

// ValidName reports whether name can name a macro in a block.
func ValidName(name string) bool { return nameRe.MatchString(name) }

// Validate checks a definition, for example one read from a config file.
func (d Def) Validate() error {
	switch {
	case !ValidName(d.Name):
		return fmt.Errorf("invalid macro name %q", d.Name)
	case d.ExtensionType == "" || d.ExtensionKey == "":
		return fmt.Errorf("macro %s: missing extension type or key", d.Name)
	case Forbidden(d.ExtensionKey):
		return fmt.Errorf("macro %s (%s) shows content from elsewhere or embeds code, which dok never creates", d.Name, d.ExtensionKey)
	}
	seen := map[string]bool{}
	for _, p := range d.Params {
		switch {
		case p.Name == "" || seen[p.Name] || strings.ContainsAny(p.Name, ":\n"):
			return fmt.Errorf("macro %s: invalid or duplicate parameter %q", d.Name, p.Name)
		case len(p.Path) == 0:
			return fmt.Errorf("macro %s: parameter %s has no path", d.Name, p.Name)
		case !slices.Contains([]string{String, Number, Boolean, Enum, URL}, p.Kind):
			return fmt.Errorf("macro %s: parameter %s has unknown kind %q", d.Name, p.Name, p.Kind)
		case p.Kind == Enum && len(p.Values) == 0:
			return fmt.Errorf("macro %s: parameter %s lists no values", d.Name, p.Name)
		}
		seen[p.Name] = true
	}
	return nil
}

func (d Def) param(name string) (Param, bool) {
	for _, p := range d.Params {
		if p.Name == name {
			return p, true
		}
	}
	return Param{}, false
}

// ParamNames lists the parameters, for messages.
func (d Def) ParamNames() string {
	names := make([]string, len(d.Params))
	for i, p := range d.Params {
		names[i] = p.Name
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// Values are the parameters a macro block sets, by name, as written.
type Values map[string]string

// ParseBlock reads the lines of a macro block: one "name: value" per line.
func (d Def) ParseBlock(body string) (Values, error) {
	v := Values{}
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok || name == "" {
			return nil, fmt.Errorf("macro %s: write each parameter as \"name: value\", not %q", d.Name, line)
		}
		if _, ok := d.param(name); !ok {
			return nil, fmt.Errorf("macro %s has no parameter %q; it takes: %s", d.Name, name, d.ParamNames())
		}
		if _, dup := v[name]; dup {
			return nil, fmt.Errorf("macro %s: parameter %s is set twice", d.Name, name)
		}
		v[name] = value
	}
	return v, nil
}

// Block writes the lines of a macro block, in the order of the definition.
func (d Def) Block(v Values) string {
	var lines []string
	for _, p := range d.Params {
		if s, ok := v[p.Name]; ok {
			lines = append(lines, p.Name+": "+s)
		}
	}
	return strings.Join(lines, "\n")
}

func (p Param) value(s string) (any, error) {
	switch p.Kind {
	case Number:
		if _, err := strconv.ParseFloat(s, 64); err != nil {
			return nil, fmt.Errorf("parameter %s takes a number, not %q", p.Name, s)
		}
		if p.Wrapped {
			return s, nil
		}
		return json.Number(s), nil
	case Boolean:
		if s != "true" && s != "false" {
			return nil, fmt.Errorf("parameter %s takes true or false, not %q", p.Name, s)
		}
		if p.Wrapped {
			return s, nil
		}
		return s == "true", nil
	case Enum:
		if !slices.Contains(p.Values, s) {
			return nil, fmt.Errorf("parameter %s takes one of %s, not %q", p.Name, strings.Join(p.Values, ", "), s)
		}
	}
	if strings.ContainsAny(s, "\x00") {
		return nil, fmt.Errorf("parameter %s holds a control character", p.Name)
	}
	return s, nil
}

func text(v any) (string, bool) {
	switch v := v.(type) {
	case string:
		return v, true
	case json.Number:
		return v.String(), true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(v), true
	}
	return "", false
}

// Build makes a new macro node from a block's values.
func (d Def) Build(v Values) (adf.Node, error) {
	params := clone(d.Parameters).(map[string]any)
	for _, p := range d.Params {
		s, ok := v[p.Name]
		if !ok {
			continue
		}
		val, err := p.value(s)
		if err != nil {
			return adf.Node{}, fmt.Errorf("macro %s: %w", d.Name, err)
		}
		if p.Wrapped {
			val = map[string]any{"value": val}
		}
		set(params, p.Path, val)
	}
	if meta, ok := params["macroMetadata"].(map[string]any); ok {
		meta["macroId"] = map[string]any{"value": uuid()}
	}
	attrs := clone(d.Template).(map[string]any)
	if attrs == nil {
		attrs = map[string]any{}
	}
	attrs["extensionType"] = d.ExtensionType
	attrs["extensionKey"] = d.ExtensionKey
	attrs["parameters"] = params
	attrs["localId"] = uuid()
	return adf.Node{Type: "extension", Attrs: attrs}, nil
}

// BuildFence makes a new macro node holding fenced code.
func (d Def) BuildFence(f Fence, code string) (adf.Node, error) {
	n, err := d.Build(nil)
	if err != nil {
		return adf.Node{}, err
	}
	p, _ := d.param(f.Param)
	params := n.Attrs["parameters"].(map[string]any)
	var val any = code
	if p.Wrapped {
		val = map[string]any{"value": code}
	}
	set(params, p.Path, val)
	return n, nil
}

// ReadFence returns the code of a macro node made by BuildFence.
func (d Def) ReadFence(f Fence, n adf.Node) (string, bool) {
	if n.Type != "extension" || n.Attrs["extensionType"] != d.ExtensionType || n.Attrs["extensionKey"] != d.ExtensionKey || len(n.Content) > 0 || len(n.Marks) > 0 {
		return "", false
	}
	p, ok := d.param(f.Param)
	if !ok {
		return "", false
	}
	params, _ := n.Attrs["parameters"].(map[string]any)
	val, ok := get(params, p.Path)
	if p.Wrapped {
		m, isMap := val.(map[string]any)
		if !isMap || len(m) != 1 {
			return "", false
		}
		val = m["value"]
	}
	code, isString := val.(string)
	if !ok || !isString {
		return "", false
	}
	built, err := d.BuildFence(f, code)
	if err != nil || !Same(built, n) {
		return "", false
	}
	return code, true
}

// Read returns the values of a macro node made from this definition, if
// it holds nothing the block cannot express.
func (d Def) Read(n adf.Node) (Values, bool) {
	if n.Type != "extension" || n.Attrs["extensionType"] != d.ExtensionType || n.Attrs["extensionKey"] != d.ExtensionKey || len(n.Content) > 0 || len(n.Marks) > 0 {
		return nil, false
	}
	params, _ := n.Attrs["parameters"].(map[string]any)
	v := Values{}
	for _, p := range d.Params {
		val, ok := get(params, p.Path)
		if !ok {
			continue
		}
		if p.Wrapped {
			m, isMap := val.(map[string]any)
			if !isMap || len(m) != 1 {
				return nil, false
			}
			val = m["value"]
		}
		s, ok := text(val)
		if !ok || strings.Contains(s, "\n") {
			return nil, false
		}
		v[p.Name] = s
	}
	built, err := d.Build(v)
	if err != nil || !Same(built, n) {
		return nil, false
	}
	return v, true
}

// volatile are keys that differ between equal macros: identifiers the
// editor gives each one, and the page context it stores.
var volatile = map[string]bool{"localId": true, "macroId": true, "embeddedMacroContext": true}

// metadata is rewritten by Confluence when it stores a macro (its title
// and schema version), so comparisons leave it out.
var metadata = map[string]bool{"macroMetadata": true}

// Fingerprint identifies a node by its content, without the identifiers and
// metadata Confluence adds or rewrites after a page was stored.
func Fingerprint(n adf.Node) []byte {
	n.Version = 0
	raw, _ := json.Marshal(n)
	var v any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	_ = dec.Decode(&v)
	out, _ := json.Marshal(strip(v))
	return out
}

// Same reports whether two macro nodes are equal apart from identifiers
// and the version field, which holds nothing below the document.
func Same(a, b adf.Node) bool {
	a.Version, b.Version = 0, 0
	norm := func(n adf.Node) any {
		raw, _ := json.Marshal(n)
		var v any
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.UseNumber()
		_ = dec.Decode(&v)
		return strip(v)
	}
	return reflect.DeepEqual(norm(a), norm(b))
}

func dropVolatile(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, x := range v {
			if !volatile[k] {
				out[k] = dropVolatile(x)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = dropVolatile(x)
		}
		return out
	}
	return v
}

// strip drops volatile keys, metadata and Confluence's internal keys,
// which start with _ and come and go (a new child pages macro briefly has
// _parentId), and makes equal numbers compare equal.
func strip(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, x := range v {
			if !volatile[k] && !metadata[k] && !strings.HasPrefix(k, "_") {
				out[k] = strip(x)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = strip(x)
		}
		return out
	case json.Number:
		// 1 and 1.0 are the same number.
		if f, err := v.Float64(); err == nil {
			return f
		}
	}
	return v
}

// Learn makes a definition from a macro node on a page. It keeps the
// macro's identity and parameter template, without identifiers or page
// context, and offers every parameter value as a parameter of its kind.
func Learn(n adf.Node, name string) (Def, error) {
	if n.Type != "extension" {
		return Def{}, fmt.Errorf("only macros without a body can be learned, not %s", n.Type)
	}
	key, _ := n.Attrs["extensionKey"].(string)
	typ, _ := n.Attrs["extensionType"].(string)
	if Forbidden(key) {
		return Def{}, fmt.Errorf("macro %s shows content from elsewhere or embeds code, which dok never creates", key)
	}
	attrs := dropVolatile(clone(n.Attrs)).(map[string]any)
	params, _ := attrs["parameters"].(map[string]any)
	delete(attrs, "parameters")
	delete(attrs, "extensionType")
	delete(attrs, "extensionKey")
	if params == nil {
		params = map[string]any{}
	}
	d := Def{Name: name, Title: Title(n), ExtensionType: typ, ExtensionKey: key, Template: attrs, Parameters: params}
	var walk func(path []string, v any)
	walk = func(path []string, v any) {
		switch x := v.(type) {
		case map[string]any:
			if len(path) == 2 && path[0] == "macroParams" {
				if inner, ok := x["value"]; ok && len(x) == 1 {
					if s, ok := text(inner); ok {
						d.Params = append(d.Params, Param{Name: path[1], Path: path, Kind: kindOf(inner, s), Wrapped: true})
						return
					}
				}
			}
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(append(append([]string(nil), path...), k), x[k])
			}
		case string, json.Number, bool:
			s, _ := text(x)
			d.Params = append(d.Params, Param{Name: strings.Join(path, "."), Path: path, Kind: kindOf(x, s)})
		}
	}
	for _, section := range []string{"macroParams", "guestParams"} {
		if v, ok := params[section]; ok {
			walk([]string{section}, v)
		}
	}
	// The template keeps settings, not the learning page's text or people:
	// text longer than a setting is left out until a block sets it, and
	// values that name people are dropped.
	var kept []Param
	for _, p := range d.Params {
		v, _ := get(params, p.Path)
		s, _ := text(v)
		if p.Wrapped {
			if m, ok := v.(map[string]any); ok {
				s, _ = text(m["value"])
			}
		}
		if personRe.MatchString(strings.Join(p.Path, ".")) || accountIDRe.MatchString(s) {
			unset(params, p.Path)
			continue
		}
		if p.Kind == String && !settingRe.MatchString(s) {
			unset(params, p.Path)
		}
		kept = append(kept, p)
	}
	d.Params = kept
	for i := range d.Params {
		// macroParams are named by their own key; guest parameters by their
		// path below guestParams.
		d.Params[i].Name = strings.TrimPrefix(strings.TrimPrefix(d.Params[i].Name, "guestParams."), "macroParams.")
		if d.Params[i].Name == "" {
			d.Params[i].Name = "value"
		}
	}
	return d, d.Validate()
}

func kindOf(v any, s string) string {
	switch v.(type) {
	case bool:
		return Boolean
	case json.Number:
		return Number
	}
	if s == "true" || s == "false" {
		return Boolean
	}
	return String
}

// Title names a macro node for people.
func Title(n adf.Node) string {
	if p, ok := n.Attrs["parameters"].(map[string]any); ok {
		if t, ok := p["extensionTitle"].(string); ok && t != "" {
			return t
		}
		if m, ok := p["macroMetadata"].(map[string]any); ok {
			if t, ok := m["title"].(string); ok && t != "" {
				return t
			}
		}
	}
	if t, ok := n.Attrs["text"].(string); ok && t != "" {
		return t
	}
	if k, ok := n.Attrs["extensionKey"].(string); ok && k != "" {
		return k[strings.LastIndex(k, "/")+1:]
	}
	return ""
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// Slug proposes a macro name from a title.
func Slug(title string) string {
	s := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		s = strings.TrimSuffix("macro-"+s, "-")
	}
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	return s
}

var (
	// settingRe matches text values kept as defaults: one short word.
	settingRe   = regexp.MustCompile(`^[A-Za-z0-9_.#-]{0,40}$`)
	personRe    = regexp.MustCompile(`(?i)(account|user|people|person|mention|email|owner|assignee|author|creator|reporter)`)
	accountIDRe = regexp.MustCompile(`^([0-9]{6}:[0-9a-f-]{36}|[0-9a-f]{24}|[0-9]+:[0-9a-f]{8}-[0-9a-f-]{27})$`)
)

func unset(m map[string]any, path []string) {
	for _, k := range path[:len(path)-1] {
		next, ok := m[k].(map[string]any)
		if !ok {
			return
		}
		m = next
	}
	delete(m, path[len(path)-1])
}

func get(m map[string]any, path []string) (any, bool) {
	var cur any = m
	for _, k := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = mm[k]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func set(m map[string]any, path []string, v any) {
	for _, k := range path[:len(path)-1] {
		next, ok := m[k].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[k] = next
		}
		m = next
	}
	m[path[len(path)-1]] = v
}

func clone(v any) any {
	switch v := v.(type) {
	case map[string]any:
		if v == nil {
			return map[string]any(nil)
		}
		out := make(map[string]any, len(v))
		for k, x := range v {
			out[k] = clone(x)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = clone(x)
		}
		return out
	}
	return v
}

func uuid() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(errors.New("macros: no randomness: " + err.Error()))
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
