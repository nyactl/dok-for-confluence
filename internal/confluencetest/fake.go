// Package confluencetest is an in-memory fake of the Confluence Cloud REST
// endpoints dok uses, for unit tests. It follows the behaviour observed on
// a real site: drafts are readable without get-draft, publishing a draft
// keeps its version number, updates need the next version number, and
// scoped tokens only work through the gateway path.
package confluencetest

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// CloudID is the fake site's cloud ID.
const CloudID = "0b8e7c1a-5d2f-4e3a-9b6c-1f2e3d4c5b6a"

const gatewayPrefix = "/ex/confluence/" + CloudID

type User struct {
	AccountID, DisplayName, Email, Token string
	// Scopes makes Token a scoped API token: it only works through the
	// gateway path and only for requests its scopes cover.
	Scopes []string
}

type Space struct {
	ID, Key, Name, HomepageID string
}

type Page struct {
	ID, SpaceID, ParentID, Title, Status string
	AuthorID                             string
	Version                              int
	VersionAuthor                        string
	// Body is the page as an ADF document.
	Body json.RawMessage
}

type Comment struct {
	ID, PageID, AuthorID string
	Body                 json.RawMessage
}

type Attachment struct {
	ID, PageID, Title, MediaType, AuthorID, FileID string
	Data                                           []byte
}

// Fake is the fake site.
type Fake struct {
	mu          sync.Mutex
	srv         *httptest.Server
	nextID      int
	Users       map[string]*User
	Spaces      []*Space
	Pages       map[string]*Page
	Comments    []*Comment
	Attachments []*Attachment
	CQL         []string // every CQL query received
	Requests    []string
}

// New starts a fake site with three users (the tested user acc-me and two
// others), two spaces and a few pages. It is closed when the test ends.
func New(t testing.TB) *Fake {
	f := &Fake{Users: map[string]*User{}, Pages: map[string]*Page{}, nextID: 9000}
	f.AddUser(User{AccountID: "acc-me", DisplayName: "Max Mustermann", Email: "me@example.com", Token: "tok-me"})
	f.AddUser(User{AccountID: "acc-alice", DisplayName: "Alice Smith", Email: "alice@example.com", Token: "tok-alice"})
	f.AddUser(User{AccountID: "acc-bob", DisplayName: "Bob Jones", Email: "bob@example.com", Token: "tok-bob"})
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *Fake) URL() string { return f.srv.URL }

// Transport trusts the fake's TLS certificate.
func (f *Fake) Transport() http.RoundTripper { return f.srv.Client().Transport }

func (f *Fake) AddUser(u User) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Users[u.AccountID] = &u
}

func (f *Fake) AddSpace(s Space) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Spaces = append(f.Spaces, &s)
}

// AddPage adds a page; Status defaults to current and Version to 1.
func (f *Fake) AddPage(p Page) *Page {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p.Status == "" {
		p.Status = "current"
	}
	if p.Version == 0 {
		p.Version = 1
	}
	if p.VersionAuthor == "" {
		p.VersionAuthor = p.AuthorID
	}
	if p.Body == nil {
		p.Body = Doc("")
	}
	f.Pages[p.ID] = &p
	return &p
}

// Doc returns a one-paragraph ADF document; {acc-x} becomes a mention.
func Doc(text string) json.RawMessage {
	var content []any
	re := regexp.MustCompile(`\{(acc-[a-z]+)\}`)
	last := 0
	for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
		if m[0] > last {
			content = append(content, map[string]any{"type": "text", "text": text[last:m[0]]})
		}
		content = append(content, map[string]any{"type": "mention", "attrs": map[string]string{"id": text[m[2]:m[3]], "text": "@someone"}})
		last = m[1]
	}
	if last < len(text) {
		content = append(content, map[string]any{"type": "text", "text": text[last:]})
	}
	blocks := []any{}
	if len(content) > 0 {
		blocks = append(blocks, map[string]any{"type": "paragraph", "content": content})
	}
	b, _ := json.Marshal(map[string]any{"type": "doc", "version": 1, "content": blocks})
	return b
}

func (f *Fake) id() string {
	f.nextID++
	return strconv.Itoa(f.nextID)
}

func (f *Fake) caller(r *http.Request) *User {
	h := strings.TrimPrefix(r.Header.Get("Authorization"), "Basic ")
	raw, err := base64.StdEncoding.DecodeString(h)
	if err != nil {
		return nil
	}
	email, token, _ := strings.Cut(string(raw), ":")
	for _, u := range f.Users {
		if u.Email == email && u.Token == token {
			return u
		}
	}
	return nil
}

