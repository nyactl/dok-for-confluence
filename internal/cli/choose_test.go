package cli

import (
	"errors"
	"strings"
	"testing"
)

func TestChoosePage(t *testing.T) {
	e := newEnv(t).loggedIn(t, "DOC")
	e.tty.picks = []string{"Runbook"}
	out, _, err := e.run(t, "get")
	if err != nil || !strings.Contains(out, "# Runbook") || !strings.Contains(out, "Step one") {
		t.Fatalf("get without a page: %q, %v", out, err)
	}
	if len(e.tty.offered) != 2 || !strings.Contains(strings.Join(e.tty.offered, "\n"), "[DOC]") {
		t.Errorf("offered %q", e.tty.offered)
	}
	if _, _, err := e.run(t, "comment", "--body", "x"); !errors.Is(err, errNothingChosen) {
		t.Errorf("canceled: %v", err)
	}
	e.tty.noTTY = true
	if _, _, err := e.run(t, "get"); err == nil || !strings.Contains(err.Error(), "give a page ID") {
		t.Errorf("without a terminal: %v", err)
	}
}

func TestCompletePages(t *testing.T) {
	e := newEnv(t).loggedIn(t, "DOC")
	out, _, err := e.run(t, "__complete", "get", "10")
	if err != nil || !strings.Contains(out, "101\tRunbook [DOC]") || !strings.Contains(out, "100\tDocs home [DOC]") {
		t.Errorf("page completion: %q, %v", out, err)
	}
	if _, _, err := e.run(t, "config", "macros", "allow", "toc"); err != nil {
		t.Fatal(err)
	}
	if out, _, _ := e.run(t, "__complete", "config", "macros", "remove", ""); !strings.Contains(out, "toc\tTable of Contents") {
		t.Errorf("macro completion: %q", out)
	}
}

func TestMentionsAcrossCommands(t *testing.T) {
	e := newEnv(t).loggedIn(t, "DOC")
	out, _, err := e.run(t, "get", "101")
	if err != nil || !strings.Contains(out, "Created by: Person A") {
		t.Fatalf("get: %q, %v", out, err)
	}
	e.tty.answers = []string{"y"}
	if _, _, err := e.run(t, "comment", "101", "--body", "Thanks @[Person A]"); err != nil {
		t.Fatal(err)
	}
	c := e.fake.Comments[len(e.fake.Comments)-1]
	if !strings.Contains(string(c.Body), `"id":"acc-alice"`) {
		t.Errorf("comment %s", c.Body)
	}
}
