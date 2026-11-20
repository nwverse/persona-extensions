package main

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// A Nextcloud installed in a subfolder (the admin manual's example is https://www.example.com/nextcloud/) runs
// remote.php with the base URI OC::$WEBROOT + "/remote.php/dav/", and sabre/dav puts that base URI in front of
// every href it writes. The instance address given in settings carries the same web root.
const subfolderRoot = "/nextcloud"

func TestSearchInSubfolderInstallKeepsEveryFile(t *testing.T) {
	a := documentedNextcloudAt(t, subfolderRoot)
	files, err := a.client.search(t.Context(), testUsername, "notes")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(contractFiles) {
		t.Fatalf("got %d files, want %d", len(files), len(contractFiles))
	}
	for i, item := range files {
		if want := "/remote.php/dav/files/" + testUsername + "/" + contractFiles[i]; item.Href != want {
			t.Fatalf("href %q, want the decoded path %q without the web root", item.Href, want)
		}
	}
}

func TestSubfolderSearchResultOpensInDetailView(t *testing.T) {
	a := documentedNextcloudAt(t, subfolderRoot)
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
	for i, path := range paths {
		if path != contractFiles[i] {
			t.Fatalf("row path %q, want %q", path, contractFiles[i])
		}
		if _, err := a.page(t.Context(), r, pageState{Mode: "detail", Path: path}); err != nil {
			t.Fatalf("opening %q from search results: %v", path, err)
		}
	}
}

func TestSubfolderSearchToolListsReadablePaths(t *testing.T) {
	a := documentedNextcloudAt(t, subfolderRoot)
	out, err := a.callTool(t.Context(), "nextcloud_search", []byte(`{"query":"notes"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range contractFiles {
		if !strings.Contains(out, "/remote.php/dav/files/"+testUsername+"/"+rel+":") {
			t.Fatalf("search output does not list %q:\n%s", rel, out)
		}
	}
	text, err := a.callTool(t.Context(), "nextcloud_read_file", []byte(`{"path":"`+contractFiles[0]+`"}`))
	if err != nil || !strings.Contains(text, "# Body of "+contractFiles[0]) {
		t.Fatalf("read after search: %v %s", err, text)
	}
}