var (
	rePage            = regexp.MustCompile(`^/wiki/api/v2/pages/([0-9]+)(/children|/footer-comments|/attachments)?$`)
	reAttachmentWrite = regexp.MustCompile(`^/wiki/rest/api/content/([0-9]+)/child/attachment$`)
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func errJSON(w http.ResponseWriter, status int, title string) {
	writeJSON(w, status, map[string]any{"errors": []any{map[string]any{"status": status, "title": title}}})
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Requests = append(f.Requests, r.Method+" "+r.URL.Path)
	if r.URL.Path == "/_edge/tenant_info" {
		writeJSON(w, 200, map[string]string{"cloudId": CloudID})
		return
	}
	viaGateway := strings.HasPrefix(r.URL.Path, gatewayPrefix+"/")
	if viaGateway {
		r.URL.Path = strings.TrimPrefix(r.URL.Path, gatewayPrefix)
	}
	me := f.caller(r)
	if me == nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if me.Scopes != nil {
		if !viaGateway {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if scope := requiredScope(r); !slices.Contains(me.Scopes, scope) {
			writeJSON(w, 401, map[string]any{"code": 401, "message": "Unauthorized; scope does not match"})
			return
		}
	}
	var data []byte
	if r.Body != nil {
		data, _ = io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(data))
	}
	p, q := r.URL.Path, r.URL.Query()
	switch {
	case p == "/wiki/rest/api/user/current":
		writeJSON(w, 200, f.userJSON(me.AccountID))
	case p == "/wiki/rest/api/user":
		if _, ok := f.Users[q.Get("accountId")]; !ok {
			errJSON(w, 404, "user not found")
			return
		}
		writeJSON(w, 200, f.userJSON(q.Get("accountId")))
	case p == "/wiki/api/v2/spaces":
		keys := strings.Split(q.Get("keys"), ",")
		var out []any
		for _, s := range f.Spaces {
			// Like Confluence, keys match regardless of case.
			if slices.ContainsFunc(keys, func(k string) bool { return strings.EqualFold(k, s.Key) }) {
				out = append(out, map[string]string{"id": s.ID, "key": s.Key, "name": s.Name, "type": "global", "status": "current", "homepageId": s.HomepageID})
			}
		}
		writeJSON(w, 200, map[string]any{"results": out, "_links": map[string]string{}})
	case p == "/wiki/api/v2/pages" && r.Method == http.MethodPost:
		f.createPage(w, data, me)
	case p == "/wiki/api/v2/footer-comments" && r.Method == http.MethodPost:
		var body struct {
			PageID string `json:"pageId"`
			Body   struct {
				Value string `json:"value"`
			} `json:"body"`
		}
		_ = json.Unmarshal(data, &body)
		if _, ok := f.Pages[body.PageID]; !ok || !json.Valid([]byte(body.Body.Value)) {
			errJSON(w, 400, "invalid comment")
			return
		}
		c := &Comment{ID: f.id(), PageID: body.PageID, AuthorID: me.AccountID, Body: json.RawMessage(body.Body.Value)}
		f.Comments = append(f.Comments, c)
		writeJSON(w, 200, f.commentJSON(c))
	case reAttachmentWrite.MatchString(p) && r.Method == http.MethodPost:
		f.upload(w, r, reAttachmentWrite.FindStringSubmatch(p)[1], me)
	case p == "/wiki/rest/api/search":
		f.search(w, q)
	case rePage.MatchString(p):
		m := rePage.FindStringSubmatch(p)
		page, ok := f.Pages[m[1]]
		if !ok {
			errJSON(w, 404, "Not Found")
			return
		}
		f.pageRoute(w, r, page, m[2], data, me)
	default:
		errJSON(w, 404, "no route for "+r.Method+" "+p)
	}
}

func requiredScope(r *http.Request) string {
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/wiki/rest/api/user"):
		return "read:confluence-user"
	case p == "/wiki/api/v2/spaces":
		return "read:space:confluence"
	case p == "/wiki/rest/api/search":
		return "search:confluence"
	case strings.HasSuffix(p, "/footer-comments") || p == "/wiki/api/v2/footer-comments":
		if r.Method == http.MethodPost {
			return "write:comment:confluence"
		}
		return "read:comment:confluence"
	case strings.HasSuffix(p, "/attachments"):
		return "read:attachment:confluence"
	case strings.HasSuffix(p, "/child/attachment"):
		return "write:confluence-file"
	case r.Method == http.MethodGet:
		return "read:page:confluence"
	}
	return "write:page:confluence"
}

