package recordings

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func send(t *testing.T, method, url, body string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func TestBookmarkLifecycle(t *testing.T) {
	srv, _, _ := setup(t)
	resp, body := send(t, "POST", srv.URL+"/api/bookmarks", `{"name":"  Go-around  ","path":"2026/11/18/essl_20261118_16.mp3","offset":90.5}`)
	if resp.StatusCode != http.StatusCreated || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("add: %d %s", resp.StatusCode, body)
	}
	var added BookmarkView
	if err := json.Unmarshal(body, &added); err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 18, 16, 1, 30, 500e6, time.UTC)
	if added.Name != "Go-around" || !added.At.Equal(want) || added.Recording == nil || resp.Header.Get("Location") == "" {
		t.Fatalf("added %+v", added)
	}

	var one BookmarkView
	get(t, srv.URL+resp.Header.Get("Location"), &one)
	if one.ID != added.ID || one.Recording == nil || one.Recording.Path != "2026/11/18/essl_20261118_16.mp3" {
		t.Fatalf("get %+v", one)
	}

	if resp, body := send(t, "PATCH", srv.URL+resp.Header.Get("Location"), `{"name":"Missed approach"}`); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("rename: %d %s", resp.StatusCode, body)
	}
	var list []BookmarkView
	get(t, srv.URL+"/api/bookmarks", &list)
	if len(list) != 1 || list[0].Name != "Missed approach" {
		t.Fatalf("list %+v", list)
	}

	if resp, _ := send(t, "DELETE", srv.URL+resp.Header.Get("Location"), ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if resp := get(t, srv.URL+"/api/bookmarks/"+itoa(added.ID), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted bookmark still there: %d", resp.StatusCode)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestBookmarkInGrowingHourSurvivesRename(t *testing.T) {
	srv, dir, _ := setup(t)
	resp, body := send(t, "POST", srv.URL+"/api/bookmarks", `{"name":"now","path":"2026/11/18/essl_20261118_17.mp3.tmp","offset":1}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add: %d %s", resp.StatusCode, body)
	}
	var b BookmarkView
	json.Unmarshal(body, &b)
	if b.Path != "2026/11/18/essl_20261118_17.mp3" || b.Recording == nil || !b.Recording.Growing {
		t.Fatalf("stored %+v", b)
	}
	// the hour ends and rtl_airband renames the file
	tmp := filepath.Join(dir, "2026/11/18/essl_20261118_17.mp3.tmp")
	if err := os.Rename(tmp, strings.TrimSuffix(tmp, ".tmp")); err != nil {
		t.Fatal(err)
	}
	var one BookmarkView
	get(t, srv.URL+"/api/bookmarks/"+itoa(b.ID), &one)
	if one.Recording == nil || one.Recording.Path != "2026/11/18/essl_20261118_17.mp3" || one.Recording.Growing {
		t.Fatalf("after rename %+v", one.Recording)
	}
}

func TestBookmarkOfDeletedRecordingHasNoRecording(t *testing.T) {
	srv, dir, _ := setup(t)
	_, body := send(t, "POST", srv.URL+"/api/bookmarks", `{"name":"old","path":"2026/11/17/essl_20261117_23.mp3","offset":0}`)
	var b BookmarkView
	json.Unmarshal(body, &b)
	os.Remove(filepath.Join(dir, "2026/11/17/essl_20261117_23.mp3"))
	var one BookmarkView
	get(t, srv.URL+"/api/bookmarks/"+itoa(b.ID), &one)
	if one.ID != b.ID || one.Recording != nil {
		t.Fatalf("got %+v", one)
	}
}

func TestBookmarkInputIsChecked(t *testing.T) {
	srv, _, _ := setup(t)
	long := strings.Repeat("x", 101)
	for _, body := range []string{
		`not json`,
		`{"name":"","path":"2026/11/18/essl_20261118_16.mp3","offset":1}`,
		`{"name":"` + long + `","path":"2026/11/18/essl_20261118_16.mp3","offset":1}`,
		`{"name":"x","path":"2026/11/18/essl_20261118_16.mp3","offset":-1}`,
		`{"name":"x","path":"../secret.mp3","offset":1}`,
		`{"name":"x","path":"2026/11/18/missing_20261118_01.mp3","offset":1}`,
		`{"name":"x","path":"2026/11/18/essl_20261118_16.mp3","offset":1,"pad":"` + strings.Repeat("y", 5000) + `"}`,
	} {
		if resp, _ := send(t, "POST", srv.URL+"/api/bookmarks", body); resp.StatusCode < 400 || resp.StatusCode >= 500 {
			t.Errorf("%.60s: got %d", body, resp.StatusCode)
		}
	}
	for _, path := range []string{"/api/bookmarks/abc", "/api/bookmarks/999"} {
		if resp, _ := send(t, "DELETE", srv.URL+path, ""); resp.StatusCode != http.StatusNotFound {
			t.Errorf("DELETE %s: %d", path, resp.StatusCode)
		}
	}
	var list []BookmarkView
	get(t, srv.URL+"/api/bookmarks", &list)
	if list == nil || len(list) != 0 {
		t.Fatalf("an empty list must be [], got %+v", list)
	}
}
