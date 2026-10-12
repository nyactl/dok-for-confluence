// Package confluence is a minimal Confluence Cloud REST client covering what
// dok needs: the v2 API for spaces, pages, comments and attachments, and the
// v1 API for search, users and uploads. It runs with the permissions of the
// token's user. Authentication, the API gateway, retries and error decoding
// come from approvekit's atlassian package.
package confluence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/nyactl/approvekit/atlassian"
)

// Client talks to the Confluence of one Atlassian Cloud site as one user.
type Client struct {
	*atlassian.Client
}

// New returns a client for site (already normalised to https://host).
func New(site, email, token, version string, opts ...atlassian.Option) *Client {
	opts = append([]atlassian.Option{atlassian.WithScopeHint(scopesFor)}, opts...)
	return &Client{atlassian.New(atlassian.Confluence, site, email, token, "dok-for-confluence/"+version, opts...)}
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	err := c.Do(ctx, method, path, query, body, out)
	if errors.Is(err, atlassian.ErrMissingScope) {
		return fmt.Errorf("%w; create a token with the scopes listed in the README (Scoped API tokens) and run `dok auth login`", err)
	}
	return err
}

var idRe = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)

// CheckID validates a numeric content or space ID, which ends up in URL
// paths.
func CheckID(id string) error {
	if !idRe.MatchString(id) {
		return fmt.Errorf("invalid ID %q: expected a number such as 123456", id)
	}
	return nil
}

var pageURLRe = regexp.MustCompile(`/pages/(?:edit-v2/|viewpage\.action\?pageId=)?([1-9][0-9]{0,19})`)

// PageID accepts a page ID or a page URL and returns the ID.
func PageID(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if CheckID(ref) == nil {
		return ref, nil
	}
	if u, err := url.Parse(ref); err == nil && u.Host != "" {
		if m := pageURLRe.FindStringSubmatch(u.Path); m != nil {
			return m[1], nil
		}
		if id := u.Query().Get("pageId"); CheckID(id) == nil {
			return id, nil
		}
	}
	return "", fmt.Errorf("%q is not a page ID or page URL", ref)
}

// User is a Confluence account. Email is often hidden by the user's privacy
// settings.
type User struct {
	AccountID   string `json:"accountId"`
	DisplayName string `json:"displayName"`
	PublicName  string `json:"publicName"`
	Email       string `json:"email"`
}

