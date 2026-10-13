package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	adf "github.com/nyactl/go-adf"

	"github.com/nyactl/dok-for-confluence/internal/confluence"
	"github.com/nyactl/dok-for-confluence/internal/macros"
)

// A page is shown as Markdown with the extended forms of go-adf (expands,
// columns, cards, colors, images), allowed macros as macro blocks, and
// everything else that Markdown cannot express as a placeholder:
//
//	```adf
//	{"type":"kept","attrs":{"id":"k3-1a2b3c","what":"panel"}}
//	```
//
// A placeholder names the part without its content, which can hold people
// and their account IDs. An update puts the stored part back for each
// placeholder; JSON the caller wrote itself is refused, so nothing can be
// added that dok does not check.

const keptType = "kept"

// codec converts one page between ADF and the Markdown dok shows.
type codec struct {
	s *Service
	// stored holds every node of the stored body as stored, in pre-order;
	// the shown copy carries each node's index plus one in Node.Version,
	// which go-adf ignores below the document.
	stored []adf.Node
	used   map[int]bool
	// media are the stored images by the name shown, and attachments the
	// page's files by name, looked up when an image names one.
	media       map[string]adf.Node
	pageID      string
	attachments map[string]confluence.Attachment
	// uploads are files the change will attach before writing.
	uploads map[string]bool
}

func decode(raw string) (adf.Node, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var n adf.Node
	err := dec.Decode(&n)
	return n, err
}

// newCodec prepares a codec for a stored body, which may be nil for a new
// page, and returns the copy of the body to show.
func (s *Service) newCodec(b *confluence.Body, pageID string) (*codec, *adf.Node, error) {
	c := &codec{s: s, used: map[int]bool{}, media: map[string]adf.Node{}, pageID: pageID, uploads: map[string]bool{}}
	if b == nil || strings.TrimSpace(b.ADF.Value) == "" {
		return c, &adf.Node{Type: "doc", Version: 1}, nil
	}
	stored, err := decode(b.ADF.Value)
	if err != nil {
		return nil, nil, fmt.Errorf("page content: %w", err)
	}
	shown, err := decode(b.ADF.Value)
	if err != nil {
		return nil, nil, err
	}
	var index func(n adf.Node)
	index = func(n adf.Node) {
		c.stored = append(c.stored, n)
		for _, ch := range n.Content {
			index(ch)
		}
	}
	for _, ch := range stored.Content {
		index(ch)
	}
	i := 0
	var tag func(n *adf.Node)
	tag = func(n *adf.Node) {
		i++
		n.Version = i
		for j := range n.Content {
			tag(&n.Content[j])
		}
	}
	for j := range shown.Content {
		tag(&shown.Content[j])
	}
	return c, &shown, nil
}

// render shows the body as Markdown.
func (c *codec) render(ctx context.Context, shown *adf.Node) (string, error) {
	if len(shown.Content) == 0 {
		return "", nil
	}
	c.s.learnMentions(ctx, *shown)
	c.s.redactText(shown)
	shown.Content = c.macroBlocks(shown.Content)
	c.collectMedia(*shown)
	raw, err := json.Marshal(shown)
	if err != nil {
		return "", err
	}
	md, err := adf.ToMarkdown(raw, c.options(adf.WithMentionText(c.s.people.Mention))...)
	if err != nil {
		return "", err
	}
	return c.s.people.Text(md), nil
}

func (c *codec) options(extra ...adf.Option) []adf.Option {
	return append([]adf.Option{adf.WithExtendedSyntax(), adf.WithRawNodes(c), adf.WithMediaResolver(c.resolveMedia)}, extra...)
}

// macroBlocks shows allowed macros as macro blocks, and keeps code that
// would read as one.
func (c *codec) macroBlocks(nodes []adf.Node) []adf.Node {
	for i := range nodes {
		n := &nodes[i]
		if n.Type == "codeBlock" && strings.HasPrefix(strAttr(n.Attrs, "language"), "macro:") {
			// Code that would read as a macro block is kept as it is.
			*n = adf.Node{Type: keptType, Version: n.Version}
			continue
		}
		if n.Type == "extension" {
			if b, ok := c.s.macroBlock(*n); ok {
				*n = b
				continue
			}
		}
		n.Content = c.macroBlocks(n.Content)
	}
	return nodes
}

