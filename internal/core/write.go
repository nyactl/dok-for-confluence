package core

import (
	"context"
	"encoding/json"

	"errors"
	"fmt"
	adf "github.com/nyactl/go-adf"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/nyactl/dok-for-confluence/internal/confluence"
)

// Kind classifies a write for the confirmation step.
type Kind string

const (
	Additive    Kind = "additive"
	Destructive Kind = "destructive"
)

// Plan is a prepared write. Description states exactly what Apply will do;
// callers must show it to the human and get approval first. A plan can be
// applied once.
type Plan struct {
	Kind        Kind
	Target      string // what the human confirms against: a page ID or space key
	Description string

	once  sync.Once
	apply func(context.Context) (string, error)
}

// Apply performs the write and returns a short result message.
func (p *Plan) Apply(ctx context.Context) (string, error) {
	var (
		msg string
		err = errors.New("plan was already applied")
	)
	p.once.Do(func() { msg, err = p.apply(ctx) })
	return msg, err
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
}

const maxTitle = 255

func checkTitle(title string) (string, error) {
	title = strings.TrimSpace(title)
	switch {
	case title == "":
		return "", errors.New("the title is empty")
	case utf8.RuneCountInString(title) > maxTitle:
		return "", fmt.Errorf("the title is longer than %d characters", maxTitle)
	case strings.ContainsAny(title, "\n\r"):
		return "", errors.New("the title must be one line")
	}
	return title, nil
}

// CreateInput describes a new page.
type CreateInput struct {
	Space    string // space key
	ParentID string // page ID or URL; empty for the top of the space
	Title    string
	Markdown string
	Publish  bool // publish right away instead of creating a draft
	// Attach are local files to attach first, for images that name them.
	Attach []string
}

// PlanCreate prepares a new page, a draft unless Publish is set.
func (s *Service) PlanCreate(ctx context.Context, in CreateInput) (*Plan, error) {
	if len(s.keys) == 0 {
		return nil, ErrNoSpaces
	}
	title, err := checkTitle(in.Title)
	if err != nil {
		return nil, err
	}
	var parent *confluence.Page
	if in.ParentID != "" {
		if parent, err = s.page(ctx, in.ParentID, false); err != nil {
			return nil, fmt.Errorf("parent: %w", err)
		}
		if err := s.writable(ctx, parent); err != nil {
			return nil, err
		}
	} else if len(s.parents) > 0 {
		return nil, errors.New("name a parent page: this profile only writes below certain pages (see `dok config show`)")
	}
	space, ok := s.spaceByKey(strings.TrimSpace(in.Space))
	switch {
	case parent != nil && in.Space == "":
		space = s.spaces[parent.SpaceID]
	case !ok:
		return nil, fmt.Errorf("space %q is not one of the spaces dok may use: %s", in.Space, strings.Join(s.keys, ", "))
	case parent != nil && parent.SpaceID != space.ID:
		return nil, fmt.Errorf("the parent page is in space %s, not %s", s.spaces[parent.SpaceID].Key, space.Key)
	}
	ups, err := readUploads(in.Attach)
	if err != nil {
		return nil, err
	}
	c, _, err := s.newCodec(nil, "")
	if err != nil {
		return nil, err
	}
	for _, u := range ups {
		c.uploads[u.name] = true
	}
	doc, err := c.parse(in.Markdown)
	if err != nil {
		return nil, err
	}
	added, _, err := partChanges(nil, doc)
	if err != nil {
		return nil, err
	}
	where := "at the top of space " + space.Key
	parentID := ""
	if parent != nil {
		where = fmt.Sprintf("in space %s below %q (%s)", space.Key, s.people.Text(parent.Title), parent.ID)
		parentID = parent.ID
	}
	state := "a draft (publish it later with `dok publish`)"
	if in.Publish {
		state = "a published page, visible to everyone who can see the space"
	}
	desc := fmt.Sprintf("Create %q %s, as %s, with this content:\n\n%s", title, where, state, indent(in.Markdown))
	desc += partSummary(added, nil, ups)
	return &Plan{
		Kind:        Additive,
		Target:      space.Key,
		Description: desc,
		apply: func(ctx context.Context) (string, error) {
			if len(ups) == 0 {
				p, err := s.cc.CreatePage(ctx, space.ID, parentID, title, doc, in.Publish)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Created page %s (%s): %s", p.ID, p.Status, s.pageURL(p)), nil
			}
			// Images refer to their files, which need the page: it is
			// created as an empty draft, the files are attached, and then
			// the draft gets its content, at its own version, and is
			// published if asked.
			p, err := s.cc.CreatePage(ctx, space.ID, parentID, title, json.RawMessage(`{"type":"doc","version":1,"content":[]}`), false)
			if err != nil {
				return "", err
			}
			full, err := s.uploadAll(ctx, p.ID, true, ups, doc)
			if err != nil {
				return "", fmt.Errorf("created draft %s, but %w", p.ID, err)
			}
			version := 1
			if p.Version != nil && p.Version.Number > 0 {
				version = p.Version.Number
			}
			if p, err = s.cc.UpdatePage(ctx, p.ID, "draft", title, full, version, ""); err != nil {
				return "", fmt.Errorf("created draft %s and attached the files, but writing its content failed: %w", p.ID, err)
			}
			if in.Publish {
				if p, err = s.cc.UpdatePage(ctx, p.ID, "current", title, full, version, ""); err != nil {
					return "", fmt.Errorf("created draft %s with its files, but publishing it failed: %w", p.ID, err)
				}
			}
			return fmt.Sprintf("Created page %s (%s) with %d attached files: %s", p.ID, p.Status, len(ups), s.pageURL(p)), nil
		},
	}, nil
}

