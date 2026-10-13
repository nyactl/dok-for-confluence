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
func (s *Service) lostParts(ctx context.Context, stored *confluence.Body, updated []byte) ([]string, error) {
	if stored == nil || strings.TrimSpace(stored.ADF.Value) == "" {
		return nil, nil
	}
	before, err := parts([]byte(stored.ADF.Value))
	if err != nil {
		return nil, fmt.Errorf("cannot check the stored page content: %w", err)
	}
	// What a read followed by an unchanged write would keep.
	kept := map[string]int{}
	if rt, err := s.toADF(s.markdown(ctx, stored)); err == nil {
		if kept, err = parts(rt); err != nil {
			return nil, err
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
