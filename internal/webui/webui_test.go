package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const indexPage = `<!doctype html><div id="root"></div>`

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	h, err := newHandler(fstest.MapFS{
		"index.html":          {Data: []byte(indexPage)},
		"favicon.svg":         {Data: []byte("<svg/>")},
		"assets/index-3f.js":  {Data: []byte("console.log(1)")},
		"assets/index-3f.css": {Data: []byte("body{}")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func get(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, target, nil))
	return w
}

func TestDashboardRoutesGetThePageAndAlwaysRevalidate(t *testing.T) {
	h := testHandler(t)
	for _, target := range []string{"/", "/hosts/a/stacks/web", "/secrets", "/assets"} {
		w := get(t, h, http.MethodGet, target)
		if w.Code != http.StatusOK || w.Body.String() != indexPage {
			t.Fatalf("%s: got %d %q, want the page", target, w.Code, w.Body.String())
		}
		if got := w.Header().Get("Cache-Control"); got != "no-cache" {
			t.Fatalf("%s: Cache-Control %q, want no-cache", target, got)
		}
		if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
			t.Fatalf("%s: Content-Type %q, want text/html", target, got)
		}
	}
}

func TestHashedAssetsAreCachedForeverAndOtherFilesAreNot(t *testing.T) {
	h := testHandler(t)
	w := get(t, h, http.MethodGet, "/assets/index-3f.js")
	if w.Code != http.StatusOK || w.Body.String() != "console.log(1)" {
		t.Fatalf("asset: got %d %q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != assetCache {
		t.Fatalf("asset Cache-Control %q, want %q", got, assetCache)
	}
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/javascript") {
		t.Fatalf("asset Content-Type %q, want text/javascript", got)
	}

	w = get(t, h, http.MethodGet, "/favicon.svg")
	if w.Code != http.StatusOK || w.Body.String() != "<svg/>" {
		t.Fatalf("favicon: got %d %q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "" {
		t.Fatalf("favicon Cache-Control %q, want none", got)
	}
}

func TestAMissingAssetIsNotFoundInsteadOfThePage(t *testing.T) {
	w := get(t, testHandler(t), http.MethodGet, "/assets/index-old.js")
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d %q, want 404", w.Code, w.Body.String())
	}
}

func TestHeadWorksAndWritesAreRefused(t *testing.T) {
	h := testHandler(t)
	if w := get(t, h, http.MethodHead, "/"); w.Code != http.StatusOK {
		t.Fatalf("HEAD: got %d", w.Code)
	}
	w := get(t, h, http.MethodPost, "/")
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST: got %d, Allow %q", w.Code, w.Header().Get("Allow"))
	}
}

func TestPathsCannotEscapeTheBuild(t *testing.T) {
	// The server's ServeMux cleans such paths before they get here; called
	// directly, the handler refuses them outright.
	w := get(t, testHandler(t), http.MethodGet, "/assets/../../../etc/passwd")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d %q, want 400", w.Code, w.Body.String())
	}
}

func TestABuildWithoutAPageIsRefused(t *testing.T) {
	_, err := newHandler(fstest.MapFS{"assets/index-3f.js": {Data: []byte("x")}})
	if err == nil || !strings.Contains(err.Error(), "pnpm build") {
		t.Fatalf("got %v, want an error that says to run pnpm build", err)
	}
}

func TestTheBuiltInDashboardHasAPage(t *testing.T) {
	h, err := Handler()
	if err != nil {
		t.Fatal(err)
	}
	w := get(t, h, http.MethodGet, "/")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `id="root"`) {
		t.Fatalf("got %d, want the built dashboard page", w.Code)
	}
}
