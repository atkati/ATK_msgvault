package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler_ServesIndexHTML(t *testing.T) {
	handler := Handler()

	// /web/ triggers a redirect to / internally via FileServer, so
	// test with /web (no trailing slash) which maps to /index.html.
	req := httptest.NewRequest(http.MethodGet, "/web", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /web returned %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<title>msgvault</title>") {
		t.Fatal("expected index.html content with <title>msgvault</title>")
	}
}

func TestHandler_ServesCSS(t *testing.T) {
	handler := Handler()

	req := httptest.NewRequest(http.MethodGet, "/web/style.css", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /web/style.css returned %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "--bg:") {
		t.Fatal("expected CSS with --bg variable")
	}
}

func TestHandler_ServesJS(t *testing.T) {
	handler := Handler()

	req := httptest.NewRequest(http.MethodGet, "/web/app.js", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /web/app.js returned %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "apiFetch") {
		t.Fatal("expected JS with apiFetch function")
	}
}
