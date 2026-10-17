package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

type listing struct {
	Path    string `json:"path"`
	Parent  string `json:"parent"`
	Entries []struct {
		Name string `json:"name"`
		Dir  bool   `json:"dir"`
		Size int64  `json:"size"`
	} `json:"entries"`
}

func list(t *testing.T, srv *Server, h http.Handler, query string) (int, listing) {
	t.Helper()
	rec := mediaRequest(srv, h, http.MethodGet, "/api/files?"+query, "")
	var l listing
	if rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil {
			t.Fatalf("body %s: %v", rec.Body.String(), err)
		}
	}
	return rec.Code, l
}

func TestFileBrowser(t *testing.T) {
	srv, h := mediaServer(t, &fakeMediaConsole{})
	dir := t.TempDir()
	for _, name := range []string{"b.iso", "a.IMG", "notes.txt", ".hidden.iso"} {
		_ = os.WriteFile(filepath.Join(dir, name), make([]byte, 10), 0o600)
	}
	for _, name := range []string{"Zeta", "alpha", ".git"} {
		_ = os.Mkdir(filepath.Join(dir, name), 0o700)
	}
	_ = os.Symlink(filepath.Join(dir, "b.iso"), filepath.Join(dir, "link.iso"))

	code, l := list(t, srv, h, "path="+url.QueryEscape(dir)+"&mode=file")
	if code != 200 {
		t.Fatalf("HTTP %d", code)
	}
	var got []string
	for _, e := range l.Entries {
		got = append(got, e.Name)
	}
	// Folders first, then images, each sorted case-insensitively; no hidden entries, no other files.
	want := []string{"alpha", "Zeta", "a.IMG", "b.iso", "link.iso"}
	if len(got) != len(want) {
		t.Fatalf("entries %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entries %v, want %v", got, want)
		}
	}
	if l.Path != dir || l.Parent != filepath.Dir(dir) || !l.Entries[0].Dir || l.Entries[3].Size != 10 {
		t.Errorf("listing %+v", l)
	}

	if _, l := list(t, srv, h, "path="+url.QueryEscape(dir)+"&mode=dir"); len(l.Entries) != 2 {
		t.Errorf("dir mode lists %d entries, want only the 2 folders", len(l.Entries))
	}
	home, _ := os.UserHomeDir()
	if code, l := list(t, srv, h, "mode=file"); code != 200 || l.Path != home {
		t.Errorf("default listing: HTTP %d path %q, want the home folder", code, l.Path)
	}
	for _, q := range []string{"path=relative&mode=file", "path=" + url.QueryEscape(filepath.Join(dir, "b.iso")), "path=" + url.QueryEscape(filepath.Join(dir, "missing"))} {
		if code, _ := list(t, srv, h, q); code != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d, want 400", q, code)
		}
	}
}

func TestFileBrowserRequiresToken(t *testing.T) {
	_, h := mediaServer(t, &fakeMediaConsole{})
	r, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/api/files?mode=file", nil)
	r.Host = "127.0.0.1"
	rec := newRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("HTTP %d, want 401", rec.Code)
	}
}
