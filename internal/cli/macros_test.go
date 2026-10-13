package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyactl/dok-for-confluence/internal/config"
	"github.com/nyactl/dok-for-confluence/internal/confluencetest"
)

const learnable = `{"type":"doc","version":1,"content":[
{"type":"extension","attrs":{"layout":"extension","extensionType":"com.atlassian.ecosystem","extensionKey":"app-1/env-1/static/mermaid","text":"Mermaid Diagram","parameters":{"extensionTitle":"Mermaid Diagram","guestParams":{"source":{"type":"text","text":"graph TD; private-->content"},"theme":"dark"}}}},
{"type":"extension","attrs":{"extensionType":"com.atlassian.confluence.macro.core","extensionKey":"include","parameters":{"macroParams":{"":{"value":"OTHER:Secret page"}},"macroMetadata":{"title":"Include Page"}}}},
{"type":"extension","attrs":{"extensionType":"com.atlassian.confluence.macro.core","extensionKey":"status-report","parameters":{"macroParams":{"colour":{"value":"Green"}},"macroMetadata":{"title":"Status report"}}}}]}`

func TestConfigMacros(t *testing.T) {
	e := newEnv(t).loggedIn(t, "DOC")
	e.fake.AddPage(confluencetest.Page{ID: "102", SpaceID: "10", ParentID: "100", Title: "Macros", AuthorID: "acc-me", Body: json.RawMessage(learnable)})

	if _, _, err := e.run(t, "config", "macros", "allow", "toc", "whiteboard"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.run(t, "config", "macros", "allow", "html"); err == nil || !strings.Contains(err.Error(), "not a built-in macro") {
		t.Errorf("allow html: %v", err)
	}
	// Learning asks for each macro; the include macro is never offered.
	e.tty.answers = []string{"y", "n"}
	_, errOut, err := e.run(t, "config", "macros", "learn", "102")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut, "skipped Include Page") || !strings.Contains(errOut, "not allowed: status-report") {
		t.Errorf("stderr: %s", errOut)
	}
	if len(e.tty.asked) != 2 || !strings.Contains(e.tty.asked[0], "Allow macro mermaid-diagram") || !strings.Contains(e.tty.asked[0], "source.text (string)") {
		t.Errorf("asked: %q", e.tty.asked)
	}
	if _, _, err := e.run(t, "config", "macros", "fence", "mermaid", "mermaid-diagram", "source.text"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.run(t, "config", "macros", "fence", "mermaid", "mermaid-diagram", "theme-x"); err == nil {
		t.Error("fence to an unknown parameter accepted")
	}
	out, _, err := e.run(t, "config", "macros", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"- toc (Table of Contents, built-in)", "- whiteboard", "- mermaid-diagram (Mermaid Diagram, learned): source.text, source.type, theme", "fenced mermaid code becomes mermaid-diagram, in source.text"} {
		if !strings.Contains(out, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}
	path, _ := config.Path("")
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "private-->content") || strings.Contains(string(raw), "Secret page") {
		t.Errorf("config holds page content: %s", raw)
	}

	// The CLI writes pages with them.
	md := filepath.Join(t.TempDir(), "page.md")
	_ = os.WriteFile(md, []byte("```macro:toc\nmaxLevel: 2\n```\n\n```mermaid\ngraph LR; A-->B\n```"), 0o600)
	e.tty.answers = []string{"y"}
	if _, _, err := e.run(t, "create", "--space", "DOC", "--title", "With macros", "--file", md); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.tty.asked[len(e.tty.asked)-1], `Adds: macro "Mermaid Diagram", macro "Table of Contents".`) {
		t.Errorf("approval: %s", e.tty.asked[len(e.tty.asked)-1])
	}

	if _, _, err := e.run(t, "config", "macros", "remove", "mermaid-diagram"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load("")
	if len(cfg.Macros.Learned) != 0 || len(cfg.Macros.Fences) != 0 {
		t.Errorf("after remove: %+v", cfg.Macros)
	}
	if _, _, err := e.run(t, "config", "macros", "remove", "nothing"); err == nil {
		t.Error("removing an unknown macro succeeded")
	}
	// Learning needs a terminal.
	e.tty.noTTY = true
	if _, _, err := e.run(t, "config", "macros", "learn", "102"); err == nil {
		t.Error("learned without a terminal")
	}
}

func TestCreateWithAttachedImage(t *testing.T) {
	e := newEnv(t).loggedIn(t, "DOC")
	dir := t.TempDir()
	img := filepath.Join(dir, "chart.png")
	_ = os.WriteFile(img, []byte("png"), 0o600)
	md := filepath.Join(dir, "page.md")
	_ = os.WriteFile(md, []byte("![chart.png](attachment:chart.png)"), 0o600)
	e.tty.answers = []string{"y"}
	if _, _, err := e.run(t, "create", "--space", "DOC", "--title", "Chart", "--file", md, "--attach", img); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.tty.asked[0], "Attaches first:\n    chart.png (3 bytes, from "+img+")") {
		t.Errorf("approval: %s", e.tty.asked[0])
	}
	if len(e.fake.Attachments) != 1 || e.fake.Attachments[0].Title != "chart.png" {
		t.Errorf("attachments: %+v", e.fake.Attachments)
	}
}
