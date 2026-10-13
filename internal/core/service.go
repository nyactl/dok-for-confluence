// Package core implements every dok operation on top of the Confluence
// client. It is the only package that sees raw Confluence data: it enforces
// the space allowlist, replaces people with labels, converts page bodies to
// Markdown and returns plain views. Writes are returned as plans that the
// caller must confirm with the human before applying.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nyactl/approvekit/atlassian"
	"github.com/nyactl/approvekit/people"
	adf "github.com/nyactl/go-adf"

	"github.com/nyactl/dok-for-confluence/internal/confluence"
	"github.com/nyactl/dok-for-confluence/internal/macros"
)

// ErrNotFound is returned for missing pages and for pages outside the
// allowed spaces alike, so the allowlist does not leak which pages exist.
var ErrNotFound = errors.New("page not found, not visible to you, or outside the spaces dok may use")

// ErrNoSpaces is returned when the profile allows no space yet.
var ErrNoSpaces = errors.New("no spaces are allowed yet; add one with `dok config spaces add KEY`")

// Settings are the parts of the configuration the service enforces.
type Settings struct {
	Privacy people.Mode
	Spaces  []string // allowed space keys
	Parents []string // if set, writes only below these pages
	Macros  []macros.Def
	Fences  map[string]macros.Fence // fenced code that becomes a macro
}

// Service runs operations for one user.
type Service struct {
	cc      *confluence.Client
	people  *people.People
	spaces  map[string]confluence.Space // by ID
	keys    []string                    // allowed keys, in configured order
	parents []string
	// macros are the allowed macros, by name and in order, and fences the
	// fenced code languages that become one.
	macros    map[string]macros.Def
	macroList []macros.Def
	fences    map[string]macros.Fence
	now       func() time.Time

	usersMu sync.Mutex
	users   map[string]bool // account IDs looked up
}

// New verifies the credentials, resolves the allowed spaces and prepares a
// service.
func New(ctx context.Context, cc *confluence.Client, s Settings) (*Service, error) {
	me, err := cc.Myself(ctx)
	if err != nil {
		return nil, err
	}
	svc := &Service{
		cc:      cc,
		people:  people.New(s.Privacy, people.Person{AccountID: me.AccountID, DisplayName: me.DisplayName, Email: me.Email}),
		spaces:  map[string]confluence.Space{},
		keys:    s.Spaces,
		parents: s.Parents,
		now:     time.Now,
		users:   map[string]bool{me.AccountID: true},
		macros:  map[string]macros.Def{},
		fences:  map[string]macros.Fence{},
	}
	for _, d := range s.Macros {
		if d.Validate() == nil {
			svc.macros[d.Name] = d
			svc.macroList = append(svc.macroList, d)
		}
	}
	for lang, f := range s.Fences {
		if d, ok := svc.macros[f.Macro]; ok && d.CheckFence(f) == nil {
			svc.fences[lang] = f
		}
	}
	found, err := cc.SpacesByKey(ctx, s.Spaces)
	if err != nil {
		return nil, err
	}
	// Confluence matches keys regardless of case and returns each space's
	// own spelling, which dok uses from here on, also in search queries.
	svc.keys = nil
	for _, k := range s.Spaces {
		i := slices.IndexFunc(found, func(sp confluence.Space) bool { return strings.EqualFold(sp.Key, k) })
		if i >= 0 {
			k = found[i].Key
			svc.spaces[found[i].ID] = found[i]
		}
		if !slices.ContainsFunc(svc.keys, func(have string) bool { return strings.EqualFold(have, k) }) {
			svc.keys = append(svc.keys, k)
		}
	}
	return svc, nil
}

// Mode returns the privacy mode.
func (s *Service) Mode() people.Mode { return s.people.Mode() }

// Site returns the site URL.
func (s *Service) Site() string { return s.cc.Site() }

func (s *Service) spaceByKey(key string) (confluence.Space, bool) {
	for _, sp := range s.spaces {
		if strings.EqualFold(sp.Key, key) {
			return sp, true
		}
	}
	return confluence.Space{}, false
}

// learn registers an account by its current name, so the name is replaced
// in free text. Lookups are cached; a failed lookup still gets a label.
func (s *Service) learn(ctx context.Context, accountID string) {
	if accountID == "" {
		return
	}
	s.usersMu.Lock()
	seen := s.users[accountID]
	s.users[accountID] = true
	s.usersMu.Unlock()
	if seen {
		return
	}
	if u, err := s.cc.User(ctx, accountID); err == nil {
		s.people.ID(u.AccountID, u.DisplayName, u.Email)
	}
}

