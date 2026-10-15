package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nyactl/approvekit/keychain"
	"github.com/nyactl/approvekit/picker"
	"github.com/nyactl/approvekit/profile"

	"github.com/nyactl/dok-for-confluence/internal/config"
	"github.com/nyactl/dok-for-confluence/internal/confluencetest"
)

// script is a fake terminal that answers prompts in order.
type script struct {
	picks   []string
	offered []string
	answers []string
	secrets []string
	asked   []string
	noTTY   bool
}

// Choose picks the first line containing the next of picks; none cancels.
func (s *script) Choose(opts picker.Options) (picker.Result, error) {
	if s.noTTY {
		return picker.Result{Index: -1}, errNoTerminal
	}
	s.offered = opts.Items
	if len(s.picks) == 0 {
		return picker.Result{Index: -1}, picker.ErrCanceled
	}
	want := s.picks[0]
	s.picks = s.picks[1:]
	for i, l := range opts.Items {
		if strings.Contains(l, want) {
			return picker.Result{Index: i}, nil
		}
	}
	return picker.Result{Index: -1}, picker.ErrCanceled
}

func (s *script) Prompt(q string) (string, error) {
	s.asked = append(s.asked, q)
	if s.noTTY || len(s.answers) == 0 {
		return "", errNoTerminal
	}
	a := s.answers[0]
	s.answers = s.answers[1:]
	return a, nil
}

func (s *script) Secret(q string) (string, error) {
	s.asked = append(s.asked, q)
	if s.noTTY || len(s.secrets) == 0 {
		return "", errNoTerminal
	}
	a := s.secrets[0]
	s.secrets = s.secrets[1:]
	return a, nil
}

type env struct {
	fake    *confluencetest.Fake
	secrets *keychain.Memory
	tty     *script
	dev     bool
}

func newEnv(t *testing.T) *env {
	t.Setenv("DOK_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("DOK_PROFILE", "")
	f := confluencetest.New(t)
	f.AddSpace(confluencetest.Space{ID: "10", Key: "DOC", Name: "Docs", HomepageID: "100"})
	f.AddPage(confluencetest.Page{ID: "100", SpaceID: "10", Title: "Docs home", AuthorID: "acc-me", Body: confluencetest.Doc("Hello")})
	f.AddPage(confluencetest.Page{ID: "101", SpaceID: "10", ParentID: "100", Title: "Runbook", AuthorID: "acc-alice", Body: confluencetest.Doc("Step one")})
	return &env{fake: f, secrets: keychain.NewMemory(), tty: &script{}}
}

func (e *env) version() string {
	if e.dev {
		return "v0.0.0-20261012100000-0123456789ab"
	}
	return "v9.9.9"
}

func (e *env) loggedIn(t *testing.T, spaces ...string) *env {
	t.Helper()
	cfg := &config.Config{Account: profile.Account{Site: e.fake.URL(), Email: "me@example.com"}, Privacy: config.PrivacyOwn, Spaces: spaces}
	if err := cfg.Save(""); err != nil {
		t.Fatal(err)
	}
	if err := e.secrets.Set(keychain.AccountKey(cfg.Site, cfg.Email), "tok-me"); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	return e.runStdin(t, "", args...)
}

func (e *env) runStdin(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	app := &App{Version: e.version(), In: strings.NewReader(stdin), Out: &out, Err: &errOut, TTY: e.tty, Secrets: e.secrets, Transport: e.fake.Transport(),
		Gateway: e.fake.URL(), Now: func() time.Time { return time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC) }}
	err := app.Execute(context.Background(), args)
	return out.String(), errOut.String(), err
}

func TestLoginWithScopedTokenKeepsSpaces(t *testing.T) {
	e := newEnv(t)
	e.fake.AddUser(confluencetest.User{AccountID: "acc-me", DisplayName: "Max Mustermann", Email: "me@example.com", Token: "tok-scoped",
		Scopes: []string{"read:confluence-user", "read:space:confluence", "read:page:confluence"}})
	e.tty.noTTY = true
	out, _, err := e.runStdin(t, "tok-scoped\n", "auth", "login", "--site", e.fake.URL(), "--email", "me@example.com", "--expires", "", "--with-token")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "scoped token, via the Atlassian API gateway") || !strings.Contains(out, "dok config spaces add KEY") {
		t.Errorf("out = %q", out)
	}
	if _, _, err := e.run(t, "config", "spaces", "add", "doc"); err != nil {
		t.Fatal(err)
	}
	out, _, err = e.run(t, "get", "101")
	if err != nil || !strings.Contains(out, "# Runbook") || !strings.Contains(out, "Created by: Person A") {
		t.Errorf("get via gateway: %q, %v", out, err)
	}
	// Logging in again keeps the allowed spaces.
	if _, _, err := e.runStdin(t, "tok-scoped\n", "auth", "login", "--site", e.fake.URL(), "--email", "me@example.com", "--expires", "", "--with-token"); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := config.Load(""); len(cfg.Spaces) != 1 || cfg.Spaces[0] != "doc" {
		t.Errorf("spaces after login = %v", cfg.Spaces)
	}
}

