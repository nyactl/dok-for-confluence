package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyactl/approvekit/atlassian"
	"github.com/nyactl/approvekit/people"

	"github.com/nyactl/dok-for-confluence/internal/confluence"
	"github.com/nyactl/dok-for-confluence/internal/confluencetest"
)

// fixture: space DOC (allowed) with a home page, a page by Alice that
// mentions Bob, and a child; space HR (not allowed) with a secret page.
func fixture(t *testing.T) *confluencetest.Fake {
	t.Helper()
	f := confluencetest.New(t)
	f.AddSpace(confluencetest.Space{ID: "10", Key: "DOC", Name: "Docs", HomepageID: "100"})
	f.AddSpace(confluencetest.Space{ID: "20", Key: "HR", Name: "HR", HomepageID: "200"})
	f.AddPage(confluencetest.Page{ID: "100", SpaceID: "10", Title: "Docs home", AuthorID: "acc-me"})
	f.AddPage(confluencetest.Page{ID: "101", SpaceID: "10", ParentID: "100", Title: "Runbook by Alice Smith", AuthorID: "acc-alice",
		Version: 3, VersionAuthor: "acc-bob", Body: confluencetest.Doc("Ask {acc-bob} or Alice Smith (alice@example.com).")})
	f.AddPage(confluencetest.Page{ID: "102", SpaceID: "10", ParentID: "101", Title: "Child", AuthorID: "acc-me"})
	f.AddPage(confluencetest.Page{ID: "200", SpaceID: "20", Title: "Salaries", AuthorID: "acc-alice", Body: confluencetest.Doc("secret")})
	return f
}

