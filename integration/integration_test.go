//go:build integration

// Package integration runs dok against a real Confluence Cloud test site.
// See README.md in this directory; it never runs against the default
// profile or a profile that did not opt in to development builds.
package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/nyactl/approvekit/atlassian"
	"github.com/nyactl/approvekit/keychain"
	"github.com/nyactl/approvekit/people"
	"github.com/nyactl/approvekit/profile"

	"github.com/nyactl/dok-for-confluence/internal/config"
	"github.com/nyactl/dok-for-confluence/internal/confluence"
	"github.com/nyactl/dok-for-confluence/internal/core"
)

type site struct {
	cfg    *config.Config
	svc    *core.Service
	space  string
	parent string
	run    string
}

// testSiteConfig loads the profile named by DOK_PROFILE and refuses anything
// that could be production.
func testSiteConfig(t *testing.T) (string, *config.Config) {
	t.Helper()
	name := os.Getenv("DOK_PROFILE")
	if name == "" || name == config.DefaultProfile {
		t.Fatal("set DOK_PROFILE to a test profile; the default profile is never used")
	}
	cfg, err := config.Load(name)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AllowDevBuilds {
		t.Fatalf("profile %s did not opt in to development builds (dok config dev-builds on --profile %s)", name, name)
	}
	if def, err := config.Load(config.DefaultProfile); err == nil && def.Site == cfg.Site {
		t.Fatalf("profile %s points to the same site as the default profile; refusing to run", name)
	}
	if err := profile.CheckDevBuild("dev", name, &cfg.Account); err != nil {
		t.Fatal(err)
	}
	return name, cfg
}

func setup(t *testing.T) *site {
	t.Helper()
	_, cfg := testSiteConfig(t)
	space := os.Getenv("DOK_IT_SPACE")
	if space == "" {
		t.Fatal("set DOK_IT_SPACE to the key of the test space")
	}
	token, err := keychain.New("dok-for-confluence").Get(keychain.AccountKey(cfg.Site, cfg.Email))
	if err != nil {
		t.Fatal(err)
	}
	var opts []atlassian.Option
	if cfg.ScopedToken {
		opts = append(opts, atlassian.WithGateway(atlassian.Gateway, cfg.CloudID))
	}
	cc := confluence.New(cfg.Site, cfg.Email, token, "integration", opts...)
	// The key is configured in other case: Confluence matches keys
	// regardless of case, and so must dok, everywhere it uses them.
	svc, err := core.New(context.Background(), cc, core.Settings{Privacy: people.Own, Spaces: []string{swapCase(space)}})
	if err != nil {
		t.Fatal(err)
	}
	spaces := svc.Spaces()
	if len(spaces) != 1 || !strings.EqualFold(spaces[0].Key, space) {
		t.Fatalf("test space %s is not visible: %+v", space, spaces)
	}
	space = spaces[0].Key
	return &site{cfg: cfg, svc: svc, space: space, parent: os.Getenv("DOK_IT_PARENT"), run: time.Now().Format("20060102-150405")}
}

