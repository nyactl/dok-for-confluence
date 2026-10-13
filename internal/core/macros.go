package core

import (
	"context"
	"fmt"
	"strings"

	adf "github.com/nyactl/go-adf"

	"github.com/nyactl/dok-for-confluence/internal/macros"
)

// MacroView describes an allowed macro and how to write it.
type MacroView struct {
	Name    string      `json:"name"`
	Title   string      `json:"title"`
	Params  []ParamView `json:"params"`
	Fence   string      `json:"fence,omitempty"` // fenced code language that becomes this macro
	Example string      `json:"example"`
}

type ParamView struct {
	Name   string   `json:"name"`
	Kind   string   `json:"kind"`
	Values []string `json:"values,omitempty"`
}

// Macros lists the macros pages may contain as macro blocks.
func (s *Service) Macros() []MacroView {
	var out []MacroView
	for _, d := range s.macroList {
		v := MacroView{Name: d.Name, Title: s.people.Text(d.Title)}
		var example []string
		for _, p := range d.Params {
			v.Params = append(v.Params, ParamView{Name: p.Name, Kind: p.Kind, Values: p.Values})
			if len(example) < 2 {
				example = append(example, p.Name+": "+exampleValue(s, p))
			}
		}
		v.Example = "```macro:" + d.Name + "\n" + strings.Join(example, "\n")
		if len(example) > 0 {
			v.Example += "\n"
		}
		v.Example += "```"
		for lang, f := range s.fences {
			if f.Macro == d.Name {
				v.Fence = lang
				v.Example = "```" + lang + "\n(the " + f.Param + ")\n```"
			}
		}
		out = append(out, v)
	}
	return out
}

func exampleValue(s *Service, p macros.Param) string {
	switch p.Kind {
	case macros.Number:
		return "2"
	case macros.Boolean:
		return "true"
	case macros.Enum:
		return p.Values[0]
	case macros.URL:
		if len(s.keys) > 0 {
			return s.cc.Site() + "/wiki/spaces/" + s.keys[0] + "/whiteboard/123"
		}
	}
	return "text"
}

// LearnMacros proposes definitions for the macros on a page, one per kind,
// named so they do not clash with taken names. Macros dok never creates
// and macros with a body are reported in skipped.
func (s *Service) LearnMacros(ctx context.Context, ref string, taken map[string]bool) (defs []macros.Def, skipped []string, err error) {
	p, err := s.page(ctx, ref, true)
	if err != nil {
		return nil, nil, err
	}
	if p.Body == nil {
		return nil, nil, nil
	}
	doc, err := decode(p.Body.ADF.Value)
	if err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	var walk func(n adf.Node)
	walk = func(n adf.Node) {
		if _, isMacro := macroTypes[n.Type]; isMacro {
			key := strAttr(n.Attrs, "extensionType") + "/" + strAttr(n.Attrs, "extensionKey")
			if seen[key] {
				return
			}
			seen[key] = true
			name := macros.Slug(macros.Title(n))
			for i := 2; taken[name]; i++ {
				name = fmt.Sprintf("%s-%d", strings.TrimRight(name[:min(len(name), 37)], "-"), i)
			}
			d, err := macros.Learn(n, name)
			if err != nil {
				skipped = append(skipped, fmt.Sprintf("%s: %v", s.people.Text(macros.Title(n)), err))
				return
			}
			d.Title = s.people.Text(d.Title)
			taken[name] = true
			defs = append(defs, d)
			return
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(doc)
	return defs, skipped, nil
}
