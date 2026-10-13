package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nyactl/approvekit/atlassian"
	"github.com/nyactl/approvekit/people"

	"github.com/nyactl/dok-for-confluence/internal/confluence"
	"github.com/nyactl/dok-for-confluence/internal/confluencetest"
	"github.com/nyactl/dok-for-confluence/internal/core"
)

func site(t *testing.T) *confluencetest.Fake {
	t.Helper()
	f := confluencetest.New(t)
	f.AddSpace(confluencetest.Space{ID: "10", Key: "DOC", Name: "Docs", HomepageID: "100"})
	f.AddSpace(confluencetest.Space{ID: "20", Key: "HR", Name: "HR", HomepageID: "200"})
	f.AddPage(confluencetest.Page{ID: "100", SpaceID: "10", Title: "Docs home", AuthorID: "acc-me"})
	f.AddPage(confluencetest.Page{ID: "101", SpaceID: "10", ParentID: "100", Title: "Runbook", AuthorID: "acc-alice",
		Body: confluencetest.Doc("Ask Alice Smith or {acc-bob}")})
	f.AddPage(confluencetest.Page{ID: "200", SpaceID: "20", Title: "Salaries", AuthorID: "acc-alice"})
	return f
}

type harness struct {
	fake    *confluencetest.Fake
	session *mcp.ClientSession
	asked   []string
}

func connect(t *testing.T, writes bool, answer string) *harness {
	t.Helper()
	ctx := context.Background()
	h := &harness{fake: site(t)}
	cc := confluence.New(h.fake.URL(), "me@example.com", "tok-me", "test", atlassian.WithTransport(h.fake.Transport()))
	svc, err := core.New(ctx, cc, core.Settings{Privacy: people.Own, Spaces: []string{"DOC"}})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(svc, Options{Version: "test", AllowWrites: writes, Target: "Confluence site: fake (profile test)"})
	st, ct := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	var opts *mcp.ClientOptions
	if answer != "" {
		opts = &mcp.ClientOptions{ElicitationHandler: func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			h.asked = append(h.asked, r.Params.Message)
			if answer == "approve" {
				return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"approve": true}}, nil
			}
			return &mcp.ElicitResult{Action: answer}, nil
		}}
	}
	h.session, err = mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, opts).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.session.Close() })
	return h
}

func (h *harness) call(t *testing.T, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := h.session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

func toolMap(t *testing.T, h *harness) map[string]*mcp.Tool {
	t.Helper()
	res, err := h.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		m[tool.Name] = tool
	}
	return m
}

var writeTools = []string{"add_comment", "attach_file", "create_page", "publish_page", "update_page"}

func TestReadOnlyByDefault(t *testing.T) {
	t.Parallel()
	tools := toolMap(t, connect(t, false, ""))
	if len(tools) != 7 {
		t.Errorf("got %d read tools", len(tools))
	}
	for name, tool := range tools {
		if slices.Contains(writeTools, name) {
			t.Errorf("write tool %s registered without --allow-writes", name)
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s is not marked read-only", name)
		}
	}
}

func TestWriteToolAnnotations(t *testing.T) {
	t.Parallel()
	tools := toolMap(t, connect(t, true, "approve"))
	for _, name := range writeTools {
		tool, ok := tools[name]
		if !ok {
			t.Errorf("%s missing", name)
			continue
		}
		wantDestructive := name == "update_page" || name == "publish_page"
		a := tool.Annotations
		if a == nil || a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint != wantDestructive {
			t.Errorf("%s annotations = %+v", name, a)
		}
	}
}

func TestReadsAreScopedAndRedacted(t *testing.T) {
	t.Parallel()
	h := connect(t, false, "")
	out, isErr := h.call(t, "get_page", map[string]any{"page": "101"})
	if isErr || strings.Contains(out, "Alice") || strings.Contains(out, "Bob") || !strings.Contains(out, "@[Person B]") {
		t.Errorf("get_page (err=%v): %s", isErr, out)
	}
	if out, isErr := h.call(t, "get_page", map[string]any{"page": "200"}); !isErr || !strings.Contains(out, "outside the spaces") {
		t.Errorf("other space (err=%v): %s", isErr, out)
	}
}

func TestWriteNeedsApproval(t *testing.T) {
	t.Parallel()
	h := connect(t, true, "approve")
	h.call(t, "get_page", map[string]any{"page": "101"})
	out, isErr := h.call(t, "add_comment", map[string]any{"page": "101", "body": "Done, thanks @[Person A]"})
	if isErr {
		t.Fatal(out)
	}
	if len(h.asked) != 1 || !strings.HasPrefix(h.asked[0], "Confluence site: fake (profile test)\n\nAdd a comment to \"Runbook\" (101)") {
		t.Errorf("asked %q", h.asked)
	}
	if len(h.fake.Comments) != 1 {
		t.Errorf("%d comments stored", len(h.fake.Comments))
	}
}

func TestWriteRefusedWithoutApproval(t *testing.T) {
	t.Parallel()
	for _, answer := range []string{"decline", "cancel", ""} {
		h := connect(t, true, answer)
		out, isErr := h.call(t, "create_page", map[string]any{"space": "DOC", "title": "New", "markdown": "x"})
		want := "not approved"
		if answer == "" {
			want = "cannot show approval dialogs"
		}
		if !isErr || !strings.Contains(out, want) {
			t.Errorf("answer %q: (err=%v) %s", answer, isErr, out)
		}
		if len(h.fake.Pages) != 3 {
			t.Errorf("answer %q: a page was created", answer)
		}
	}
}

func TestForgedApprovalRejected(t *testing.T) {
	t.Parallel()
	h := connect(t, true, "decline")
	res, err := h.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:           "add_comment",
		Arguments:      map[string]any{"page": "101", "body": "x"},
		InputResponses: mcp.InputResponseMap{"approval": &mcp.ElicitResult{Action: "accept", Content: map[string]any{"approve": true}}},
		RequestState:   "00.ff",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || len(h.fake.Comments) != 0 {
		t.Errorf("forged approval: isError=%v comments=%d", res.IsError, len(h.fake.Comments))
	}
}

func TestOutOfScopeFailsBeforeAsking(t *testing.T) {
	t.Parallel()
	h := connect(t, true, "approve")
	out, isErr := h.call(t, "add_comment", map[string]any{"page": "200", "body": "hi"})
	if !isErr || len(h.asked) != 0 || len(h.fake.Comments) != 0 {
		t.Errorf("err=%v asked=%v out=%s", isErr, h.asked, out)
	}
}

func TestCheckUploadPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	plain := filepath.Join(dir, "report.txt")
	hiddenDir := filepath.Join(dir, ".ssh")
	key := filepath.Join(hiddenDir, "id_ed25519")
	link := filepath.Join(dir, "innocent.txt")
	for _, err := range []error{
		os.WriteFile(plain, []byte("x"), 0o600),
		os.Mkdir(hiddenDir, 0o700),
		os.WriteFile(key, []byte("x"), 0o600),
		os.Symlink(key, link),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := checkUploadPath(plain); err != nil {
		t.Errorf("plain file: %v", err)
	}
	for _, p := range []string{key, link, "report.txt"} {
		if err := checkUploadPath(p); err == nil {
			t.Errorf("%s was accepted", p)
		}
	}
}