func TestLoginRejectsBadToken(t *testing.T) {
	e := newEnv(t)
	e.tty.noTTY = true
	_, _, err := e.runStdin(t, "wrong\n", "auth", "login", "--site", e.fake.URL(), "--email", "me@example.com", "--expires", "", "--with-token")
	if !errors.Is(err, errLoginRejected) {
		t.Fatalf("err = %v", err)
	}
	if _, err := config.Load(""); !errors.Is(err, config.ErrNotConfigured) {
		t.Errorf("config saved after failed login: %v", err)
	}
}

func TestNoSpacesYet(t *testing.T) {
	e := newEnv(t).loggedIn(t)
	if _, _, err := e.run(t, "get", "101"); err == nil || !strings.Contains(err.Error(), "dok config spaces add") {
		t.Errorf("err = %v", err)
	}
}

func TestSpacesAddAndRemoveIgnoreCase(t *testing.T) {
	e := newEnv(t).loggedIn(t)
	for _, args := range [][]string{{"add", "MoSys"}, {"add", "doc"}, {"add", "DOC"}, {"remove", "mosys"}} {
		if _, _, err := e.run(t, append([]string{"config", "spaces"}, args...)...); err != nil {
			t.Fatal(args, err)
		}
	}
	if cfg, _ := config.Load(""); strings.Join(cfg.Spaces, ",") != "DOC" {
		t.Errorf("spaces = %v", cfg.Spaces)
	}
}

func TestUpdateNeedsTypedPageID(t *testing.T) {
	e := newEnv(t).loggedIn(t, "DOC")
	file := filepath.Join(t.TempDir(), "page.md")
	if err := os.WriteFile(file, []byte("Step one\n\nStep two"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.tty.answers = []string{"y"}
	if _, _, err := e.run(t, "update", "101", "--file", file, "--version", "1"); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Errorf("y must not confirm an update: %v", err)
	}
	if e.fake.Pages["101"].Version != 1 {
		t.Fatal("page changed without confirmation")
	}
	e.tty.answers = []string{"101"}
	out, _, err := e.run(t, "update", "101", "--file", file, "--version", "1")
	if err != nil || !strings.Contains(out, "version 2") {
		t.Fatalf("update: %q, %v", out, err)
	}
	asked := e.tty.asked[len(e.tty.asked)-1]
	if !strings.Contains(asked, "Confluence site: "+e.fake.URL()+" (profile default)") || !strings.Contains(asked, "+ Step two") || !strings.Contains(asked, "Type 101") {
		t.Errorf("asked %q", asked)
	}
}

func TestCommentConfirmAndNoTerminal(t *testing.T) {
	e := newEnv(t).loggedIn(t, "DOC")
	e.tty.answers = []string{"y"}
	if _, _, err := e.run(t, "comment", "101", "--body", "Looks good"); err != nil {
		t.Fatal(err)
	}
	if len(e.fake.Comments) != 1 {
		t.Errorf("%d comments", len(e.fake.Comments))
	}
	e.tty.noTTY = true
	if _, _, err := e.run(t, "comment", "101", "--body", "again"); !errors.Is(err, errNoTerminal) {
		t.Errorf("without a terminal: %v", err)
	}
	if len(e.fake.Comments) != 1 {
		t.Error("comment stored without confirmation")
	}
}

func TestCreateDraft(t *testing.T) {
	e := newEnv(t).loggedIn(t, "DOC")
	e.tty.answers = []string{"y"}
	out, _, err := e.runStdin(t, "# Plan\n\n- one", "create", "--parent", "100", "--title", "Plan", "--file", "-")
	if err != nil || !strings.Contains(out, "(draft)") {
		t.Fatalf("create: %q, %v", out, err)
	}
}

func TestDevBuildsStayAwayFromProduction(t *testing.T) {
	e := newEnv(t).loggedIn(t, "DOC")
	e.dev = true
	if _, _, err := e.run(t, "whoami", "--profile", "default"); !errors.Is(err, profile.ErrDevBuildNotAllowed) {
		t.Errorf("default profile: %v", err)
	}
	// Without DOK_CONFIG, a development build must name a profile. HOME is
	// a temporary directory, so the real settings are never touched.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("DOK_CONFIG", "")
	if _, _, err := e.run(t, "whoami"); !errors.Is(err, errDevNeedsProfile) {
		t.Errorf("no profile: %v", err)
	}
}
