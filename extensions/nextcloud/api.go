package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	errToken       = errors.New("Nextcloud rejected the username or app password; replace them in extension settings")
	errUnavailable = errors.New("Nextcloud could not be reached; try again later")
	errNotFound    = errors.New("Nextcloud could not find that file")
	errRateLimited = errors.New("Nextcloud rate limit reached; try again later")
)

type client struct {
	base, username, password string
	http                     *http.Client
}

func newClient(base, username, password string) *client {
	return &client{base: strings.TrimRight(base, "/"), username: strings.TrimSpace(username), password: strings.TrimSpace(password), http: &http.Client{
		Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *client) do(ctx context.Context, method, path, contentType string, body io.Reader, out any) (http.Header, error) {
	if c.username == "" || c.password == "" {
		return nil, errors.New("Nextcloud username and app password are required")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Accept", "application/xml, text/plain, text/markdown, text/plain; charset=utf-8")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if method == http.MethodPut {
		req.Header.Set("If-None-Match", "*")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errUnavailable, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20+1))
	if err != nil {
		return nil, errUnavailable
	}
	if len(data) > 1<<20 {
		return nil, errors.New("Nextcloud returned an oversized response")
	}
	switch res.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusMultiStatus, http.StatusNoContent:
		if out != nil {
			switch target := out.(type) {
			case *multistatus:
				if err := xml.Unmarshal(data, target); err != nil {
					return nil, errors.New("Nextcloud returned invalid WebDAV XML")
				}
			case *[]byte:
				*target = data
			default:
				return nil, errors.New("unsupported Nextcloud response target")
			}
		}
		return res.Header, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, errToken
	case http.StatusNotFound:
		return nil, errNotFound
	case http.StatusPreconditionFailed, http.StatusConflict:
		return nil, errors.New("a file already exists at this path; choose a new note name")
	case http.StatusTooManyRequests:
		return nil, errRateLimited
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return nil, errors.New("Nextcloud rejected the operation; check the path, file type and permissions")
	default:
		return nil, fmt.Errorf("Nextcloud returned HTTP %d", res.StatusCode)
	}
}

// webroot is the decoded path of the instance address, empty when Nextcloud answers at the host root.
func (c *client) webroot() string {
	u, err := url.Parse(c.base)
	if err != nil {
		return ""
	}
	return strings.TrimRight(u.Path, "/")
}

const maxQueryChars = 100

func (c *client) search(ctx context.Context, username, query string) ([]fileEntry, error) {
	if username == "" || strings.ContainsAny(username, "/\\\x00\r\n") || len(username) > 128 {
		return nil, errors.New("invalid Nextcloud username")
	}
	query = strings.TrimSpace(query)
	if query == "" || len([]rune(query)) > maxQueryChars || strings.ContainsAny(query, "%\x00\r\n") {
		return nil, errors.New("filename search must be at most 100 characters without wildcards")
	}
	scope := "/files/" + username
	body := searchXML(scope, query)
	var out multistatus
	_, err := c.do(ctx, "SEARCH", "/remote.php/dav/", "text/xml; charset=utf-8", bytes.NewReader(body), &out)
	if err != nil {
		return nil, err
	}
	prefix := "/remote.php/dav/files/" + username + "/"
	webroot := c.webroot()
	var files []fileEntry
	for _, item := range out.Responses {
		// WebDAV hrefs are percent-encoded; keep the decoded path so it can be passed back to file().
		href, err := url.PathUnescape(item.Href)
		if err != nil || item.IsCollection {
			continue
		}
		// An instance installed in a subfolder writes its web root in front of every href.
		href = strings.TrimPrefix(href, webroot)
		if !strings.HasPrefix(href, prefix) {
			continue
		}
		if validRemotePath(strings.TrimPrefix(href, prefix)) {
			item.Href = href
			files = append(files, item)
		}
	}
	return files, nil
}

func (c *client) file(ctx context.Context, username, remotePath string) (fileEntry, []byte, error) {
	if username == "" || strings.ContainsAny(username, "/\\\x00\r\n") || len(username) > 128 || !validRemotePath(remotePath) {
		return fileEntry{}, nil, errors.New("invalid Nextcloud file path")
	}
	escaped := escapePath(remotePath)
	var metadata multistatus
	propfind := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:prop><d:displayname/><d:getcontenttype/><d:getcontentlength/><d:getlastmodified/><d:getetag/><oc:fileid/><d:resourcetype/></d:prop></d:propfind>`
	if _, err := c.do(ctx, "PROPFIND", "/remote.php/dav/files/"+username+"/"+escaped, "application/xml; charset=utf-8", strings.NewReader(propfind), &metadata); err != nil {
		return fileEntry{}, nil, err
	}
	if len(metadata.Responses) == 0 || metadata.Responses[0].IsCollection {
		return fileEntry{}, nil, errors.New("Nextcloud did not return the requested file")
	}
	item := metadata.Responses[0]
	if item.ContentLength > 100000 {
		return item, nil, errors.New("text file is too large to read in Persona; open it in Nextcloud instead")
	}
	if !isTextType(item.ContentType) {
		return item, nil, errors.New("only plain text and Markdown files can be read in Persona")
	}
	var text []byte
	if _, err := c.do(ctx, http.MethodGet, "/remote.php/dav/files/"+username+"/"+escaped, "", nil, &text); err != nil {
		return item, nil, err
	}
	return item, text, nil
}

func (c *client) createNote(ctx context.Context, username, remotePath string, text []byte) error {
	if username == "" || strings.ContainsAny(username, "/\\\x00\r\n") || len(username) > 128 || !validRemotePath(remotePath) {
		return errors.New("invalid Nextcloud note path")
	}
	_, err := c.do(ctx, http.MethodPut, "/remote.php/dav/files/"+username+"/"+escapePath(remotePath), "text/markdown; charset=utf-8", bytes.NewReader(text), nil)
	return err
}

func validRemotePath(remotePath string) bool {
	if remotePath == "" || len(remotePath) > 500 || strings.HasPrefix(remotePath, "/") || strings.ContainsAny(remotePath, "\\\x00\r\n?#") {
		return false
	}
	lower := strings.ToLower(remotePath)
	if strings.Contains(lower, "%2e") || strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") {
		return false
	}
	for _, part := range strings.Split(remotePath, "/") {
		if part == "" || part == "." || part == ".." || strings.TrimSpace(part) != part {
			return false
		}
	}
	return true
}

func escapePath(remotePath string) string {
	parts := strings.Split(remotePath, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func isTextType(contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	return contentType == "text/plain" || contentType == "text/markdown" || contentType == "text/x-markdown" || contentType == "application/json" || contentType == "text/csv"
}

func clipText(text string, limit int) string {
	r := []rune(text)
	if len(r) > limit {
		return string(r[:max(0, limit-1)]) + "…"
	}
	return text
}
