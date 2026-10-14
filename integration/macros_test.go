//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nyactl/approvekit/people"
	adf "github.com/nyactl/go-adf"

	"github.com/nyactl/dok-for-confluence/internal/core"
	"github.com/nyactl/dok-for-confluence/internal/macros"
)

// standIn is a macro of an app that is not installed, shaped like a Forge
// diagram macro; Confluence stores it, so learning, fenced code and
// placeholders can be checked without the app.
const standIn = `{"type":"extension","attrs":{"layout":"extension","extensionType":"com.atlassian.ecosystem","extensionKey":"00000000-0000-4000-8000-000000000000/11111111-1111-4111-8111-111111111111/static/standin-diagram","text":"Stand-in Diagram","localId":"3f1c2a4e-0000-4000-8000-000000000003",
"parameters":{"layout":"extension","extensionTitle":"Stand-in Diagram","forgeEnvironment":"PRODUCTION","extensionId":"ari:cloud:ecosystem::extension/00000000-0000-4000-8000-000000000000/11111111-1111-4111-8111-111111111111/static/standin-diagram","guestParams":{"source":{"type":"text","text":"graph TD; learned-->page"},"theme":"default"}}}}`

// tinyPNG is a valid 2x2 PNG.
func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func stored(t *testing.T, s *site, id string) adf.Node {
	t.Helper()
	// A published page is not found when asked for its draft.
	p, err := s.cc.Page(context.Background(), id, false)
	if err != nil {
		p, err = s.cc.Page(context.Background(), id, true)
	}
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(strings.NewReader(p.Body.ADF.Value))
	dec.UseNumber()
	var n adf.Node
	if err := dec.Decode(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// types counts node and mark types; empty paragraphs, which Markdown cannot
// hold, and text pieces, which Confluence may join, are not counted.
func types(n adf.Node, into map[string]int) map[string]int {
	if into == nil {
		into = map[string]int{}
	}
	if n.Type != "text" && (n.Type != "paragraph" || len(n.Content) > 0) {
		into[n.Type]++
	}
	for _, m := range n.Marks {
		into["mark:"+m.Type]++
	}
	for _, c := range n.Content {
		types(c, into)
	}
	return into
}

// settle waits until Confluence has finished adding identifiers to a new
// page, which it does some moments after storing it, without a new version.
func settle(t *testing.T, s *site, id string) {
	t.Helper()
	prev := ""
	for range 20 {
		p, err := s.cc.Page(context.Background(), id, false)
		if err == nil && p.Body != nil && p.Body.ADF.Value == prev {
			return
		}
		if err == nil && p.Body != nil {
			prev = p.Body.ADF.Value
		}
		time.Sleep(2 * time.Second)
	}
	t.Logf("page %s kept changing", id)
}

func renders(t *testing.T, s *site, id string, want ...string) {
	t.Helper()
	html, err := s.cc.ViewHTML(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	// Confluence's own embeds, such as whiteboards, are drawn in the browser
	// and show as unknown in this view.
	html = regexp.MustCompile(`<img[^>]*unknown-macro\?name=native-embed:[^>]*>`).ReplaceAllString(html, "")
	if strings.Contains(strings.ToLower(html), "unknown-macro") || strings.Contains(html, "Unknown macro") {
		t.Errorf("page %s shows an unknown macro:\n%s", id, html)
	}
	for _, w := range want {
		if !strings.Contains(html, w) {
			t.Errorf("page %s renders without %q:\n%s", id, w, html)
		}
	}
}

func TestMacrosAndForms(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	spaceID := s.svc.Spaces()[0].ID
	builtin := macros.Builtin()

	// The stand-in macro is learned from a page, as `dok config macros learn` does.
	learnPage, err := s.cc.CreatePage(ctx, spaceID, s.parent, "dok learn "+s.run, []byte(`{"type":"doc","version":1,"content":[`+standIn+`]}`), true)
	if err != nil {
		t.Fatal(err)
	}
	defs, skipped, err := s.svc.LearnMacros(ctx, learnPage.ID, map[string]bool{})
	if err != nil || len(defs) != 1 || len(skipped) != 0 {
		t.Fatalf("learned %+v, skipped %v, %v", defs, skipped, err)
	}
	learned := defs[0]
	if raw, _ := json.Marshal(learned); strings.Contains(string(raw), "learned-->page") || strings.Contains(string(raw), "3f1c2a4e") {
		t.Errorf("definition holds page content or identifiers: %s", raw)
	}
	svc, err := core.New(ctx, s.cc, core.Settings{Privacy: people.Own, Spaces: []string{s.space},
		Macros: []macros.Def{builtin["toc"], builtin["children"], builtin["whiteboard"], learned},
		Fences: map[string]macros.Fence{"mermaid": {Macro: learned.Name, Param: "source.text"}}})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("allowed macros render", func(t *testing.T) {
		md := "## One\n\n```macro:toc\nmaxLevel: 2\n```\n\n## Two\n\n```macro:children\nall: true\n```\n\n```mermaid\ngraph LR; dok-->page\n```"
		plan, err := svc.PlanCreate(ctx, core.CreateInput{Space: s.space, ParentID: s.parent, Title: "dok macros " + s.run, Markdown: md, Publish: true})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(plan.Description, `Adds: macro "Child pages", macro "Stand-in Diagram", macro "Table of Contents".`) {
			t.Errorf("plan:\n%s", plan.Description)
		}
		id := createdID(t, applied(t)(plan, nil))
		settle(t, s, id)
		renders(t, s, id, "toc-macro")
		got := types(stored(t, s, id), nil)
		if got["extension"] != 3 || got["codeBlock"] != 0 {
			t.Errorf("stored: %v", got)
		}
		p, err := svc.Page(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if p.Body != md {
			t.Errorf("read back\ngot:  %q\nwant: %q", p.Body, md)
		}
		// An edit keeps the macros as they are, identifiers included.
		before := stored(t, s, id)
		applied(t)(svc.PlanUpdate(ctx, core.UpdateInput{Page: id, Markdown: p.Body + "\n\nmore", Version: p.Version}))
		after := stored(t, s, id)
		for i, n := range before.Content {
			if n.Type == "extension" && after.Content[i].Attrs["localId"] != n.Attrs["localId"] {
				t.Errorf("macro %d got a new identity", i)
			}
		}
	})

	t.Run("placeholders read before Confluence adds identifiers", func(t *testing.T) {
		doc := `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"early"}]},` +
			`{"type":"extension","attrs":{"layout":"default","extensionType":"com.atlassian.confluence.macro.core","extensionKey":"children","parameters":{"macroParams":{"depth":{"value":"1"}},"macroMetadata":{"title":"Child pages"}}}},` + standIn + `]}`
		created, err := s.cc.CreatePage(ctx, spaceID, s.parent, "dok early read "+s.run, []byte(doc), true)
		if err != nil {
			t.Fatal(err)
		}
		early, err := s.svc.Page(ctx, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		settle(t, s, created.ID)
		applied(t)(s.svc.PlanUpdate(ctx, core.UpdateInput{Page: created.ID, Version: early.Version, Markdown: early.Body + "\n\nlater"}))
		if got := types(stored(t, s, created.ID), nil); got["extension"] != 2 {
			t.Errorf("stored: %v", got)
		}
	})

	t.Run("refused macros", func(t *testing.T) {
		for md, want := range map[string]string{
			"```macro:html\n```": "not allowed",
			"```adf\n{\"type\":\"extension\",\"attrs\":{\"extensionType\":\"com.atlassian.confluence.macro.core\",\"extensionKey\":\"html\"}}\n```": "JSON parts cannot be added",
			"```macro:whiteboard\nurl: https://example.com/wiki/spaces/" + s.space + "/whiteboard/1\n```":                                           "allowed spaces",
		} {
			if _, err := svc.PlanCreate(ctx, core.CreateInput{Space: s.space, ParentID: s.parent, Title: "dok refused " + s.run, Markdown: md}); err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%q: err = %v, want %q", md, err, want)
			}
		}
	})

	t.Run("images", func(t *testing.T) {
		dir := t.TempDir()
		first, second := filepath.Join(dir, "dok first.png"), filepath.Join(dir, "dok-second.png")
		for _, f := range []string{first, second} {
			if err := os.WriteFile(f, tinyPNG(t), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		md := "An image:\n\n![dok first.png](<attachment:dok first.png> \"center\")"
		id := createdID(t, applied(t)(svc.PlanCreate(ctx, core.CreateInput{Space: s.space, ParentID: s.parent, Title: "dok images " + s.run, Markdown: md, Publish: true, Attach: []string{first}})))
		renders(t, s, id)
		doc := stored(t, s, id)
		raw, _ := json.Marshal(doc)
		if !regexp.MustCompile(`"collection":"contentId-`+id+`"`).Match(raw) || strings.Contains(string(raw), "pending:") || types(doc, nil)["mediaSingle"] != 1 {
			t.Errorf("stored: %s", raw)
		}
		p, err := svc.Page(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(p.Body, "![dok first.png](<attachment:dok first.png> \"center") {
			t.Errorf("read back: %q", p.Body)
		}
		applied(t)(svc.PlanUpdate(ctx, core.UpdateInput{Page: id, Version: p.Version, Markdown: p.Body + "\n\n![dok-second.png](attachment:dok-second.png)", Attach: []string{second}}))
		doc = stored(t, s, id)
		if types(doc, nil)["mediaSingle"] != 2 {
			raw, _ := json.Marshal(doc)
			t.Errorf("after update: %s", raw)
		}
		as, err := svc.Attachments(ctx, id, 10)
		if err != nil || len(as) != 2 {
			t.Errorf("attachments = %+v, %v", as, err)
		}
		// A draft with an image: the file goes to the draft.
		draft := createdID(t, applied(t)(svc.PlanCreate(ctx, core.CreateInput{Space: s.space, ParentID: s.parent, Title: "dok image draft " + s.run, Markdown: "![dok first.png](<attachment:dok first.png>)", Attach: []string{first}})))
		if dp, err := svc.Page(ctx, draft); err != nil || dp.Status != "draft" || !strings.Contains(dp.Body, "attachment:dok first.png") {
			t.Errorf("draft = %+v, %v", dp, err)
		}
	})

	// A page made in the editor, if one is named: it is copied, edited
	// through dok, and every part of it must survive.
	editor := os.Getenv("DOK_IT_EDITOR_PAGE")
	t.Run("editor page", func(t *testing.T) {
		if editor == "" {
			t.Skip("set DOK_IT_EDITOR_PAGE to a page made in the editor")
		}
		src, err := s.cc.Page(ctx, editor, false)
		if err != nil {
			t.Fatal(err)
		}
		copied, err := s.cc.CreatePage(ctx, spaceID, s.parent, "dok editor copy "+s.run, []byte(src.Body.ADF.Value), true)
		if err != nil {
			t.Fatal(err)
		}
		before := stored(t, s, copied.ID)
		p, err := svc.Page(ctx, copied.ID)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("editor page as Markdown:\n%s", p.Body)
		applied(t)(svc.PlanUpdate(ctx, core.UpdateInput{Page: copied.ID, Version: p.Version, Markdown: "Edited through dok.\n\n" + p.Body}))
		after := stored(t, s, copied.ID)
		want, got := types(before, nil), types(after, nil)
		want["paragraph"]++
		for k, n := range want {
			if got[k] != n {
				t.Errorf("%s: %d before, %d after", k, n, got[k])
			}
		}
		renders(t, s, copied.ID)
		// The whiteboard on it can be embedded on a new page.
		for _, n := range before.Content {
			if strAttr(n, "extensionKey") != "native-embed:whiteboard" {
				continue
			}
			params, _ := n.Attrs["parameters"].(map[string]any)
			mp, _ := params["macroParams"].(map[string]any)
			u, _ := mp["url"].(map[string]any)
			url, _ := u["value"].(string)
			id := createdID(t, applied(t)(svc.PlanCreate(ctx, core.CreateInput{Space: s.space, ParentID: s.parent, Title: "dok whiteboard " + s.run, Markdown: "```macro:whiteboard\nurl: " + url + "\n```", Publish: true})))
			renders(t, s, id)
		}
	})
}

func strAttr(n adf.Node, key string) string {
	s, _ := n.Attrs[key].(string)
	return s
}