func service(t *testing.T, f *confluencetest.Fake, mode people.Mode, parents ...string) *Service {
	t.Helper()
	cc := confluence.New(f.URL(), "me@example.com", "tok-me", "test", atlassian.WithTransport(f.Transport()))
	s, err := New(context.Background(), cc, Settings{Privacy: mode, Spaces: []string{"DOC"}, Parents: parents})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func assertNoPersonalData(t *testing.T, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	for _, s := range []string{"Alice", "Bob", "alice@example.com", "acc-alice", "acc-bob"} {
		if strings.Contains(string(b), s) {
			t.Errorf("output contains %q: %s", s, b)
		}
	}
}

func TestPageIsRedacted(t *testing.T) {
	t.Parallel()
	s := service(t, fixture(t), people.Own)
	v, err := s.Page(context.Background(), "https://example.atlassian.net/wiki/spaces/DOC/pages/101/Runbook")
	if err != nil {
		t.Fatal(err)
	}
	assertNoPersonalData(t, v)
	if v.Author != "Person A" || v.LastEditedBy != "Person B" || v.Version != 3 || v.Space != "DOC" {
		t.Errorf("view = %+v", v)
	}
	if !strings.Contains(v.Body, "@[Person B]") || !strings.Contains(v.Body, "Person A (Person A)") || v.Title != "Runbook by Person A" {
		t.Errorf("body = %q title = %q", v.Body, v.Title)
	}
}

func TestOffModeShowsNames(t *testing.T) {
	t.Parallel()
	s := service(t, fixture(t), people.Off)
	v, err := s.Page(context.Background(), "101")
	if err != nil || v.Author != "Alice Smith" || !strings.Contains(v.Body, "@[Bob Jones]") {
		t.Errorf("view = %+v, %v", v, err)
	}
}

func TestOtherSpacesAreInvisible(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	s := service(t, f, people.Own)
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"page":     func() error { _, err := s.Page(ctx, "200"); return err },
		"children": func() error { _, err := s.Children(ctx, "200", 0); return err },
		"comments": func() error { _, err := s.Comments(ctx, "200", 0); return err },
		"comment":  func() error { _, err := s.PlanComment(ctx, "200", "hi"); return err },
		"missing":  func() error { _, err := s.Page(ctx, "999"); return err },
	} {
		if err := call(); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := s.PlanCreate(ctx, CreateInput{Space: "HR", Title: "x"}); err == nil || !strings.Contains(err.Error(), "not one of the spaces") {
		t.Errorf("create in HR: %v", err)
	}
	if got := s.Spaces(); len(got) != 1 || got[0].Key != "DOC" {
		t.Errorf("spaces = %+v", got)
	}
}

func TestSearchStaysInAllowedSpaces(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	s := service(t, f, people.Own)
	if _, err := s.Search(context.Background(), `runbook "quoted"`, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.CQL[0], `space in ("DOC")`) || !strings.Contains(f.CQL[0], `text ~ "runbook \"quoted\""`) {
		t.Errorf("CQL = %s", f.CQL[0])
	}
	// Every result is checked again against the allowed spaces.
	hits, err := s.Search(context.Background(), "a", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.Space != "DOC" || strings.Contains(h.Excerpt, "@@@") {
			t.Errorf("hit = %+v", h)
		}
	}
	assertNoPersonalData(t, hits)
}

func TestNoSpacesConfigured(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	cc := confluence.New(f.URL(), "me@example.com", "tok-me", "test", atlassian.WithTransport(f.Transport()))
	s, err := New(context.Background(), cc, Settings{Privacy: people.Own})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Page(context.Background(), "101"); !errors.Is(err, ErrNoSpaces) {
		t.Errorf("err = %v", err)
	}
}

func TestCreateIsDraftByDefault(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	s := service(t, f, people.Own)
	ctx := context.Background()
	// Only people shown in this session can be mentioned: read the page first.
	if _, err := s.Page(ctx, "101"); err != nil {
		t.Fatal(err)
	}
	p, err := s.PlanCreate(ctx, CreateInput{ParentID: "101", Title: "New design", Markdown: "## Goal\n\nThanks @[Person B]"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != Additive || !strings.Contains(p.Description, "as a draft") || !strings.Contains(p.Description, `below "Runbook by Person A" (101)`) {
		t.Errorf("description = %s", p.Description)
	}
	assertNoPersonalData(t, p.Description)
	msg, err := p.Apply(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var created *confluencetest.Page
	for _, pg := range f.Pages {
		if pg.Title == "New design" {
			created = pg
		}
	}
	if created == nil || created.Status != "draft" || created.ParentID != "101" || created.SpaceID != "10" || !strings.Contains(msg, "draft") {
		t.Fatalf("created = %+v, msg %q", created, msg)
	}
	if !strings.Contains(string(created.Body), `"type":"mention","attrs":{"id":"acc-bob"`) {
		t.Errorf("mention not resolved: %s", created.Body)
	}

	pub, err := s.PlanPublish(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pub.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if created.Status != "current" || created.Version != 1 {
		t.Errorf("after publish = %+v", created)
	}
	if _, err := s.PlanPublish(ctx, created.ID); err == nil || !strings.Contains(err.Error(), "already published") {
		t.Errorf("publish twice: %v", err)
	}
	if _, err := s.PlanCreate(ctx, CreateInput{Space: "DOC", Title: " "}); err == nil {
		t.Error("empty title accepted")
	}
	if _, err := s.PlanCreate(ctx, CreateInput{Space: "DOC", Title: "x", Markdown: "hi @[Nobody]"}); err == nil || !strings.Contains(err.Error(), "cannot mention") {
		t.Errorf("unknown mention: %v", err)
	}
}

func TestUpdateChecksVersionAndShowsDiff(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	s := service(t, f, people.Own)
	ctx := context.Background()
	v, err := s.Page(ctx, "101")
	if err != nil {
		t.Fatal(err)
	}
	newBody := v.Body + "\n\n- added step"
	if _, err := s.PlanUpdate(ctx, UpdateInput{Page: "101", Markdown: newBody, Version: 2}); err == nil || !strings.Contains(err.Error(), "changed since version 2") {
		t.Errorf("stale version: %v", err)
	}
	if _, err := s.PlanUpdate(ctx, UpdateInput{Page: "101", Markdown: newBody}); err == nil || !strings.Contains(err.Error(), "at version 3") {
		t.Errorf("missing version: %v", err)
	}
	if _, err := s.PlanUpdate(ctx, UpdateInput{Page: "101", Markdown: v.Body, Version: 3}); err == nil || !strings.Contains(err.Error(), "nothing would change") {
		t.Errorf("no change: %v", err)
	}
	p, err := s.PlanUpdate(ctx, UpdateInput{Page: "101", Markdown: newBody, Version: 3, Title: "Runbook v2"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != Destructive || !strings.Contains(p.Description, "from version 3 to 4") || !strings.Contains(p.Description, "+ - added step") ||
		!strings.Contains(p.Description, `to:   "Runbook v2"`) {
		t.Errorf("description:\n%s", p.Description)
	}
	assertNoPersonalData(t, p.Description)
	if _, err := p.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if pg := f.Pages["101"]; pg.Version != 4 || pg.Title != "Runbook v2" || !strings.Contains(string(pg.Body), "added step") {
		t.Errorf("page = %+v", pg)
	}
}

func TestParentsLimitWrites(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	s := service(t, f, people.Own, "101")
	ctx := context.Background()
	if _, err := s.PlanComment(ctx, "102", "below 101"); err != nil {
		t.Errorf("child of an allowed parent: %v", err)
	}
	if _, err := s.PlanComment(ctx, "101", "the parent itself"); err != nil {
		t.Errorf("allowed parent: %v", err)
	}
	if _, err := s.PlanComment(ctx, "100", "above"); err == nil || !strings.Contains(err.Error(), "not below") {
		t.Errorf("above the parent: %v", err)
	}
	if _, err := s.PlanCreate(ctx, CreateInput{Space: "DOC", Title: "top level"}); err == nil || !strings.Contains(err.Error(), "name a parent") {
		t.Errorf("top-level create: %v", err)
	}
	if _, err := s.Page(ctx, "100"); err != nil {
		t.Errorf("reading outside the write trees must work: %v", err)
	}
}

func TestCommentsAndAttachments(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	f.Comments = append(f.Comments, &confluencetest.Comment{ID: "c1", PageID: "101", AuthorID: "acc-alice", Body: confluencetest.Doc("ping {acc-me}")})
	s := service(t, f, people.Own)
	ctx := context.Background()
	cs, err := s.Comments(ctx, "101", 0)
	if err != nil || len(cs) != 1 || cs[0].Author != "Person A" || cs[0].Body != "ping @[Me]" {
		t.Fatalf("comments = %+v, %v", cs, err)
	}
	p, err := s.PlanComment(ctx, "101", "Done, @[Person A]")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if last := f.Comments[len(f.Comments)-1]; !strings.Contains(string(last.Body), `"id":"acc-alice"`) {
		t.Errorf("comment = %s", last.Body)
	}

	path := filepath.Join(t.TempDir(), "diagram.svg")
	if err := os.WriteFile(path, []byte("<svg/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err = s.PlanAttach(ctx, "101", path)
	if err != nil || !strings.Contains(p.Description, "Attach diagram.svg (6 bytes") {
		t.Fatalf("attach plan = %+v, %v", p, err)
	}
	if _, err := p.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	as, err := s.Attachments(ctx, "101", 0)
	if err != nil || len(as) != 1 || as[0].Title != "diagram.svg" || as[0].Author != "Me" {
		t.Errorf("attachments = %+v, %v", as, err)
	}
}

func TestLineDiff(t *testing.T) {
	t.Parallel()
	got := lineDiff("a\nb\nc\nd\ne\nf\ng\ni\nj", "a\nb\nC\nd\ne\nf\ng\ni\nj\nh")
	want := "  a\n  b\n- c\n+ C\n  d\n  e\n  ...\n  i\n  j\n+ h"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := lineDiff("", "new"); got != "+ new" {
		t.Errorf("from empty: %q", got)
	}
}
