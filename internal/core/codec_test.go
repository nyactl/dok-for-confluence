package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/nyactl/approvekit/atlassian"
	"github.com/nyactl/approvekit/people"

	"github.com/nyactl/dok-for-confluence/internal/confluence"
	"github.com/nyactl/dok-for-confluence/internal/confluencetest"
	"github.com/nyactl/dok-for-confluence/internal/macros"
)

func serviceWith(t *testing.T, f *confluencetest.Fake, s Settings) *Service {
	t.Helper()
	cc := confluence.New(f.URL(), "me@example.com", "tok-me", "test", atlassian.WithTransport(f.Transport()))
	if s.Privacy == "" {
		s.Privacy = people.Own
	}
	if s.Spaces == nil {
		s.Spaces = []string{"DOC"}
	}
	svc, err := New(context.Background(), cc, s)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// nodesOf returns the top-level nodes of a body as JSON, for comparison.
func nodesOf(t *testing.T, raw []byte) []string {
	t.Helper()
	n, err := decode(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range n.Content {
		b, _ := json.Marshal(c)
		out = append(out, string(b))
	}
	return out
}

var placeholderRe = regexp.MustCompile(`\{"type":"kept","attrs":\{"id":"(k[0-9]+-[0-9a-f]{6})","what":"([^"\\]|\\.)*"\}\}`)

func TestPlaceholdersKeepParts(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	pg := f.AddPage(confluencetest.Page{ID: "110", SpaceID: "10", ParentID: "100", Title: "Macros", AuthorID: "acc-me", Body: json.RawMessage(macroPage)})
	stored := nodesOf(t, pg.Body)
	s := service(t, f, people.Own)
	ctx := context.Background()

	v, err := s.Page(ctx, "110")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Intro `adf:{\"type\":\"kept\"", `"what":"inline macro \"Anchor\""`, `"what":"macro \"Diagram\""`, `"what":"macro \"Page Properties\""`, `"what":"image or file"`} {
		if !strings.Contains(v.Body, want) {
			t.Errorf("body lacks %s:\n%s", want, v.Body)
		}
	}
	// The placeholders hold no content of the parts.
	for _, bad := range []string{"graph TD", "Owner: team", "app-1", "m-1"} {
		if strings.Contains(v.Body, bad) {
			t.Errorf("body shows %q:\n%s", bad, v.Body)
		}
	}

	plan, err := s.PlanUpdate(ctx, UpdateInput{Page: "110", Version: v.Version, Markdown: strings.Replace(v.Body, "Intro", "Introduction", 1)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan.Description, "Removes") || strings.Contains(plan.Description, "Adds") {
		t.Errorf("plan: %s", plan.Description)
	}
	if _, err := plan.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	after := nodesOf(t, pg.Body)
	if !strings.Contains(after[0], "Introduction") || !reflect.DeepEqual(after[1:], stored[1:]) {
		t.Errorf("parts changed\nbefore: %v\nafter:  %v", stored, after)
	}
	if !strings.Contains(after[0], `"extensionKey":"anchor"`) {
		t.Errorf("inline macro lost: %s", after[0])
	}

	// Deleting a placeholder deletes the part, and the approval says so.
	v, _ = s.Page(ctx, "110")
	without := regexp.MustCompile("```adf\n[^\n]*Diagram[^\n]*\n```").ReplaceAllString(v.Body, "")
	plan, err = s.PlanUpdate(ctx, UpdateInput{Page: "110", Version: v.Version, Markdown: without})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Description, `Removes: macro "Diagram".`) {
		t.Errorf("plan does not name the removed macro:\n%s", plan.Description)
	}
}

func TestPlaceholdersRefuseWhatTheyDidNotWrite(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	f.AddPage(confluencetest.Page{ID: "110", SpaceID: "10", ParentID: "100", Title: "Macros", AuthorID: "acc-me", Body: json.RawMessage(macroPage)})
	s := service(t, f, people.Own)
	ctx := context.Background()
	v, _ := s.Page(ctx, "110")
	diagram := regexp.MustCompile("```adf\n[^\n]*Diagram[^\n]*\n```").FindString(v.Body)
	id := placeholderRe.FindStringSubmatch(diagram)[1]
	for name, md := range map[string]string{
		"authored macro":  v.Body + "\n\n```adf\n{\"type\":\"extension\",\"attrs\":{\"extensionType\":\"t\",\"extensionKey\":\"html\"}}\n```",
		"authored inline": v.Body + "\n\nx `adf:{\"type\":\"status\",\"attrs\":{\"text\":\"DONE\",\"color\":\"green\"}}`",
		"duplicate":       v.Body + "\n\n" + diagram,
		"foreign id":      strings.Replace(v.Body, id, id[:len(id)-1]+"0", 1),
		"moved inline":    v.Body + "\n\nx `adf:" + placeholderRe.FindString(diagram) + "`",
	} {
		if strings.Contains(name, "foreign") && strings.HasSuffix(id, "0") {
			md = strings.Replace(v.Body, id, id[:len(id)-1]+"1", 1)
		}
		_, err := s.PlanUpdate(ctx, UpdateInput{Page: "110", Version: v.Version, Markdown: md})
		if err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// Placeholders do not work on new pages either.
	if _, err := s.PlanCreate(ctx, CreateInput{Space: "DOC", Title: "copy", Markdown: diagram}); err == nil {
		t.Error("a placeholder copied into a new page was accepted")
	}
}

func TestPlaceholdersHidePeople(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	body := `{"type":"doc","version":1,"content":[
{"type":"panel","attrs":{"panelType":"info"},"content":[{"type":"paragraph","content":[{"type":"text","text":"Ask "},{"type":"mention","attrs":{"id":"acc-bob","text":"@Bob Jones"}}]}]},
{"type":"extension","attrs":{"extensionType":"com.atlassian.ecosystem","extensionKey":"app/env/static/x","text":"Report by Alice Smith","parameters":{"extensionTitle":"Report by Alice Smith","guestParams":{"owner":"acc-alice"},"embeddedMacroContext":{"accountId":"acc-alice"}}}},
{"type":"paragraph","content":[{"type":"status","attrs":{"text":"Alice","color":"green"}}]}]}`
	f.AddPage(confluencetest.Page{ID: "113", SpaceID: "10", ParentID: "100", Title: "People", AuthorID: "acc-alice", Body: json.RawMessage(body)})
	s := service(t, f, people.Own)
	v, err := s.Page(context.Background(), "113")
	if err != nil {
		t.Fatal(err)
	}
	assertNoPersonalData(t, v)
	if !strings.Contains(v.Body, `"what":"macro \"Report by Person A\""`) || !strings.Contains(v.Body, `"what":"panel"`) {
		t.Errorf("body:\n%s", v.Body)
	}
}

func tocSettings() Settings {
	return Settings{Macros: []macros.Def{macros.Builtin()["toc"], macros.Builtin()["whiteboard"]}}
}

const storedTOC = `{"type":"extension","attrs":{"layout":"default","extensionType":"com.atlassian.confluence.macro.core","extensionKey":"toc","parameters":{"macroParams":{"style":{"value":"none"}},"macroMetadata":{"macroId":{"value":"a685959c"},"schemaVersion":{"value":"1"},"title":"Table of Contents"}},"localId":"e0d05a52"}}`

func TestAllowedMacroBlocks(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	pg := f.AddPage(confluencetest.Page{ID: "114", SpaceID: "10", ParentID: "100", Title: "TOC", AuthorID: "acc-me",
		Body: json.RawMessage(`{"type":"doc","version":1,"content":[` + storedTOC + `,{"type":"codeBlock","attrs":{"language":"macro:toc"},"content":[{"type":"text","text":"maxLevel: 9"}]},{"type":"paragraph","content":[{"type":"text","text":"text"}]}]}`)})
	s := serviceWith(t, f, tocSettings())
	ctx := context.Background()
	v, err := s.Page(ctx, "114")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(v.Body, "```macro:toc\nstyle: none\n```\n\n```adf\n{\"type\":\"kept\"") {
		t.Fatalf("body:\n%s", v.Body)
	}
	// Unchanged, the stored macro is kept with its identifiers.
	plan, err := s.PlanUpdate(ctx, UpdateInput{Page: "114", Version: v.Version, Markdown: v.Body + "\n\nmore"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if got := nodesOf(t, pg.Body); got[0] != nodesOf(t, []byte(`{"type":"doc","content":[`+storedTOC+`]}`))[0] || !strings.Contains(got[1], `"language":"macro:toc"`) {
		t.Errorf("stored macro or lookalike code changed: %v", got)
	}
	// A changed block makes a new macro, named in the approval.
	v, _ = s.Page(ctx, "114")
	plan, err = s.PlanUpdate(ctx, UpdateInput{Page: "114", Version: v.Version, Markdown: strings.Replace(v.Body, "style: none", "maxLevel: 2", 1) + "\n\n```macro:toc\n```"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Description, `Adds: macro "Table of Contents".`) {
		t.Errorf("plan:\n%s", plan.Description)
	}
	if _, err := plan.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	body := string(pg.Body)
	if strings.Contains(body, `"localId":"e0d05a52"`) || !strings.Contains(body, `"maxLevel":{"value":"2"}`) || strings.Count(body, `"extensionKey":"toc"`) != 2 {
		t.Errorf("body: %s", body)
	}
	v, _ = s.Page(ctx, "114")
	for _, c := range []struct{ md, want string }{
		{"```macro:children\n```", "not allowed"},
		{"```macro:toc\nmaxLevel: lots\n```", "takes a number"},
		{"```macro:toc\ncolour: red\n```", "has no parameter"},
		{"```macro:whiteboard\nurl: https://elsewhere.example/wiki/spaces/DOC/whiteboard/1\n```", "allowed spaces"},
		{"```macro:whiteboard\nurl: " + f.URL() + "/wiki/spaces/HR/whiteboard/1\n```", "allowed spaces"},
	} {
		if _, err := s.PlanUpdate(ctx, UpdateInput{Page: "114", Version: v.Version, Markdown: v.Body + "\n\n" + c.md}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: err = %v, want %q", c.md, err, c.want)
		}
	}
	plan, err = s.PlanCreate(ctx, CreateInput{Space: "DOC", Title: "Board", Markdown: "```macro:whiteboard\nurl: " + f.URL() + "/wiki/spaces/DOC/whiteboard/42\n```"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Description, `Adds: macro "embedded whiteboard".`) {
		t.Errorf("plan:\n%s", plan.Description)
	}
}

const forgeMermaid = `{"type":"extension","attrs":{"layout":"extension","extensionType":"com.atlassian.ecosystem","extensionKey":"app-1/env-1/static/mermaid","text":"Mermaid Diagram",
	"parameters":{"layout":"extension","extensionId":"ari:cloud:ecosystem::extension/app-1/env-1/static/mermaid","extensionTitle":"Mermaid Diagram","forgeEnvironment":"PRODUCTION","guestParams":{"source":{"type":"text","text":"graph TD; A-->B"},"theme":"dark"}}}}`

func TestFencedCodeAsMacro(t *testing.T) {
	t.Parallel()
	n, err := decode(forgeMermaid)
	if err != nil {
		t.Fatal(err)
	}
	d, err := macros.Learn(n, "mermaid-diagram")
	if err != nil {
		t.Fatal(err)
	}
	f := fixture(t)
	pg := f.AddPage(confluencetest.Page{ID: "115", SpaceID: "10", ParentID: "100", Title: "Diagram", AuthorID: "acc-me",
		Body: json.RawMessage(`{"type":"doc","version":1,"content":[` + forgeMermaid + `,{"type":"codeBlock","attrs":{"language":"mermaid"},"content":[{"type":"text","text":"graph LR; plain-->code"}]}]}`)})
	s := serviceWith(t, f, Settings{Macros: []macros.Def{d}, Fences: map[string]macros.Fence{"mermaid": {Macro: d.Name, Param: "source.text"}}})
	ctx := context.Background()
	v, err := s.Page(ctx, "115")
	if err != nil {
		t.Fatal(err)
	}
	if v.Body != "```mermaid\ngraph TD; A-->B\n```\n\n```mermaid\ngraph LR; plain-->code\n```" {
		t.Fatalf("body:\n%s", v.Body)
	}
	plan, err := s.PlanUpdate(ctx, UpdateInput{Page: "115", Version: v.Version, Markdown: v.Body + "\n\n```mermaid\nsequenceDiagram\n  A->>B: hi\n```"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Description, `Adds: macro "Mermaid Diagram" (2).`) {
		t.Errorf("plan:\n%s", plan.Description)
	}
	if _, err := plan.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	got := nodesOf(t, pg.Body)
	if len(got) != 3 || !strings.Contains(got[2], `"text":"sequenceDiagram\n  A-\u003e\u003eB: hi"`) || !strings.Contains(got[2], `"theme":"dark"`) || strings.Contains(strings.Join(got, ""), `"codeBlock"`) {
		t.Errorf("body: %v", got)
	}
}

func TestImagesWithAttachments(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	stored := `{"type":"mediaSingle","attrs":{"layout":"center","width":600,"widthType":"pixel"},"content":[{"type":"media","attrs":{"type":"file","id":"file-old","collection":"contentId-116","alt":"old.png","width":600,"height":400,"__fileName":"old.png"}}]}`
	pg := f.AddPage(confluencetest.Page{ID: "116", SpaceID: "10", ParentID: "100", Title: "Images", AuthorID: "acc-me",
		Body: json.RawMessage(`{"type":"doc","version":1,"content":[` + stored + `]}`)})
	s := service(t, f, people.Own)
	ctx := context.Background()
	v, err := s.Page(ctx, "116")
	if err != nil {
		t.Fatal(err)
	}
	if v.Body != `![old.png](attachment:old.png "center 600px")` {
		t.Fatalf("body: %q", v.Body)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "new chart.png")
	if err := os.WriteFile(file, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	md := v.Body + "\n\n![new chart.png](<attachment:new chart.png> \"wide\")"
	if _, err := s.PlanUpdate(ctx, UpdateInput{Page: "116", Version: v.Version, Markdown: md}); err == nil || !strings.Contains(err.Error(), "no attachment of that name") {
		t.Errorf("image without its file: %v", err)
	}
	plan, err := s.PlanUpdate(ctx, UpdateInput{Page: "116", Version: v.Version, Markdown: md, Attach: []string{file}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Description, "Attaches first:\n    new chart.png (3 bytes, from "+file+")") || !strings.Contains(plan.Description, "Adds: image or file.") {
		t.Errorf("plan:\n%s", plan.Description)
	}
	if _, err := plan.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	got := nodesOf(t, pg.Body)
	if got[0] != nodesOf(t, []byte(`{"type":"doc","content":[`+stored+`]}`))[0] || !regexp.MustCompile(`"collection":"contentId-116","id":"file-[0-9]+","type":"file"`).MatchString(got[1]) || strings.Contains(got[1], "pending:") {
		t.Errorf("body: %v", got)
	}
	// The file is attached now: a second image can refer to it.
	v, _ = s.Page(ctx, "116")
	if _, err := s.PlanUpdate(ctx, UpdateInput{Page: "116", Version: v.Version, Markdown: v.Body + "\n\n![new chart.png](<attachment:new chart.png>)"}); err != nil {
		t.Error(err)
	}
	if _, err := s.PlanUpdate(ctx, UpdateInput{Page: "116", Version: v.Version, Markdown: v.Body, Attach: []string{file}}); err == nil || !strings.Contains(err.Error(), "already has an attachment") {
		t.Errorf("attaching a file twice: %v", err)
	}

	// A new page with an image is created as a draft, the file attached to
	// it, then filled.
	plan, err = s.PlanCreate(ctx, CreateInput{Space: "DOC", Title: "Chart", Markdown: "See:\n\n![new chart.png](<attachment:new chart.png>)", Attach: []string{file}})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := plan.Apply(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var created *confluencetest.Page
	for _, p := range f.Pages {
		if p.Title == "Chart" {
			created = p
		}
	}
	if created == nil || created.Status != "draft" || !strings.Contains(string(created.Body), `"collection":"contentId-`+created.ID+`"`) || !strings.Contains(msg, "1 attached files") {
		t.Fatalf("created %+v: %s", created, msg)
	}
	att := 0
	for _, a := range f.Attachments {
		if a.PageID == created.ID {
			att++
		}
	}
	if att != 1 {
		t.Errorf("%d attachments on the new page", att)
	}
}

func TestHeadingsInListsAreKept(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	body := `{"type":"doc","version":1,"content":[{"type":"bulletList","content":[{"type":"listItem","content":[{"type":"heading","attrs":{"level":3},"content":[{"type":"text","text":"in a list"}]}]}]}]}`
	pg := f.AddPage(confluencetest.Page{ID: "112", SpaceID: "10", ParentID: "100", Title: "List", AuthorID: "acc-me", Body: json.RawMessage(body)})
	s := service(t, f, people.Own)
	ctx := context.Background()
	v, err := s.Page(ctx, "112")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.PlanUpdate(ctx, UpdateInput{Page: "112", Version: v.Version, Markdown: v.Body + "\n\nmore"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pg.Body), `"type":"heading","attrs":{"level":3}`) {
		t.Errorf("heading lost: %s", pg.Body)
	}
}
