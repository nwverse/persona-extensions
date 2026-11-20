package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// contractFiles are stored under the user's files root. Their names contain a space, non-ASCII text and a
// literal percent sign, which a real Nextcloud server reports percent-encoded in every <d:href>.
var contractFiles = []string{"My Notes/读书 notes.md", "50% done notes.md"}

// sabreEncodePath mirrors \Sabre\HTTP\encodePath, which sabre/dav applies to every href it writes into a
// multistatus body (Sabre\DAV\Xml\Element\Response): everything except A-Za-z0-9_-.~()/:@ becomes %xx.
func sabreEncodePath(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("_-.~()/:@", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02x", c)
		}
	}
	return b.String()
}

func davMultistatus(files []string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:s="http://sabredav.org/ns" xmlns:oc="http://owncloud.org/ns" xmlns:nc="http://nextcloud.org/ns">`)
	for i, rel := range files {
		name := rel[strings.LastIndex(rel, "/")+1:]
		href := sabreEncodePath("/remote.php/dav/files/" + testUsername + "/" + rel)
		fmt.Fprintf(&b, `<d:response><d:href>%s</d:href><d:propstat><d:prop><d:getlastmodified>Fri, 20 Nov 2026 10:00:00 GMT</d:getlastmodified><d:getcontentlength>12</d:getcontentlength><d:resourcetype/><d:getetag>&quot;etag%d&quot;</d:getetag><d:getcontenttype>text/markdown</d:getcontenttype><oc:fileid>%d</oc:fileid><d:displayname>%s</d:displayname></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`, href, i, 40+i, name)
	}
	b.WriteString(`</d:multistatus>`)
	return b.String()
}

// documentedNextcloud serves WebDAV the way Nextcloud does: hrefs in SEARCH and PROPFIND responses are
// percent-encoded, and the file is only found when the request path encodes the real name exactly once.
func documentedNextcloud(t *testing.T) *app {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if username, password, ok := r.BasicAuth(); !ok || username != testUsername || password != testPassword {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		const root = "/remote.php/dav/files/" + testUsername + "/"
		rel := strings.TrimPrefix(r.URL.Path, root)
		known := strings.HasPrefix(r.URL.Path, root)
		if known {
			known = false
			for _, file := range contractFiles {
				known = known || file == rel
			}
		}
		switch {
		case r.Method == "SEARCH" && r.URL.Path == "/remote.php/dav/":
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = w.Write([]byte(davMultistatus(contractFiles)))
		case r.Method == "PROPFIND" && known:
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = w.Write([]byte(davMultistatus([]string{rel})))
		case r.Method == http.MethodGet && known:
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte("# Body of " + rel))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return &app{client: newClient(srv.URL, testUsername, testPassword), receipts: newWriteStore("")}
}

func TestSearchReturnsDecodedPaths(t *testing.T) {
	a := documentedNextcloud(t)
	files, err := a.client.search(t.Context(), testUsername, "notes")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(contractFiles) {
		t.Fatalf("got %d files, want %d", len(files), len(contractFiles))
	}
	for i, item := range files {
		if want := "/remote.php/dav/files/" + testUsername + "/" + contractFiles[i]; item.Href != want {
			t.Fatalf("href %q, want the decoded path %q", item.Href, want)
		}
	}
}

func TestSearchResultOpensInDetailView(t *testing.T) {
	a := documentedNextcloud(t)
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}}
	list, err := a.page(t.Context(), r, pageState{Query: "notes"})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, section := range list.Sections {
		for _, item := range section.Items {
			if item.Action != nil && item.Action.ID == "detail" {
				paths = append(paths, item.Action.Values["path"])
			}
		}
	}
	if len(paths) != len(contractFiles) {
		t.Fatalf("got %d rows, want %d", len(paths), len(contractFiles))
	}
	for _, path := range paths {
		if _, err := a.page(t.Context(), r, pageState{Mode: "detail", Path: path}); err != nil {
			t.Fatalf("opening %q from search results: %v", path, err)
		}
	}
}

func TestReadFileByNameFromSearchOutput(t *testing.T) {
	a := documentedNextcloud(t)
	for _, rel := range contractFiles {
		text, err := a.callTool(t.Context(), "nextcloud_read_file", []byte(`{"path":"`+rel+`"}`))
		if err != nil || !strings.Contains(text, "# Body of "+rel) {
			t.Fatalf("%q: %v %s", rel, err, text)
		}
	}
	out, err := a.callTool(t.Context(), "nextcloud_search", []byte(`{"query":"notes"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range contractFiles {
		if !strings.Contains(out, "/remote.php/dav/files/"+testUsername+"/"+rel+":") {
			t.Fatalf("search output does not list the readable path %q:\n%s", rel, out)
		}
	}
}