func (f *Fake) userJSON(id string) map[string]string {
	u := f.Users[id]
	return map[string]string{"accountId": u.AccountID, "displayName": u.DisplayName, "publicName": u.DisplayName, "email": u.Email}
}

func (f *Fake) pageJSON(p *Page, body bool) map[string]any {
	out := map[string]any{
		"id": p.ID, "status": p.Status, "title": p.Title, "spaceId": p.SpaceID, "parentId": p.ParentID,
		"authorId": p.AuthorID, "ownerId": p.AuthorID, "createdAt": "2026-10-01T09:00:00.000Z",
		"version": map[string]any{"number": p.Version, "authorId": p.VersionAuthor, "createdAt": "2026-10-02T09:00:00.000Z"},
		"_links":  map[string]string{"webui": "/spaces/X/pages/" + p.ID},
	}
	if body {
		out["body"] = map[string]any{"atlas_doc_format": map[string]string{"value": string(p.Body), "representation": "atlas_doc_format"}}
	}
	return out
}

func (f *Fake) commentJSON(c *Comment) map[string]any {
	return map[string]any{
		"id": c.ID, "pageId": c.PageID,
		"version": map[string]any{"number": 1, "authorId": c.AuthorID, "createdAt": "2026-10-03T09:00:00.000Z"},
		"body":    map[string]any{"atlas_doc_format": map[string]string{"value": string(c.Body), "representation": "atlas_doc_format"}},
	}
}

func (f *Fake) pageRoute(w http.ResponseWriter, r *http.Request, p *Page, sub string, data []byte, me *User) {
	switch sub + " " + r.Method {
	case " GET":
		writeJSON(w, 200, f.pageJSON(p, r.URL.Query().Get("body-format") == "atlas_doc_format"))
	case " PUT":
		f.updatePage(w, p, data, me)
	case "/children GET":
		var out []any
		for _, c := range f.sortedPages() {
			if c.ParentID == p.ID && c.Status == "current" {
				out = append(out, map[string]string{"id": c.ID, "status": c.Status, "title": c.Title, "spaceId": c.SpaceID})
			}
		}
		writeJSON(w, 200, map[string]any{"results": out, "_links": map[string]string{}})
	case "/footer-comments GET":
		var out []any
		for _, c := range f.Comments {
			if c.PageID == p.ID {
				out = append(out, f.commentJSON(c))
			}
		}
		writeJSON(w, 200, map[string]any{"results": out, "_links": map[string]string{}})
	case "/attachments GET":
		var out []any
		for _, a := range f.Attachments {
			if a.PageID == p.ID {
				out = append(out, map[string]any{"id": a.ID, "title": a.Title, "fileId": a.FileID, "mediaType": a.MediaType, "fileSize": len(a.Data),
					"version": map[string]any{"number": 1, "authorId": a.AuthorID, "createdAt": "2026-10-03T09:00:00.000Z"}})
			}
		}
		writeJSON(w, 200, map[string]any{"results": out, "_links": map[string]string{}})
	default:
		errJSON(w, 405, "method not allowed")
	}
}

type pageWrite struct {
	SpaceID  string `json:"spaceId"`
	Status   string `json:"status"`
	Title    string `json:"title"`
	ParentID string `json:"parentId"`
	Body     struct {
		Representation string `json:"representation"`
		Value          string `json:"value"`
	} `json:"body"`
	Version struct {
		Number  int    `json:"number"`
		Message string `json:"message"`
	} `json:"version"`
}

func (f *Fake) createPage(w http.ResponseWriter, data []byte, me *User) {
	var in pageWrite
	if err := json.Unmarshal(data, &in); err != nil || in.Body.Representation != "atlas_doc_format" || !json.Valid([]byte(in.Body.Value)) {
		errJSON(w, 400, "invalid page")
		return
	}
	if !slices.ContainsFunc(f.Spaces, func(s *Space) bool { return s.ID == in.SpaceID }) {
		errJSON(w, 400, "space not found")
		return
	}
	if in.ParentID != "" {
		if parent, ok := f.Pages[in.ParentID]; !ok || parent.SpaceID != in.SpaceID {
			errJSON(w, 400, "parent not found")
			return
		}
	}
	p := &Page{ID: f.id(), SpaceID: in.SpaceID, ParentID: in.ParentID, Title: in.Title, Status: in.Status, AuthorID: me.AccountID,
		Version: 1, VersionAuthor: me.AccountID, Body: json.RawMessage(in.Body.Value)}
	f.Pages[p.ID] = p
	writeJSON(w, 200, f.pageJSON(p, true))
}