// UpdateInput describes a change to a page's content.
type UpdateInput struct {
	Page     string // page ID or URL
	Markdown string
	Title    string // empty keeps the title
	// Version is the version the change is based on. The update fails if
	// the page changed since, so nothing is overwritten unseen.
	Version int
	Message string // optional version comment
	// Attach are local files to attach first, for images that name them.
	Attach []string
}

// PlanUpdate prepares replacing a page's content; the plan shows the
// difference.
func (s *Service) PlanUpdate(ctx context.Context, in UpdateInput) (*Plan, error) {
	p, err := s.page(ctx, in.Page, true)
	if err != nil {
		return nil, err
	}
	if err := s.writable(ctx, p); err != nil {
		return nil, err
	}
	if p.Status == "draft" {
		return nil, fmt.Errorf("page %s is a draft; publish it first or edit it in Confluence", p.ID)
	}
	current := 0
	if p.Version != nil {
		current = p.Version.Number
	}
	switch {
	case in.Version <= 0:
		return nil, fmt.Errorf("give the version the change is based on (page %s is at version %d)", p.ID, current)
	case in.Version != current:
		return nil, fmt.Errorf("page %s changed since version %d; it is at version %d now: read it again and redo the change", p.ID, in.Version, current)
	}
	title := p.Title
	if in.Title != "" {
		if title, err = checkTitle(in.Title); err != nil {
			return nil, err
		}
	}
	ups, err := readUploads(in.Attach)
	if err != nil {
		return nil, err
	}
	c, shown, err := s.newCodec(p.Body, p.ID)
	if err != nil {
		return nil, err
	}
	old, err := c.render(ctx, shown)
	if err != nil {
		return nil, err
	}
	if err := s.loadAttachments(ctx, c, in.Markdown, ups); err != nil {
		return nil, err
	}
	doc, err := c.parse(in.Markdown)
	if err != nil {
		return nil, err
	}
	newMD := strings.TrimSpace(in.Markdown)
	if newMD == old {
		if title == p.Title {
			return nil, errors.New("nothing would change")
		}
		if p.Body != nil {
			// Only the title changes: the stored body is kept exactly.
			doc = json.RawMessage(p.Body.ADF.Value)
		}
	}
	lost, err := s.lostParts(ctx, p.Body, p.ID, doc)
	if err != nil {
		return nil, err
	}
	if len(lost) > 0 {
		return nil, fmt.Errorf("page %s contains parts that cannot be edited as Markdown, and this update would delete them: %s. Change the page in Confluence instead, or change only its title", p.ID, strings.Join(lost, ", "))
	}
	var stored json.RawMessage
	if p.Body != nil {
		stored = json.RawMessage(p.Body.ADF.Value)
	}
	added, removed, err := partChanges(stored, doc)
	if err != nil {
		return nil, err
	}
	var desc strings.Builder
	fmt.Fprintf(&desc, "Update page %q (%s) from version %d to %d.", s.people.Text(p.Title), p.ID, current, current+1)
	if title != p.Title {
		fmt.Fprintf(&desc, "\n\nTitle:\n    from: %q\n    to:   %q", s.people.Text(p.Title), title)
	}
	if newMD != old {
		fmt.Fprintf(&desc, "\n\nContent changes:\n\n%s", indent(lineDiff(old, newMD)))
	}
	desc.WriteString(partSummary(s.redacted(added), s.redacted(removed), ups))
	return &Plan{
		Kind:        Destructive,
		Target:      p.ID,
		Description: desc.String(),
		apply: func(ctx context.Context) (string, error) {
			doc := doc
			if len(ups) > 0 {
				// Attaching files adds no page version.
				if doc, err = s.uploadAll(ctx, p.ID, false, ups, doc); err != nil {
					return "", err
				}
			}
			u, err := s.cc.UpdatePage(ctx, p.ID, "current", title, doc, current+1, in.Message)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Updated page %s to version %d.", u.ID, current+1), nil
		},
	}, nil
}