// Myself returns the authenticated user.
func (c *Client) Myself(ctx context.Context) (*User, error) {
	var u User
	if err := c.do(ctx, http.MethodGet, "/wiki/rest/api/user/current", nil, nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// User returns an account by ID.
func (c *Client) User(ctx context.Context, accountID string) (*User, error) {
	var u User
	if err := c.do(ctx, http.MethodGet, "/wiki/rest/api/user", url.Values{"accountId": {accountID}}, nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// Space is a Confluence space.
type Space struct {
	ID         string `json:"id"`
	Key        string `json:"key"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Status     string `json:"status"`
	HomepageID string `json:"homepageId"`
}

type links struct {
	Next  string `json:"next"`
	WebUI string `json:"webui"`
	Base  string `json:"base"`
}

// pages collects cursor-paginated v2 results until limit or the end.
func pages[T any](ctx context.Context, c *Client, path string, query url.Values, limit int) ([]T, error) {
	var all []T
	q := url.Values{}
	for k, v := range query {
		q[k] = v
	}
	for len(all) < limit {
		q.Set("limit", strconv.Itoa(min(limit-len(all), 250)))
		var page struct {
			Results []T   `json:"results"`
			Links   links `json:"_links"`
		}
		if err := c.do(ctx, http.MethodGet, path, q, nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Results...)
		cursor := ""
		if next, err := url.Parse(page.Links.Next); err == nil {
			cursor = next.Query().Get("cursor")
		}
		if cursor == "" || len(page.Results) == 0 {
			break
		}
		q.Set("cursor", cursor)
	}
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// SpacesByKey returns the spaces with these keys that the user can see.
func (c *Client) SpacesByKey(ctx context.Context, keys []string) ([]Space, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	return pages[Space](ctx, c, "/wiki/api/v2/spaces", url.Values{"keys": {strings.Join(keys, ",")}}, 250)
}

// Version is one version of a page or comment.
type Version struct {
	Number    int    `json:"number"`
	Message   string `json:"message,omitempty"`
	AuthorID  string `json:"authorId,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
	MinorEdit bool   `json:"minorEdit,omitempty"`
}

// Body holds content in the Atlassian Document Format. Confluence returns
// the document as a JSON string inside the JSON response.
type Body struct {
	ADF struct {
		Value string `json:"value"`
	} `json:"atlas_doc_format"`
}

// Page is a Confluence page.
type Page struct {
	ID        string   `json:"id"`
	Status    string   `json:"status"` // current, draft, archived, trashed
	Title     string   `json:"title"`
	SpaceID   string   `json:"spaceId"`
	ParentID  string   `json:"parentId"`
	AuthorID  string   `json:"authorId"`
	OwnerID   string   `json:"ownerId"`
	CreatedAt string   `json:"createdAt"`
	Version   *Version `json:"version"`
	Body      *Body    `json:"body"`
	Links     links    `json:"_links"`
}

// Page returns a page with its body. A draft is only returned with
// draft true.
func (c *Client) Page(ctx context.Context, id string, draft bool) (*Page, error) {
	if err := CheckID(id); err != nil {
		return nil, err
	}
	q := url.Values{"body-format": {"atlas_doc_format"}}
	if draft {
		q.Set("get-draft", "true")
	}
	var p Page
	if err := c.do(ctx, http.MethodGet, "/wiki/api/v2/pages/"+id, q, nil, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// PageMeta returns a page without its body.
func (c *Client) PageMeta(ctx context.Context, id string) (*Page, error) {
	if err := CheckID(id); err != nil {
		return nil, err
	}
	var p Page
	if err := c.do(ctx, http.MethodGet, "/wiki/api/v2/pages/"+id, nil, nil, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ChildPage is a page below another page.
type ChildPage struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Title   string `json:"title"`
	SpaceID string `json:"spaceId"`
}

// Children returns up to limit child pages of a page, in their order.
func (c *Client) Children(ctx context.Context, id string, limit int) ([]ChildPage, error) {
	if err := CheckID(id); err != nil {
		return nil, err
	}
	return pages[ChildPage](ctx, c, "/wiki/api/v2/pages/"+id+"/children", nil, limit)
}

// Comment is a footer comment on a page.
type Comment struct {
	ID      string   `json:"id"`
	PageID  string   `json:"pageId"`
	Version *Version `json:"version"`
	Body    *Body    `json:"body"`
}

// FooterComments returns up to limit footer comments of a page.
func (c *Client) FooterComments(ctx context.Context, id string, limit int) ([]Comment, error) {
	if err := CheckID(id); err != nil {
		return nil, err
	}
	return pages[Comment](ctx, c, "/wiki/api/v2/pages/"+id+"/footer-comments", url.Values{"body-format": {"atlas_doc_format"}}, limit)
}

// Attachment is a file attached to a page.
type Attachment struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	MediaType string   `json:"mediaType"`
	FileSize  int64    `json:"fileSize"`
	Version   *Version `json:"version"`
}

// Attachments returns up to limit attachments of a page.
func (c *Client) Attachments(ctx context.Context, id string, limit int) ([]Attachment, error) {
	if err := CheckID(id); err != nil {
		return nil, err
	}
	return pages[Attachment](ctx, c, "/wiki/api/v2/pages/"+id+"/attachments", nil, limit)
}

// SearchResult is one hit of a CQL search.
type SearchResult struct {
	Content struct {
		ID    string `json:"id"`
		Type  string `json:"type"`
		Title string `json:"title"`
		Space struct {
			Key string `json:"key"`
		} `json:"space"`
		History struct {
			CreatedBy   User `json:"createdBy"`
			LastUpdated struct {
				By User `json:"by"`
			} `json:"lastUpdated"`
		} `json:"history"`
	} `json:"content"`
	Title                 string `json:"title"`
	Excerpt               string `json:"excerpt"`
	LastModified          string `json:"lastModified"`
	ResultGlobalContainer struct {
		Title string `json:"title"`
	} `json:"resultGlobalContainer"`
}

// Search runs a CQL query and returns up to limit results.
func (c *Client) Search(ctx context.Context, cql string, limit int) ([]SearchResult, error) {
	var out struct {
		Results []SearchResult `json:"results"`
	}
	// The history names who created and last changed each page, so their
	// names can be redacted in titles and excerpts.
	q := url.Values{"cql": {cql}, "limit": {strconv.Itoa(min(limit, 100))}, "expand": {"content.space,content.history,content.history.lastUpdated"}}
	if err := c.do(ctx, http.MethodGet, "/wiki/rest/api/search", q, nil, &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}

type bodyWrite struct {
	Representation string `json:"representation"`
	Value          string `json:"value"`
}

// CreatePage creates a page, as a draft unless publish is true. parentID
// may be empty for a page at the top of the space.
func (c *Client) CreatePage(ctx context.Context, spaceID, parentID, title string, doc json.RawMessage, publish bool) (*Page, error) {
	status := "draft"
	if publish {
		status = "current"
	}
	body := map[string]any{
		"spaceId": spaceID,
		"status":  status,
		"title":   title,
		"body":    bodyWrite{"atlas_doc_format", string(doc)},
	}
	if parentID != "" {
		body["parentId"] = parentID
	}
	var p Page
	if err := c.do(ctx, http.MethodPost, "/wiki/api/v2/pages", nil, body, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// UpdatePage replaces a page's title and body as the given version, which
// must be the current version plus one. status is "current" or "draft".
func (c *Client) UpdatePage(ctx context.Context, id, status, title string, doc json.RawMessage, version int, message string) (*Page, error) {
	if err := CheckID(id); err != nil {
		return nil, err
	}
	body := map[string]any{
		"id":      id,
		"status":  status,
		"title":   title,
		"body":    bodyWrite{"atlas_doc_format", string(doc)},
		"version": map[string]any{"number": version, "message": message},
	}
	var p Page
	if err := c.do(ctx, http.MethodPut, "/wiki/api/v2/pages/"+id, nil, body, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// AddFooterComment adds a comment at the bottom of a page.
func (c *Client) AddFooterComment(ctx context.Context, pageID string, doc json.RawMessage) (*Comment, error) {
	if err := CheckID(pageID); err != nil {
		return nil, err
	}
	body := map[string]any{"pageId": pageID, "body": bodyWrite{"atlas_doc_format", string(doc)}}
	var cm Comment
	if err := c.do(ctx, http.MethodPost, "/wiki/api/v2/footer-comments", nil, body, &cm); err != nil {
		return nil, err
	}
	return &cm, nil
}

// AddAttachment uploads a file to a page. The v2 API has no upload, so this
// uses the v1 API.
func (c *Client) AddAttachment(ctx context.Context, pageID, filename string, data []byte) (*Attachment, error) {
	if err := CheckID(pageID); err != nil {
		return nil, err
	}
	body, err := atlassian.Multipart("file", filename, data)
	if err != nil {
		return nil, err
	}
	var out struct {
		Results []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"results"`
	}
	if err := c.do(ctx, http.MethodPost, "/wiki/rest/api/content/"+pageID+"/child/attachment", nil, body, &out); err != nil {
		return nil, err
	}
	if len(out.Results) == 0 {
		return nil, errors.New("the upload was accepted but Confluence returned no attachment")
	}
	return &Attachment{ID: out.Results[0].ID, Title: out.Results[0].Title}, nil
}

// scopesFor names the scoped-token scopes of the endpoints dok uses, as given
// in Atlassian's API specifications.
func scopesFor(method, path string) string {
	for _, e := range endpointScopes {
		if e.method == method && e.path.MatchString(path) {
			return e.scopes
		}
	}
	return ""
}

var endpointScopes = []struct {
	method string
	path   *regexp.Regexp
	scopes string
}{
	{http.MethodGet, regexp.MustCompile(`^/wiki/rest/api/user(/current)?$`), "read:confluence-user"},
	{http.MethodGet, regexp.MustCompile(`^/wiki/api/v2/spaces$`), "read:space:confluence"},
	{http.MethodGet, regexp.MustCompile(`^/wiki/api/v2/pages/[0-9]+$`), "read:page:confluence"},
	{http.MethodGet, regexp.MustCompile(`^/wiki/api/v2/pages/[0-9]+/children$`), "read:page:confluence"},
	{http.MethodGet, regexp.MustCompile(`^/wiki/api/v2/pages/[0-9]+/footer-comments$`), "read:comment:confluence"},
	{http.MethodGet, regexp.MustCompile(`^/wiki/api/v2/pages/[0-9]+/attachments$`), "read:attachment:confluence"},
	{http.MethodGet, regexp.MustCompile(`^/wiki/rest/api/search$`), "search:confluence"},
	{http.MethodPost, regexp.MustCompile(`^/wiki/api/v2/pages$`), "write:page:confluence"},
	{http.MethodPut, regexp.MustCompile(`^/wiki/api/v2/pages/[0-9]+$`), "write:page:confluence"},
	{http.MethodPost, regexp.MustCompile(`^/wiki/api/v2/footer-comments$`), "write:comment:confluence"},
	{http.MethodPost, regexp.MustCompile(`^/wiki/rest/api/content/[0-9]+/child/attachment$`), "write:confluence-file"},
}
