package core

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	adf "github.com/nyactl/go-adf"

	"github.com/nyactl/dok-for-confluence/internal/confluence"
)

// Pages are edited as Markdown, but not everything on a page has a Markdown
// form: app macros, images, layouts, expands and some formatting do not
// survive the trip from ADF to Markdown and back. An update replaces the
// whole body, so these parts would be deleted without showing up in the
// diff. lostParts finds them by making that round trip with the stored
// body, and an update that would remove any of them is refused.

// macroTypes are the ADF nodes that hold app macros.
var macroTypes = map[string]string{
	"extension":       "macro",
	"bodiedExtension": "macro",
	"inlineExtension": "inline macro",
}

// partNames describe node and mark types for people; others go by their
// ADF type.
var partNames = map[string]string{
	"layoutSection":        "column layout",
	"mediaSingle":          "image or file",
	"mediaGroup":           "image or file",
	"mediaInline":          "inline file",
	"expand":               "expand",
	"nestedExpand":         "expand",
	"panel":                "panel",
	"status":               "status lozenge",
	"date":                 "date",
	"emoji":                "emoji",
	"inlineCard":           "smart link",
	"blockCard":            "smart link card",
	"embedCard":            "embedded link",
	"taskList":             "task list",
	"decisionList":         "decision list",
	"mark:textColor":       "colored text",
	"mark:backgroundColor": "highlighted text",
	"mark:underline":       "underlined text",
	"mark:subsup":          "sub- or superscript",
	"mark:alignment":       "text alignment",
	"mark:indentation":     "indentation",
}

var plainTypes = map[string]bool{"paragraph": true, "heading": true, "bulletList": true, "orderedList": true, "codeBlock": true,
	"blockquote": true, "rule": true, "table": true, "hardBreak": true, "mention": true}

// skipped are counted through their parent or carry no content of their own.
var skipped = map[string]bool{"doc": true, "text": true, "media": true, "caption": true, "layoutColumn": true,
	"listItem": true, "taskItem": true, "blockTaskItem": true, "decisionItem": true, "tableRow": true, "tableCell": true, "tableHeader": true}

// macroTitle names a macro. Confluence's own embeds, such as whiteboards,
// are titled "native-embed:<kind>".
func macroTitle(n adf.Node) string {
	t := rawMacroTitle(n)
	if kind, ok := strings.CutPrefix(t, "native-embed:"); ok {
		return "embedded " + kind
	}
	return t
}