// PlanPublish prepares publishing a draft.
func (s *Service) PlanPublish(ctx context.Context, ref string) (*Plan, error) {
	p, err := s.page(ctx, ref, true)
	if err != nil {
		return nil, err
	}
	if err := s.writable(ctx, p); err != nil {
		return nil, err
	}
	if p.Status != "draft" {
		return nil, fmt.Errorf("page %s is already published", p.ID)
	}
	version := 1
	if p.Version != nil {
		version = p.Version.Number
	}
	space := s.spaces[p.SpaceID].Key
	return &Plan{
		Kind:        Destructive,
		Target:      p.ID,
		Description: fmt.Sprintf("Publish the draft %q (%s) in space %s. It becomes visible to everyone who can see the space.", s.people.Text(p.Title), p.ID, space),
		apply: func(ctx context.Context) (string, error) {
			// A draft is published as its own version number.
			u, err := s.cc.UpdatePage(ctx, p.ID, "current", p.Title, []byte(p.Body.ADF.Value), version, "")
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Published page %s: %s", u.ID, s.pageURL(u)), nil
		},
	}, nil
}

// PlanComment prepares a footer comment written in Markdown.
func (s *Service) PlanComment(ctx context.Context, ref, body string) (*Plan, error) {
	if strings.TrimSpace(body) == "" {
		return nil, errors.New("comment is empty")
	}
	p, err := s.page(ctx, ref, false)
	if err != nil {
		return nil, err
	}
	if err := s.writable(ctx, p); err != nil {
		return nil, err
	}
	doc, err := s.toADF(body)
	if err != nil {
		return nil, err
	}
	return &Plan{
		Kind:        Additive,
		Target:      p.ID,
		Description: fmt.Sprintf("Add a comment to %q (%s):\n\n%s", s.people.Text(p.Title), p.ID, indent(body)),
		apply: func(ctx context.Context) (string, error) {
			c, err := s.cc.AddFooterComment(ctx, p.ID, doc)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Added comment %s to page %s.", c.ID, p.ID), nil
		},
	}, nil
}

const maxUploadBytes = 50 << 20

