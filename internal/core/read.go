package core

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"strings"

	"github.com/nyactl/dok-for-confluence/internal/confluence"
)

// Views contain only text that is safe to show: people are labels, rich
// text is Markdown, and no account IDs or emails appear.

type SpaceView struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	ID   string `json:"id"`
}

type PageView struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Space        string `json:"space"`
	Status       string `json:"status"`
	Version      int    `json:"version"`
	Author       string `json:"author,omitempty"`
	LastEditedBy string `json:"lastEditedBy,omitempty"`
	Updated      string `json:"updated,omitempty"`
	ParentID     string `json:"parentId,omitempty"`
	URL          string `json:"url,omitempty"`
	Body         string `json:"body"`
}

type ChildView struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type SearchHit struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Space   string `json:"space"`
	Excerpt string `json:"excerpt,omitempty"`
	Updated string `json:"updated,omitempty"`
}

type CommentView struct {
	ID      string `json:"id"`
	Author  string `json:"author"`
	Created string `json:"created"`
	Body    string `json:"body"`
}

type AttachmentView struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	MediaType string `json:"mediaType"`
	Size      int64  `json:"size"`
	Author    string `json:"author"`
}

type Me struct {
	DisplayName string   `json:"displayName"`
	Site        string   `json:"site"`
	Privacy     string   `json:"privacy"`
	Spaces      []string `json:"spaces"`
	Parents     []string `json:"parents,omitempty"`
}

// Whoami describes the authenticated user and the settings in force.
func (s *Service) Whoami(ctx context.Context) (*Me, error) {
	u, err := s.cc.Myself(ctx)
	if err != nil {
		return nil, err
	}
	return &Me{DisplayName: u.DisplayName, Site: s.cc.Site(), Privacy: string(s.people.Mode()), Spaces: s.keys, Parents: s.parents}, nil
}

// Spaces lists the allowed spaces the user can see, in configured order.
// Allowed keys the user cannot see are left out.
func (s *Service) Spaces() []SpaceView {
	var out []SpaceView
	for _, k := range s.keys {
		if sp, ok := s.spaceByKey(k); ok {
			out = append(out, SpaceView{Key: sp.Key, Name: s.people.Text(sp.Name), ID: sp.ID})
		}
	}
	return out
}

// Page returns a page as Markdown. ref is a page ID or URL.
func (s *Service) Page(ctx context.Context, ref string) (*PageView, error) {
	p, err := s.page(ctx, ref, true)
	if err != nil {
		return nil, err
	}
	return s.pageView(ctx, p), nil
}

func (s *Service) pageView(ctx context.Context, p *confluence.Page) *PageView {
	// People are learned first, so their names are redacted in the title.
	v := &PageView{ID: p.ID, Space: s.spaces[p.SpaceID].Key, Status: p.Status, Author: s.person(ctx, p.AuthorID), ParentID: p.ParentID, URL: s.pageURL(p)}
	if p.Version != nil {
		v.Version = p.Version.Number
		v.LastEditedBy = s.person(ctx, p.Version.AuthorID)
		v.Updated = s.when(p.Version.CreatedAt)
	}
	v.Body = s.pageMarkdown(ctx, p)
	v.Title = s.people.Text(p.Title)
	return v
}

// Children lists the pages directly below a page.
func (s *Service) Children(ctx context.Context, ref string, max int) ([]ChildView, error) {
	p, err := s.page(ctx, ref, false)
	if err != nil {
		return nil, err
	}
	cs, err := s.cc.Children(ctx, p.ID, limit(max))
	if err != nil {
		return nil, err
	}
	out := []ChildView{}
	for _, c := range cs {
		if _, ok := s.spaces[c.SpaceID]; ok && c.Status == "current" {
			out = append(out, ChildView{ID: c.ID, Title: s.people.Text(c.Title)})
		}
	}
	return out, nil
}

var highlightRe = regexp.MustCompile(`@@@(end)?hl@@@`)

// Search finds pages in the allowed spaces whose text contains text, most
// recently changed first.
func (s *Service) Search(ctx context.Context, text string, max int) ([]SearchHit, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("search text is empty")
	}
	return s.search(ctx, "text ~ "+cqlString(text), max)
}

// RecentPages lists the pages of the allowed spaces, most recently changed
// first.
func (s *Service) RecentPages(ctx context.Context, max int) ([]SearchHit, error) {
	return s.search(ctx, "", max)
}

// search finds pages in the allowed spaces that match the CQL condition,
// which may be empty.
func (s *Service) search(ctx context.Context, condition string, max int) ([]SearchHit, error) {
	if len(s.keys) == 0 {
		return nil, ErrNoSpaces
	}
	quoted := make([]string, len(s.keys))
	for i, k := range s.keys {
		quoted[i] = cqlString(k)
	}
	if condition != "" {
		condition = " AND " + condition
	}
	cql := fmt.Sprintf("type = page AND space in (%s)%s ORDER BY lastmodified DESC", strings.Join(quoted, ","), condition)
	rs, err := s.cc.Search(ctx, cql, limit(max))
	if err != nil {
		return nil, err
	}
	for _, r := range rs {
		for _, u := range []confluence.User{r.Content.History.CreatedBy, r.Content.History.LastUpdated.By} {
			if u.AccountID != "" {
				s.people.ID(u.AccountID, u.DisplayName, u.Email)
			}
		}
	}
	out := []SearchHit{}
	for _, r := range rs {
		// The CQL already restricts the spaces; results are checked again,
		// because a query string alone cannot be trusted to keep it.
		if _, ok := s.spaceByKey(r.Content.Space.Key); !ok || r.Content.ID == "" || r.Content.Type != "page" {
			continue
		}
		out = append(out, SearchHit{
			ID: r.Content.ID, Title: s.people.Text(r.Content.Title), Space: r.Content.Space.Key,
			Excerpt: s.people.Text(highlightRe.ReplaceAllString(html.UnescapeString(r.Excerpt), "")), Updated: s.when(r.LastModified),
		})
	}
	return out, nil
}

// cqlString quotes s as a CQL string literal.
func cqlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// Comments returns the footer comments of a page, oldest first.
func (s *Service) Comments(ctx context.Context, ref string, max int) ([]CommentView, error) {
	p, err := s.page(ctx, ref, false)
	if err != nil {
		return nil, err
	}
	cs, err := s.cc.FooterComments(ctx, p.ID, limit(max))
	if err != nil {
		return nil, err
	}
	out := make([]CommentView, 0, len(cs))
	for _, c := range cs {
		v := CommentView{ID: c.ID, Body: s.markdown(ctx, c.Body)}
		if c.Version != nil {
			v.Author, v.Created = s.person(ctx, c.Version.AuthorID), s.when(c.Version.CreatedAt)
		}
		out = append(out, v)
	}
	return out, nil
}

// Attachments lists the files attached to a page.
func (s *Service) Attachments(ctx context.Context, ref string, max int) ([]AttachmentView, error) {
	p, err := s.page(ctx, ref, false)
	if err != nil {
		return nil, err
	}
	as, err := s.cc.Attachments(ctx, p.ID, limit(max))
	if err != nil {
		return nil, err
	}
	out := make([]AttachmentView, 0, len(as))
	for _, a := range as {
		v := AttachmentView{ID: a.ID, Title: s.people.Text(a.Title), MediaType: a.MediaType, Size: a.FileSize}
		if a.Version != nil {
			v.Author = s.person(ctx, a.Version.AuthorID)
		}
		out = append(out, v)
	}
	return out, nil
}

func limit(max int) int {
	if max <= 0 {
		return 50
	}
	return min(max, 250)
}