// macroBlock is the code block that shows an allowed macro.
func (s *Service) macroBlock(n adf.Node) (adf.Node, bool) {
	block := func(lang, text string) (adf.Node, bool) {
		// A value with a name in it would come back with the label in
		// place of the name.
		if s.people.Text(text) != text {
			return adf.Node{}, false
		}
		return adf.Node{Type: "codeBlock", Version: n.Version, Attrs: map[string]any{"language": lang}, Content: textContent(text)}, true
	}
	for lang, f := range s.fences {
		if code, ok := s.macros[f.Macro].ReadFence(f, n); ok {
			return block(lang, code)
		}
	}
	for _, d := range s.macroList {
		if v, ok := d.Read(n); ok {
			return block("macro:"+d.Name, d.Block(v))
		}
	}
	return adf.Node{}, false
}

func textContent(s string) []adf.Node {
	if s == "" {
		return nil
	}
	return []adf.Node{{Type: "text", Text: s}}
}

func strAttr(attrs map[string]any, key string) string {
	s, _ := attrs[key].(string)
	return s
}

// collectMedia notes the stored images by the name shown.
func (c *codec) collectMedia(n adf.Node) {
	if n.Type == "media" && n.Version > 0 && n.Version <= len(c.stored) {
		if name := strAttr(n.Attrs, "alt"); name != "" {
			if _, dup := c.media[name]; !dup {
				c.media[name] = c.stored[n.Version-1]
			}
		}
	}
	for _, ch := range n.Content {
		c.collectMedia(ch)
	}
}

// Write implements adf.RawNodes: a placeholder for a stored node.
func (c *codec) Write(n adf.Node) adf.Node {
	i := n.Version - 1
	if i < 0 || i >= len(c.stored) {
		return adf.Node{Type: keptType, Attrs: map[string]any{"id": "unknown"}}
	}
	what := partName(c.stored[i])
	if what == "" || n.Type == keptType {
		what = c.stored[i].Type
	}
	return adf.Node{Type: keptType, Attrs: map[string]any{"id": c.id(i), "what": c.s.people.Text(what)}}
}

// id names a stored part by its position and content. Confluence adds local
// IDs to new parts some moments after storing them, so those do not count.
func (c *codec) id(i int) string {
	sum := sha256.Sum256(macros.Fingerprint(c.stored[i]))
	return "k" + strconv.Itoa(i) + "-" + hex.EncodeToString(sum[:3])
}

var errAuthored = errors.New("JSON parts cannot be added or changed; use the Markdown forms dok shows (see list_macros for macros), or change the page in Confluence")

// Read implements adf.RawNodes: the stored node a placeholder stands for.
func (c *codec) Read(n adf.Node, inline bool) (adf.Node, error) {
	if n.Type != keptType {
		return adf.Node{}, errAuthored
	}
	id := strAttr(n.Attrs, "id")
	num, _, _ := strings.Cut(strings.TrimPrefix(id, "k"), "-")
	i, err := strconv.Atoi(num)
	if err != nil || i < 0 || i >= len(c.stored) || c.id(i) != id {
		return adf.Node{}, fmt.Errorf("placeholder %q does not belong to this version of the page; read it again", id)
	}
	if c.used[i] {
		return adf.Node{}, fmt.Errorf("placeholder %s appears twice; each stored part can be kept once", id)
	}
	if inline != isInline(c.stored[i]) {
		return adf.Node{}, fmt.Errorf("placeholder %s was moved between text and blocks", id)
	}
	c.used[i] = true
	// The mark keeps macros from converting the stored part; it is
	// removed there.
	n = c.stored[i]
	n.Version = restored
	return n, nil
}

// restored marks, in Node.Version, a part put back for a placeholder.
const restored = -1

func isInline(n adf.Node) bool {
	switch n.Type {
	case "text", "hardBreak", "mention", "emoji", "inlineCard", "date", "status", "placeholder", "inlineExtension", "mediaInline":
		return true
	}
	return false
}

// resolveMedia finds the media node of an image by the name it is shown
// with: a stored image, a file attached to the page, or one this change
// uploads.
func (c *codec) resolveMedia(name string) (adf.Node, bool) {
	if m, ok := c.media[name]; ok {
		return m, true
	}
	if c.uploads[name] {
		return adf.Node{Type: "media", Attrs: map[string]any{"type": "file", "id": pendingID(name), "collection": "contentId-" + c.pageID, "alt": name}}, true
	}
	if a, ok := c.attachments[name]; ok && a.FileID != "" {
		return adf.Node{Type: "media", Attrs: map[string]any{"type": "file", "id": a.FileID, "collection": "contentId-" + c.pageID, "alt": name}}, true
	}
	return adf.Node{}, false
}