func (s *Service) person(ctx context.Context, accountID string) string {
	if accountID == "" {
		return ""
	}
	s.learn(ctx, accountID)
	return s.people.ID(accountID, "", "")
}

// page fetches a page and returns ErrNotFound unless it is in an allowed
// space. With body, the page's content is included.
func (s *Service) page(ctx context.Context, ref string, body bool) (*confluence.Page, error) {
	if len(s.keys) == 0 {
		return nil, ErrNoSpaces
	}
	id, err := confluence.PageID(ref)
	if err != nil {
		return nil, err
	}
	var p *confluence.Page
	if body {
		p, err = s.cc.Page(ctx, id, false)
	} else {
		p, err = s.cc.PageMeta(ctx, id)
	}
	if errors.Is(err, atlassian.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if _, ok := s.spaces[p.SpaceID]; !ok {
		return nil, ErrNotFound
	}
	if p.Status != "current" && p.Status != "draft" {
		return nil, ErrNotFound
	}
	return p, nil
}

const maxDepth = 100

// writable checks that a page may be written: with Parents configured, it
// must be one of them or below one of them.
func (s *Service) writable(ctx context.Context, p *confluence.Page) error {
	if len(s.parents) == 0 {
		return nil
	}
	cur := p
	for range maxDepth {
		if slices.Contains(s.parents, cur.ID) {
			return nil
		}
		if cur.ParentID == "" {
			break
		}
		next, err := s.cc.PageMeta(ctx, cur.ParentID)
		if err != nil {
			break
		}
		cur = next
	}
	return fmt.Errorf("page %s is not below the pages dok may write to (see `dok config show`)", p.ID)
}

// markdown renders a body with people replaced. Known names are redacted in
// each text piece before conversion, where Markdown escaping cannot split
// them, and again in the result, for names spread over several pieces.
func (s *Service) markdown(ctx context.Context, b *confluence.Body) string {
	if b == nil || strings.TrimSpace(b.ADF.Value) == "" {
		return ""
	}
	var doc adf.Node
	if err := json.Unmarshal([]byte(b.ADF.Value), &doc); err != nil {
		return "(content could not be converted)"
	}
	doc.Content = markMacros(doc.Content)
	s.learnMentions(ctx, doc)
	s.redactText(&doc)
	raw, err := json.Marshal(doc)
	if err != nil {
		return "(content could not be converted)"
	}
	md, err := adf.ToMarkdown(raw, adf.WithMentionText(s.people.Mention))
	if err != nil {
		return "(content could not be converted)"
	}
	return s.people.Text(md)
}

// learnMentions looks up mentioned people, whose stored mention text may be
// outdated, so their current names are redacted too.
func (s *Service) learnMentions(ctx context.Context, n adf.Node) {
	if n.Type == "mention" {
		if id, ok := n.Attrs["id"].(string); ok {
			s.learn(ctx, id)
		}
	}
	for _, c := range n.Content {
		s.learnMentions(ctx, c)
	}
}

func (s *Service) redactText(n *adf.Node) {
	if n.Text != "" {
		n.Text = s.people.Text(n.Text)
	}
	for _, k := range []string{"text", "title", "alt"} {
		if t, ok := n.Attrs[k].(string); ok && n.Type != "mention" {
			n.Attrs[k] = s.people.Text(t)
		}
	}
	for i := range n.Content {
		s.redactText(&n.Content[i])
	}
}

// toADF converts Markdown into ADF, resolving @[label] mentions of people
// shown in this session.
func (s *Service) toADF(md string) (json.RawMessage, error) {
	doc, err := adf.FromMarkdown(md, adf.WithMentionResolver(s.people.Resolve))
	var me *adf.MentionError
	if errors.As(err, &me) {
		return nil, fmt.Errorf("cannot mention @[%s]: only people already shown in this session can be mentioned", me.Label)
	}
	return doc, err
}

// when converts a Confluence timestamp to local "2006-01-02 15:04".
func (s *Service) when(ts string) string {
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t.In(s.now().Location()).Format("2006-01-02 15:04")
	}
	return ts
}

func (s *Service) pageURL(p *confluence.Page) string {
	if p.Links.WebUI == "" {
		return ""
	}
	return s.cc.Site() + "/wiki" + p.Links.WebUI
}
