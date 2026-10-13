package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/nyactl/approvekit/sanitize"
)

// Terminal is the controlling terminal. Confirmations and secrets are read
// from it rather than stdin, so piped input and non-interactive callers,
// such as scripts or an assistant running the command, cannot answer them.
type Terminal interface {
	Prompt(question string) (string, error)
	Secret(question string) (string, error)
}

var errNoTerminal = errors.New("this needs an interactive terminal; run it yourself in a terminal window")

type devTTY struct{}

func open() (*os.File, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("%w (%w)", errNoTerminal, err)
	}
	return f, nil
}

func (devTTY) Prompt(question string) (string, error) {
	f, err := open()
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	if _, err := io.WriteString(f, sanitize.String(question)); err != nil {
		return "", err
	}
	line, err := bufio.NewReader(f).ReadString('\n')
	if err != nil && line == "" {
		return "", errNoTerminal
	}
	return strings.TrimSpace(line), nil
}

func (devTTY) Secret(question string) (string, error) {
	f, err := open()
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	if _, err := io.WriteString(f, question); err != nil {
		return "", err
	}
	b, err := term.ReadPassword(int(f.Fd()))
	_, _ = io.WriteString(f, "\n")
	if err != nil {
		return "", fmt.Errorf("reading from the terminal: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// confirm asks for approval. Destructive changes require typing the target
// (a page ID or space key); everything else a "y".
func confirm(t Terminal, description, key string, destructive bool) error {
	text := "\n" + description + "\n\n"
	if destructive && key != "" {
		answer, err := t.Prompt(text + "Type " + key + " to apply this change: ")
		if err != nil {
			return err
		}
		if !strings.EqualFold(answer, key) {
			return errors.New("not confirmed; nothing was changed")
		}
		return nil
	}
	answer, err := t.Prompt(text + "Apply this change? [y/N] ")
	if err != nil {
		return err
	}
	if a := strings.ToLower(answer); a != "y" && a != "yes" {
		return errors.New("not confirmed; nothing was changed")
	}
	return nil
}
