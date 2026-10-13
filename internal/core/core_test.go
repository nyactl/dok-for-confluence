package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nyactl/approvekit/atlassian"
	"github.com/nyactl/approvekit/people"
	adf "github.com/nyactl/go-adf"

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

func TestSpaceKeysMatchRegardlessOfCase(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	f.AddSpace(confluencetest.Space{ID: "30", Key: "MoSys", Name: "Mixed", HomepageID: "300"})
	f.AddPage(confluencetest.Page{ID: "300", SpaceID: "30", Title: "Mixed home", AuthorID: "acc-me"})
	cc := confluence.New(f.URL(), "me@example.com", "tok-me", "test", atlassian.WithTransport(f.Transport()))
	s, err := New(context.Background(), cc, Settings{Privacy: people.Own, Spaces: []string{"doc", "MOSYS", "Doc", "GONE"}})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, sp := range s.Spaces() {
		keys = append(keys, sp.Key)
	}
	if strings.Join(keys, ",") != "DOC,MoSys" {
		t.Errorf("spaces = %v", keys)
	}
	if strings.Join(s.keys, ",") != "DOC,MoSys,GONE" {
		t.Errorf("keys = %v", s.keys)
	}
	if _, err := s.Page(context.Background(), "300"); err != nil {
		t.Errorf("page in MoSys: %v", err)
	}
	if _, err := s.Search(context.Background(), "home", 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.CQL[0], `space in ("DOC","MoSys","GONE")`) {
		t.Errorf("CQL = %s", f.CQL[0])
	}
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

// macroPage holds parts that Markdown cannot represent, as the Confluence
// editor stores them.
const macroPage = `{"type":"doc","version":1,"content":[
{"type":"paragraph","content":[{"type":"text","text":"Intro "},
 {"type":"inlineExtension","attrs":{"extensionType":"com.atlassian.confluence.macro.core","extensionKey":"anchor","parameters":{"macroParams":{"":{"value":"top"}},"macroMetadata":{"title":"Anchor"}}}}]},
{"type":"extension","attrs":{"layout":"default","extensionType":"com.atlassian.ecosystem","extensionKey":"app-1/env-1/static/diagram","text":"Diagram",
 "parameters":{"guestParams":{"source":{"type":"text","text":"graph TD; A-->B"},"nested":{"deeper":{"deepest":[1,2,3]}}},"extensionTitle":"Diagram"}}},
{"type":"bodiedExtension","attrs":{"extensionType":"com.atlassian.confluence.macro.core","extensionKey":"details","parameters":{"macroMetadata":{"title":"Page Properties"}}},
 "content":[{"type":"paragraph","content":[{"type":"text","text":"Owner: team"}]}]},
{"type":"mediaSingle","attrs":{"layout":"center"},"content":[{"type":"media","attrs":{"id":"m-1","type":"file","collection":"c"}}]}]}`

func TestUpdateKeepsMacros(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	pg := f.AddPage(confluencetest.Page{ID: "110", SpaceID: "10", ParentID: "100", Title: "Macros", AuthorID: "acc-me", Body: json.RawMessage(macroPage)})
	s := service(t, f, people.Own)
	ctx := context.Background()

	v, err := s.Page(ctx, "110")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`Intro (inline macro "Anchor" not shown)`, `> (macro "Diagram" not shown)`, `> (macro "Page Properties" not shown)`, "Owner: team"} {
		if !strings.Contains(v.Body, want) {
			t.Errorf("body lacks %q:\n%s", want, v.Body)
		}
	}

	_, err = s.PlanUpdate(ctx, UpdateInput{Page: "110", Version: v.Version, Markdown: strings.Replace(v.Body, "Intro", "Introduction", 1)})
	if err == nil {
		t.Fatal("update that deletes macros was accepted")
	}
	for _, want := range []string{`inline macro "Anchor"`, `macro "Diagram"`, `macro "Page Properties"`, "image or file", "Change the page in Confluence"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}

	// A title change alone keeps the stored body as it is.
	plan, err := s.PlanUpdate(ctx, UpdateInput{Page: "110", Version: v.Version, Title: "Macros renamed", Markdown: v.Body})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	var want, got any
	_ = json.Unmarshal([]byte(macroPage), &want)
	_ = json.Unmarshal(pg.Body, &got)
	if pg.Title != "Macros renamed" || !reflect.DeepEqual(want, got) {
		t.Errorf("title-only update changed the body: %s", pg.Body)
	}
}

func TestMacroTitle(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		attrs map[string]any
		want  string
	}{
		{map[string]any{"extensionKey": "toc", "parameters": map[string]any{"macroMetadata": map[string]any{"title": "Table of Contents"}}}, "Table of Contents"},
		{map[string]any{"extensionKey": "native-embed:whiteboard", "parameters": map[string]any{"macroMetadata": map[string]any{"title": "native-embed:whiteboard"}}}, "embedded whiteboard"},
		{map[string]any{"extensionKey": "app/env/static/diagram", "text": "Diagram", "parameters": map[string]any{"extensionTitle": "Mermaid"}}, "Mermaid"},
		{map[string]any{"extensionKey": "app/env/static/diagram"}, "diagram"},
		{nil, ""},
	} {
		if got := macroTitle(adf.Node{Type: "extension", Attrs: c.attrs}); got != c.want {
			t.Errorf("macroTitle(%v) = %q, want %q", c.attrs, got, c.want)
		}
	}
}

func TestUpdateWithoutMacrosIsUnchanged(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	s := service(t, f, people.Own)
	ctx := context.Background()
	v, err := s.Page(ctx, "101")
	if err != nil {
		t.Fatal(err)
	}
	md := v.Body + "\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n- one\n- two\n\n```go\nx\n```\n\n> quote, **bold**, [link](https://example.com)"
	if _, err := s.PlanUpdate(ctx, UpdateInput{Page: "101", Version: v.Version, Markdown: md}); err != nil {
		t.Fatal(err)
	}
}
