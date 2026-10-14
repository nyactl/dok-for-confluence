// Package render turns core views into Markdown or JSON. Every output goes
// through sanitize, so text from Confluence cannot control the terminal.
package render

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nyactl/approvekit/sanitize"

	"github.com/nyactl/dok-for-confluence/internal/core"
)

func JSON(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return sanitize.String(string(b)), nil
}

func done(b *strings.Builder) string {
	return sanitize.String(strings.TrimRight(b.String(), "\n") + "\n")
}

func line(b *strings.Builder, format string, args ...any) {
	fmt.Fprintf(b, format+"\n", args...)
}

func Me(m *core.Me) string {
	var b strings.Builder
	line(&b, "- Name: %s", m.DisplayName)
	line(&b, "- Site: %s", m.Site)
	line(&b, "- Privacy: %s", m.Privacy)
	line(&b, "- Spaces: %s", orDash(strings.Join(m.Spaces, ", ")))
	if len(m.Parents) > 0 {
		line(&b, "- Writes only below pages: %s", strings.Join(m.Parents, ", "))
	}
	return done(&b)
}

func Spaces(ss []core.SpaceView) string {
	if len(ss) == 0 {
		return "No spaces are allowed yet; add one with `dok config spaces add KEY`.\n"
	}
	var b strings.Builder
	for _, s := range ss {
		line(&b, "- %s: %s", s.Key, s.Name)
	}
	return done(&b)
}

func Page(p *core.PageView) string {
	var b strings.Builder
	line(&b, "# %s", p.Title)
	line(&b, "")
	line(&b, "- Page: %s, space %s", p.ID, p.Space)
	line(&b, "- Status: %s, version %d", p.Status, p.Version)
	if p.Author != "" {
		line(&b, "- Created by: %s", p.Author)
	}
	if p.LastEditedBy != "" {
		line(&b, "- Last changed by: %s, %s", p.LastEditedBy, p.Updated)
	}
	if p.ParentID != "" {
		line(&b, "- Parent page: %s", p.ParentID)
	}
	if p.URL != "" {
		line(&b, "- URL: %s", p.URL)
	}
	if p.Body != "" {
		line(&b, "\n---\n\n%s", p.Body)
	}
	return done(&b)
}

func Children(cs []core.ChildView) string {
	if len(cs) == 0 {
		return "No child pages.\n"
	}
	var b strings.Builder
	for _, c := range cs {
		line(&b, "- %s: %s", c.ID, c.Title)
	}
	return done(&b)
}

func Search(hs []core.SearchHit) string {
	if len(hs) == 0 {
		return "No matching pages.\n"
	}
	var b strings.Builder
	for _, h := range hs {
		line(&b, "- **%s** %s (space %s, updated %s)", h.ID, h.Title, h.Space, orDash(h.Updated))
		if e := strings.Join(strings.Fields(h.Excerpt), " "); e != "" {
			line(&b, "  %s", e)
		}
	}
	return done(&b)
}

func Comments(cs []core.CommentView) string {
	if len(cs) == 0 {
		return "No comments.\n"
	}
	var b strings.Builder
	for _, c := range cs {
		line(&b, "### %s, %s (comment %s)\n\n%s\n", c.Author, c.Created, c.ID, c.Body)
	}
	return done(&b)
}

func Attachments(as []core.AttachmentView) string {
	if len(as) == 0 {
		return "No attachments.\n"
	}
	var b strings.Builder
	for _, a := range as {
		line(&b, "- %s: %s (%s, %d bytes) by %s", a.ID, a.Title, orDash(a.MediaType), a.Size, orDash(a.Author))
	}
	return done(&b)
}

// Plan renders a write for confirmation.
func Plan(p *core.Plan) string {
	return sanitize.String(p.Description)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// Macros lists the allowed macros.
func Macros(ms []core.MacroView) string {
	if len(ms) == 0 {
		return "No macros are allowed; the user can allow some with `dok config macros`.\n"
	}
	var b strings.Builder
	for _, m := range ms {
		line(&b, "## %s (%s)", m.Name, m.Title)
		line(&b, "")
		if len(m.Params) == 0 {
			line(&b, "No parameters.")
		}
		for _, p := range m.Params {
			kind := p.Kind
			if len(p.Values) > 0 {
				kind += ": " + strings.Join(p.Values, ", ")
			}
			line(&b, "- %s (%s)", p.Name, kind)
		}
		if m.Fence != "" {
			line(&b, "")
			line(&b, "Fenced %s code becomes this macro.", m.Fence)
		}
		line(&b, "")
		line(&b, "%s", m.Example)
		line(&b, "")
	}
	return b.String()
}