func rawMacroTitle(n adf.Node) string {
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

// partName describes a node, or "" for nodes that are not counted.
func partName(n adf.Node) string {
	if kind, ok := macroTypes[n.Type]; ok {
		if t := macroTitle(n); t != "" {
			return fmt.Sprintf("%s %q", kind, t)
		}
		return kind
	}
	if skipped[n.Type] {
		return ""
	}
	// Text structure has a Markdown form; whatever Markdown cannot hold in
	// a place, such as a heading in a list, goes through a placeholder. The
	// editor's empty paragraphs and headings are dropped, which is no loss.
	if plainTypes[n.Type] {
		return ""
	}
	if name, ok := partNames[n.Type]; ok {
		return name
	}
	return n.Type
}

// countParts counts the parts of a document by description.
func countParts(n adf.Node, into map[string]int) {
	if name := partName(n); name != "" {
		into[name]++
	}
	for _, m := range n.Marks {
		if name, ok := partNames["mark:"+m.Type]; ok {
			into[name]++
		}
	}
	for _, c := range n.Content {
		countParts(c, into)
	}
}

func parts(raw []byte) (map[string]int, error) {
	var doc adf.Node
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	out := map[string]int{}
	countParts(doc, out)
	return out, nil
}

// lostParts names the parts of the stored body that cannot be represented
// in Markdown and that the new body would no longer contain.
func (s *Service) lostParts(ctx context.Context, stored *confluence.Body, pageID string, updated []byte) ([]string, error) {
	if stored == nil || strings.TrimSpace(stored.ADF.Value) == "" {
		return nil, nil
	}
	before, err := parts([]byte(stored.ADF.Value))
	if err != nil {
		return nil, fmt.Errorf("cannot check the stored page content: %w", err)
	}
	// What a read followed by an unchanged write would keep.
	kept := map[string]int{}
	c, shown, err := s.newCodec(stored, pageID)
	if err != nil {
		return nil, err
	}
	if md, err := c.render(ctx, shown); err == nil {
		if rt, err := c.parse(md); err == nil {
			if kept, err = parts(rt); err != nil {
				return nil, err
			}
		}
	}
	after, err := parts(updated)
	if err != nil {
		return nil, err
	}
	var lost []string
	for _, name := range slices.Sorted(maps.Keys(before)) {
		unrepresentable := before[name] - kept[name]
		removed := before[name] - after[name]
		if n := min(unrepresentable, removed); n > 0 {
			if n > 1 {
				name = fmt.Sprintf("%s (%d)", name, n)
			}
			lost = append(lost, s.people.Text(name))
		}
	}
	return lost, nil
}

// markMacros replaces app macros, which have no Markdown form, with a note,
// so readers know they are there. The content of a macro with a body stays.
func markMacros(nodes []adf.Node) []adf.Node {
	out := make([]adf.Node, 0, len(nodes))
	for _, n := range nodes {
		kind, ok := macroTypes[n.Type]
		if !ok {
			n.Content = markMacros(n.Content)
			out = append(out, n)
			continue
		}
		note := "(" + kind + " not shown)"
		if t := macroTitle(n); t != "" {
			note = fmt.Sprintf("(%s %q not shown)", kind, t)
		}
		if n.Type == "inlineExtension" {
			out = append(out, adf.Node{Type: "text", Text: note})
			continue
		}
		out = append(out, adf.Node{Type: "blockquote", Content: []adf.Node{{Type: "paragraph", Content: []adf.Node{{Type: "text", Text: note}}}}})
		out = append(out, markMacros(n.Content)...)
	}
	return out
}

// partChanges names the parts the updated body has more and fewer of than
// the stored one; stored is nil for a new page.
func partChanges(stored, updated []byte) (added, removed []string, err error) {
	before := map[string]int{}
	if len(stored) > 0 {
		if before, err = parts(stored); err != nil {
			return nil, nil, err
		}
	}
	after, err := parts(updated)
	if err != nil {
		return nil, nil, err
	}
	count := func(name string, n int) string {
		if n > 1 {
			return fmt.Sprintf("%s (%d)", name, n)
		}
		return name
	}
	for _, name := range slices.Sorted(maps.Keys(after)) {
		if n := after[name] - before[name]; n > 0 {
			added = append(added, count(name, n))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(before)) {
		if n := before[name] - after[name]; n > 0 {
			removed = append(removed, count(name, n))
		}
	}
	return added, removed, nil
}

// partSummary states the parts and files a change adds or removes, for the
// approval.
func partSummary(added, removed []string, ups []upload) string {
	var b strings.Builder
	if len(added) > 0 {
		b.WriteString("\n\nAdds: " + strings.Join(added, ", ") + ".")
	}
	if len(removed) > 0 {
		b.WriteString("\n\nRemoves: " + strings.Join(removed, ", ") + ".")
	}
	for i, u := range ups {
		if i == 0 {
			b.WriteString("\n\nAttaches first:")
		}
		b.WriteString("\n    " + u.String())
	}
	return b.String()
}

func (s *Service) redacted(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = s.people.Text(n)
	}
	return out
}

// loadAttachments lets images refer to the files attached to the page, and
// checks that files to attach do not exist yet.
func (s *Service) loadAttachments(ctx context.Context, c *codec, md string, ups []upload) error {
	if len(ups) == 0 && !strings.Contains(md, "attachment:") {
		return nil
	}
	list, err := s.cc.Attachments(ctx, c.pageID, 250)
	if err != nil {
		return err
	}
	c.attachments = map[string]confluence.Attachment{}
	for _, a := range list {
		c.attachments[a.Title] = a
	}
	for _, u := range ups {
		if _, exists := c.attachments[u.name]; exists {
			return fmt.Errorf("the page already has an attachment named %s; refer to it as ![%s](attachment:%s) without attaching it again", u.name, u.name, u.name)
		}
		c.uploads[u.name] = true
	}
	return nil
}
