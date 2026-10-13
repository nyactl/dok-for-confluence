package macros

import (
	"encoding/json"
	"strings"
	"testing"

	adf "github.com/nyactl/go-adf"
)

func node(t *testing.T, s string) adf.Node {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var n adf.Node
	if err := dec.Decode(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// As the Confluence editor stores them.
const (
	editorTOC    = `{"type":"extension","attrs":{"layout":"default","extensionType":"com.atlassian.confluence.macro.core","extensionKey":"toc","parameters":{"macroParams":{"style":{"value":"none"}},"macroMetadata":{"macroId":{"value":"a685959c"},"schemaVersion":{"value":"1"},"title":"Table of Contents"}},"localId":"e0d05a52"}}`
	forgeDiagram = `{"type":"extension","attrs":{"layout":"extension","extensionType":"com.atlassian.ecosystem","extensionKey":"app-1/env-1/static/mermaid","text":"Mermaid Diagram","localId":"l1",
		"parameters":{"layout":"extension","localId":"l1","extensionId":"ari:cloud:ecosystem::extension/app-1/env-1/static/mermaid","extensionTitle":"Mermaid Diagram","forgeEnvironment":"PRODUCTION",
		"guestParams":{"source":{"type":"text","text":"graph TD; secret-->content"},"theme":"dark","scale":2,"showBorder":true,"ownerAccountId":"557058:0fe7a3a1-0000-4000-8000-1234567890ab","lastEditor":"712020:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
		"embeddedMacroContext":{"accountId":"557058:0fe7a3a1-0000-4000-8000-1234567890ab","pageId":"42","spaceKey":"DOC"}}}}`
)

func TestBuiltinTOC(t *testing.T) {
	t.Parallel()
	d := Builtin()["toc"]
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	v, ok := d.Read(node(t, editorTOC))
	if !ok || v["style"] != "none" || len(v) != 1 {
		t.Fatalf("Read = %v, %v", v, ok)
	}
	if got := d.Block(v); got != "style: none" {
		t.Errorf("Block = %q", got)
	}
	v, err := d.ParseBlock("maxLevel: 3\n\nstyle: disc\n")
	if err != nil {
		t.Fatal(err)
	}
	n, err := d.Build(v)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(n)
	for _, want := range []string{`"extensionKey":"toc"`, `"maxLevel":{"value":"3"}`, `"style":{"value":"disc"}`, `"title":"Table of Contents"`, `"localId":"`, `"macroId":{"value":"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("built macro lacks %s: %s", want, raw)
		}
	}
	if back, ok := d.Read(n); !ok || back["maxLevel"] != "3" {
		t.Errorf("built macro does not read back: %v %v", back, ok)
	}
	other, _ := d.Build(v)
	if !Same(n, other) || other.Attrs["localId"] == n.Attrs["localId"] {
		t.Error("two builds should be the same apart from new identifiers")
	}
	for _, bad := range []string{"maxLevel: three", "style: fancy", "outline: yes", "colour: red", "maxLevel 3", "maxLevel: 1\nmaxLevel: 2"} {
		v, err := d.ParseBlock(bad)
		if err == nil {
			_, err = d.Build(v)
		}
		if err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	// A parameter the definition does not know keeps the macro from being
	// shown as a block.
	withUnknown := strings.Replace(editorTOC, `"style":{"value":"none"}`, `"style":{"value":"none"},"class":{"value":"x"}`, 1)
	if _, ok := d.Read(node(t, withUnknown)); ok {
		t.Error("macro with an unknown parameter read as a block")
	}
}

func TestLearnKeepsNoContentOrPeople(t *testing.T) {
	t.Parallel()
	d, err := Learn(node(t, forgeDiagram), Slug("Mermaid Diagram"))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(d)
	for _, bad := range []string{"secret", "557058", "712020", "embeddedMacroContext", `"l1"`, "pageId", "Owner", "lastEditor"} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("learned definition holds %q: %s", bad, raw)
		}
	}
	if d.Name != "mermaid-diagram" || d.Title != "Mermaid Diagram" {
		t.Errorf("name %q, title %q", d.Name, d.Title)
	}
	kinds := map[string]string{}
	for _, p := range d.Params {
		kinds[p.Name] = p.Kind
	}
	want := map[string]string{"source.text": String, "source.type": String, "theme": String, "scale": Number, "showBorder": Boolean}
	for name, kind := range want {
		if kinds[name] != kind {
			t.Errorf("parameter %s: kind %q, want %q (all: %v)", name, kinds[name], kind, kinds)
		}
	}
	f := Fence{Macro: d.Name, Param: "source.text"}
	if err := d.CheckFence(f); err != nil {
		t.Fatal(err)
	}
	n, err := d.BuildFence(f, "graph LR; A-->B")
	if err != nil {
		t.Fatal(err)
	}
	if code, ok := d.ReadFence(f, n); !ok || code != "graph LR; A-->B" {
		t.Errorf("ReadFence = %q, %v", code, ok)
	}
	built, _ := json.Marshal(n)
	for _, want := range []string{`"extensionKey":"app-1/env-1/static/mermaid"`, `"forgeEnvironment":"PRODUCTION"`, `"scale":2`, `"theme":"dark"`, `"type":"text"`, `"text":"graph LR; A--\u003eB"`} {
		if !strings.Contains(string(built), want) {
			t.Errorf("built macro lacks %s: %s", want, built)
		}
	}
	if err := d.CheckFence(Fence{Macro: d.Name, Param: "scale"}); err == nil {
		t.Error("a number parameter accepted for fenced code")
	}
}

func TestForbiddenMacros(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"include", "excerpt-include", "html", "html-include", "iframe", "widget", "rss", "contentbylabel", "app/env/static/page-include"} {
		if !Forbidden(key) {
			t.Errorf("%s is not forbidden", key)
		}
		n := node(t, `{"type":"extension","attrs":{"extensionType":"com.atlassian.confluence.macro.core","extensionKey":"`+key+`","parameters":{"macroParams":{}}}}`)
		if _, err := Learn(n, "x"); err == nil {
			t.Errorf("%s learned", key)
		}
		if err := (Def{Name: "x", ExtensionType: "t", ExtensionKey: key}).Validate(); err == nil {
			t.Errorf("%s valid as a definition", key)
		}
	}
	for _, key := range []string{"toc", "children", "native-embed:whiteboard", "app-1/env-1/static/mermaid", "status", "jira"} {
		if Forbidden(key) {
			t.Errorf("%s is forbidden", key)
		}
	}
	if _, err := Learn(node(t, `{"type":"bodiedExtension","attrs":{"extensionType":"t","extensionKey":"details"},"content":[]}`), "x"); err == nil {
		t.Error("a macro with a body learned")
	}
}

func TestSlug(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"Table of Contents": "table-of-contents", "  Mermaid: Diagram!  ": "mermaid-diagram", "123 go": "macro-123-go", "": "macro"} {
		if got := Slug(in); got != want || !ValidName(got) {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}