func applied(t *testing.T) func(*core.Plan, error) string {
	return func(p *core.Plan, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		msg, err := p.Apply(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
}

// createdID reads the page ID from a create message: "Created page 123 ...".
func createdID(t *testing.T, msg string) string {
	t.Helper()
	var id string
	if _, err := fmt.Sscanf(msg, "Created page %s", &id); err != nil {
		t.Fatalf("no page ID in %q", msg)
	}
	return id
}

func TestIntegration(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	t.Run("whoami", func(t *testing.T) {
		me, err := s.svc.Whoami(ctx)
		if err != nil || me.DisplayName == "" || me.Privacy != "own" {
			t.Errorf("whoami = %+v, %v", me, err)
		}
	})

	var draftID string
	t.Run("create a draft", func(t *testing.T) {
		in := core.CreateInput{Space: s.space, ParentID: s.parent, Title: "dok integration " + s.run,
			Markdown: "## Purpose\n\nCreated by the **dok** integration tests.\n\n- one\n- two\n\n```go\nfmt.Println(\"hi\")\n```"}
		draftID = createdID(t, applied(t)(s.svc.PlanCreate(ctx, in)))
		p, err := s.svc.Page(ctx, draftID)
		if err != nil {
			t.Fatal(err)
		}
		if p.Status != "draft" || p.Author != "Me" || !strings.Contains(p.Body, "Created by the **dok** integration tests.") || !strings.Contains(p.Body, "```go") {
			t.Errorf("draft = %+v", p)
		}
	})
	if draftID == "" {
		t.FailNow()
	}

	t.Run("publish", func(t *testing.T) {
		applied(t)(s.svc.PlanPublish(ctx, draftID))
		p, err := s.svc.Page(ctx, draftID)
		if err != nil || p.Status != "current" {
			t.Fatalf("after publish = %+v, %v", p, err)
		}
		if _, err := s.svc.PlanPublish(ctx, draftID); err == nil {
			t.Error("published twice")
		}
	})

	t.Run("update with version check", func(t *testing.T) {
		p, err := s.svc.Page(ctx, draftID)
		if err != nil {
			t.Fatal(err)
		}
		changed := p.Body + "\n\n- three"
		if _, err := s.svc.PlanUpdate(ctx, core.UpdateInput{Page: draftID, Markdown: changed, Version: p.Version - 1}); err == nil {
			t.Error("stale version accepted")
		}
		if _, err := s.svc.PlanUpdate(ctx, core.UpdateInput{Page: draftID, Markdown: p.Body, Version: p.Version}); err == nil {
			t.Error("update without changes accepted")
		}
		applied(t)(s.svc.PlanUpdate(ctx, core.UpdateInput{Page: draftID, Markdown: changed, Version: p.Version, Message: "dok integration"}))
		after, err := s.svc.Page(ctx, draftID)
		if err != nil || after.Version != p.Version+1 || !strings.Contains(after.Body, "- three") {
			t.Errorf("after update = %+v, %v", after, err)
		}
		// The body reads back as it was written: no escaping drift.
		if after.Body != strings.TrimSpace(changed) {
			t.Errorf("body changed on the round trip:\nwrote: %q\nread:  %q", changed, after.Body)
		}
	})

	t.Run("comment with mention of me", func(t *testing.T) {
		applied(t)(s.svc.PlanComment(ctx, draftID, "Reviewed, @[Me]."))
		cs, err := s.svc.Comments(ctx, draftID, 10)
		if err != nil || len(cs) == 0 || cs[len(cs)-1].Author != "Me" || !strings.Contains(cs[len(cs)-1].Body, "@[Me]") {
			t.Errorf("comments = %+v, %v", cs, err)
		}
	})

	t.Run("attach", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "dok-it.txt")
		if err := os.WriteFile(path, []byte("dok "+s.run), 0o600); err != nil {
			t.Fatal(err)
		}
		applied(t)(s.svc.PlanAttach(ctx, draftID, path))
		as, err := s.svc.Attachments(ctx, draftID, 10)
		if err != nil || len(as) == 0 || as[0].Title != "dok-it.txt" {
			t.Errorf("attachments = %+v, %v", as, err)
		}
	})

	t.Run("children", func(t *testing.T) {
		p, err := s.svc.Page(ctx, draftID)
		if err != nil {
			t.Fatal(err)
		}
		cs, err := s.svc.Children(ctx, p.ParentID, 250)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, c := range cs {
			found = found || c.ID == draftID
		}
		if !found {
			t.Errorf("page %s not among the children of %s", draftID, p.ParentID)
		}
	})

	t.Run("search", func(t *testing.T) {
		// The search index catches up within seconds to a minute.
		deadline := time.Now().Add(90 * time.Second)
		for {
			hits, err := s.svc.Search(ctx, s.run, 10)
			if err != nil {
				t.Fatal(err)
			}
			for _, h := range hits {
				if h.ID == draftID {
					return
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("page %s not found by search: %+v", draftID, hits)
			}
			time.Sleep(5 * time.Second)
		}
	})

	t.Run("other spaces are invisible", func(t *testing.T) {
		// A page ID that cannot be in the test space; the error is the same
		// for missing pages and pages in other spaces.
		if _, err := s.svc.Page(ctx, "1"); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("err = %v", err)
		}
	})
}

func swapCase(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsUpper(r) {
			return unicode.ToLower(r)
		}
		return unicode.ToUpper(r)
	}, s)
}