func (f *Fake) updatePage(w http.ResponseWriter, p *Page, data []byte, me *User) {
	var in pageWrite
	if err := json.Unmarshal(data, &in); err != nil || !json.Valid([]byte(in.Body.Value)) {
		errJSON(w, 400, "invalid page")
		return
	}
	switch {
	case p.Status == "draft" && in.Status == "current":
		// Publishing a draft keeps its version number.
		if in.Version.Number != p.Version {
			errJSON(w, 409, fmt.Sprintf("Version must be the draft's version when publishing. Current Version: [%d]. Provided version: [%d]", p.Version, in.Version.Number))
			return
		}
		p.Status = "current"
	case p.Status == "draft" && in.Status == "draft":
		// Changing a draft keeps its version number too.
		if in.Version.Number != p.Version {
			errJSON(w, 409, fmt.Sprintf("Version must be the draft's version. Current Version: [%d]. Provided version: [%d]", p.Version, in.Version.Number))
			return
		}
	case in.Version.Number != p.Version+1:
		errJSON(w, 409, fmt.Sprintf("Version must be incremented when updating a page. Current Version: [%d]. Provided version: [%d]", p.Version, in.Version.Number))
		return
	default:
		p.Version++
	}
	p.Title, p.Body, p.VersionAuthor = in.Title, json.RawMessage(in.Body.Value), me.AccountID
	writeJSON(w, 200, f.pageJSON(p, true))
}

func (f *Fake) upload(w http.ResponseWriter, r *http.Request, pageID string, me *User) {
	// Like Confluence, the v1 API finds drafts only with status=draft.
	pg, ok := f.Pages[pageID]
	if !ok || (pg.Status == "draft") != (r.URL.Query().Get("status") == "draft") {
		errJSON(w, 404, "Not Found")
		return
	}
	if r.Header.Get("X-Atlassian-Token") != "no-check" {
		writeJSON(w, 403, map[string]string{"message": "XSRF check failed"})
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, 400, map[string]string{"message": "no file"})
		return
	}
	data, _ := io.ReadAll(file)
	id := f.id()
	a := &Attachment{ID: "att" + id, PageID: pageID, Title: hdr.Filename, MediaType: "text/plain", AuthorID: me.AccountID, Data: data, FileID: "file-" + id}
	f.Attachments = append(f.Attachments, a)
	writeJSON(w, 200, map[string]any{"results": []any{map[string]any{"id": a.ID, "title": a.Title, "extensions": map[string]any{"fileId": a.FileID, "collectionName": "contentId-" + pageID}}}})
}

var (
	cqlSpacesRe = regexp.MustCompile(`space in \(([^)]*)\)`)
	cqlTextRe   = regexp.MustCompile(`text ~ "((?:[^"\\]|\\.)*)"`)
)

// search understands the CQL dok writes: space in (...) and text ~ "...".
func (f *Fake) search(w http.ResponseWriter, q map[string][]string) {
	cql := ""
	if v := q["cql"]; len(v) > 0 {
		cql = v[0]
	}
	f.CQL = append(f.CQL, cql)
	var keys []string
	if m := cqlSpacesRe.FindStringSubmatch(cql); m != nil {
		for _, k := range strings.Split(m[1], ",") {
			keys = append(keys, strings.Trim(strings.TrimSpace(k), `"`))
		}
	}
	text := ""
	if m := cqlTextRe.FindStringSubmatch(cql); m != nil {
		text = strings.ToLower(strings.ReplaceAll(m[1], `\"`, `"`))
	}
	var out []any
	for _, p := range f.sortedPages() {
		var space *Space
		for _, s := range f.Spaces {
			if s.ID == p.SpaceID {
				space = s
			}
		}
		if p.Status != "current" || space == nil || (keys != nil && !slices.Contains(keys, space.Key)) {
			continue
		}
		if text != "" && !strings.Contains(strings.ToLower(p.Title+" "+string(p.Body)), text) {
			continue
		}
		res := map[string]any{"title": p.Title, "excerpt": "@@@hl@@@" + p.Title + "@@@endhl@@@", "lastModified": "2026-10-02T09:00:00.000Z",
			"content": map[string]any{"id": p.ID, "type": "page", "title": p.Title, "space": map[string]string{"key": space.Key},
				"history": map[string]any{"createdBy": f.userJSON(p.AuthorID), "lastUpdated": map[string]any{"by": f.userJSON(p.VersionAuthor)}}}}
		out = append(out, res)
	}
	writeJSON(w, 200, map[string]any{"results": out})
}

func (f *Fake) sortedPages() []*Page {
	out := make([]*Page, 0, len(f.Pages))
	for _, p := range f.Pages {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