var unsafeFileChars = regexp.MustCompile(`[^A-Za-z0-9._ -]+`)

// PlanAttach prepares uploading a local file. The file is read now, so the
// upload is exactly what the human approved.
func (s *Service) PlanAttach(ctx context.Context, ref, path string) (*Plan, error) {
	up, err := readUpload(path)
	if err != nil {
		return nil, err
	}
	p, err := s.page(ctx, ref, false)
	if err != nil {
		return nil, err
	}
	if err := s.writable(ctx, p); err != nil {
		return nil, err
	}
	name, data := up.name, up.data
	return &Plan{
		Kind:        Additive,
		Target:      p.ID,
		Description: fmt.Sprintf("Attach %s to %q (%s).", up, s.people.Text(p.Title), p.ID),
		apply: func(ctx context.Context) (string, error) {
			a, err := s.cc.AddAttachment(ctx, p.ID, name, data, p.Status == "draft")
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Attached %s to page %s as attachment %s.", name, p.ID, a.ID), nil
		},
	}, nil
}

// upload is a local file read for a change, so the upload is exactly what
// the human approved.
type upload struct {
	name, path string
	data       []byte
}

func (u upload) String() string {
	return fmt.Sprintf("%s (%s, from %s)", u.name, formatBytes(int64(len(u.data))), u.path)
}

func readUpload(path string) (upload, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return upload{}, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return upload{}, err
	}
	if !st.Mode().IsRegular() {
		return upload{}, fmt.Errorf("%s is not a regular file", abs)
	}
	if st.Size() > maxUploadBytes {
		return upload{}, fmt.Errorf("%s is %s; the limit is %s", abs, formatBytes(st.Size()), formatBytes(maxUploadBytes))
	}
	f, err := os.Open(abs) //nolint:gosec // G304: uploading the file the user named is the point; it is shown in the plan
	if err != nil {
		return upload{}, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxUploadBytes+1))
	if err != nil {
		return upload{}, err
	}
	if len(data) > maxUploadBytes {
		return upload{}, fmt.Errorf("%s grew beyond the limit of %s", abs, formatBytes(maxUploadBytes))
	}
	name := strings.Trim(unsafeFileChars.ReplaceAllString(filepath.Base(abs), "_"), ". ")
	if name == "" {
		name = "attachment"
	}
	return upload{name: name, path: abs, data: data}, nil
}

func readUploads(paths []string) ([]upload, error) {
	var out []upload
	seen := map[string]bool{}
	for _, p := range paths {
		u, err := readUpload(p)
		if err != nil {
			return nil, err
		}
		if seen[u.name] {
			return nil, fmt.Errorf("two files are named %s", u.name)
		}
		seen[u.name] = true
		out = append(out, u)
	}
	return out, nil
}

// uploadAll attaches the files to a page and puts their file IDs into the
// images of doc that refer to them.
func (s *Service) uploadAll(ctx context.Context, pageID string, draft bool, ups []upload, doc json.RawMessage) (json.RawMessage, error) {
	ids := map[string]string{}
	for _, u := range ups {
		a, err := s.cc.AddAttachment(ctx, pageID, u.name, u.data, draft)
		if err != nil {
			return nil, fmt.Errorf("attaching %s: %w", u.name, err)
		}
		if a.FileID == "" {
			return nil, fmt.Errorf("attaching %s: Confluence returned no file ID", u.name)
		}
		ids[pendingID(u.name)] = a.FileID
	}
	n, err := decode(string(doc))
	if err != nil {
		return nil, err
	}
	var fill func(n *adf.Node)
	fill = func(n *adf.Node) {
		if n.Type == "media" {
			if id, ok := ids[strAttr(n.Attrs, "id")]; ok {
				n.Attrs["id"] = id
				n.Attrs["collection"] = "contentId-" + pageID
			}
		}
		for i := range n.Content {
			fill(&n.Content[i])
		}
	}
	fill(&n)
	return json.Marshal(n)
}

func formatBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}