// pendingID stands for the file ID of an upload until it is known.
func pendingID(name string) string { return "pending:" + name }

// parse converts Markdown to the body to store.
func (c *codec) parse(md string) (json.RawMessage, error) {
	raw, err := adf.FromMarkdown(md, c.options(adf.WithMentionResolver(c.s.people.Resolve))...)
	var me *adf.MentionError
	var mediaErr *adf.MediaError
	switch {
	case errors.As(err, &me):
		return nil, fmt.Errorf("cannot mention @[%s]: only people already shown in this session can be mentioned", me.Label)
	case errors.As(err, &mediaErr):
		return nil, fmt.Errorf("image %q: the page has no attachment of that name; attach the file with the change", mediaErr.Name)
	case errors.Is(err, adf.ErrRawNode):
		return nil, fmt.Errorf("%w; keep each ```adf placeholder exactly as shown, or remove it with its fences", err)
	case err != nil:
		return nil, err
	}
	doc, err := decode(string(raw))
	if err != nil {
		return nil, err
	}
	if doc.Content, err = c.macros(doc.Content); err != nil {
		return nil, err
	}
	return json.Marshal(doc)
}

// macros turns macro blocks and mapped fenced code into macros, reusing a
// stored macro that is the same, so its identifiers stay.
func (c *codec) macros(nodes []adf.Node) ([]adf.Node, error) {
	for i := range nodes {
		n := &nodes[i]
		if n.Version == restored {
			n.Version = 0
			continue
		}
		if n.Type == "codeBlock" {
			lang := strAttr(n.Attrs, "language")
			code := ""
			if len(n.Content) > 0 {
				code = n.Content[0].Text
			}
			var built adf.Node
			var err error
			if name, ok := strings.CutPrefix(lang, "macro:"); ok {
				d, allowed := c.s.macros[name]
				if !allowed {
					return nil, fmt.Errorf("macro %q is not allowed; allowed macros: %s (see list_macros)", name, c.s.macroNames())
				}
				var v macros.Values
				if v, err = d.ParseBlock(code); err == nil {
					if err = c.s.checkValues(d, v); err == nil {
						built, err = d.Build(v)
					}
				}
			} else if f, ok := c.s.fences[lang]; ok {
				built, err = c.s.macros[f.Macro].BuildFence(f, code)
			} else {
				continue
			}
			if err != nil {
				return nil, err
			}
			*n = c.reuse(built)
			continue
		}
		var err error
		if n.Content, err = c.macros(n.Content); err != nil {
			return nil, err
		}
	}
	return nodes, nil
}

// reuse returns an unused stored macro equal to built, or built.
func (c *codec) reuse(built adf.Node) adf.Node {
	for i, n := range c.stored {
		if !c.used[i] && n.Type == "extension" && macros.Same(n, built) {
			c.used[i] = true
			return n
		}
	}
	return built
}

func (s *Service) macroNames() string {
	if len(s.macroList) == 0 {
		return "none (see `dok config macros`)"
	}
	names := make([]string, len(s.macroList))
	for i, d := range s.macroList {
		names[i] = d.Name
	}
	return strings.Join(names, ", ")
}

// checkValues checks values that must stay within what dok may use, such as
// a whiteboard in an allowed space of this site.
func (s *Service) checkValues(d macros.Def, v macros.Values) error {
	for _, p := range d.Params {
		val, ok := v[p.Name]
		if !ok || p.Kind != macros.URL {
			continue
		}
		rest, ok := strings.CutPrefix(val, s.cc.Site()+"/wiki/spaces/")
		key, id, _ := strings.Cut(rest, "/whiteboard/")
		if _, allowed := s.spaceByKey(key); !ok || !allowed || CheckDigits(id) != nil {
			return fmt.Errorf("macro %s: %s must be a whiteboard in one of the allowed spaces, like %s/wiki/spaces/%s/whiteboard/123", d.Name, p.Name, s.cc.Site(), s.keys[0])
		}
	}
	return nil
}

// CheckDigits checks a numeric ID.
func CheckDigits(id string) error {
	if id == "" || strings.Trim(id, "0123456789") != "" || len(id) > 20 {
		return fmt.Errorf("invalid ID %q", id)
	}
	return nil
}

// pageMarkdown shows a page's body.
func (s *Service) pageMarkdown(ctx context.Context, p *confluence.Page) string {
	c, shown, err := s.newCodec(p.Body, p.ID)
	if err != nil {
		return "(content could not be converted)"
	}
	md, err := c.render(ctx, shown)
	if err != nil {
		return "(content could not be converted)"
	}
	return md
}
