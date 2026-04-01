package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler_ServesIndexHTML(t *testing.T) {
	handler := Handler()

	// chi.Mount strips the prefix, so "/" arrives at the handler.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET / returned %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<title>msgvault</title>") {
		t.Fatal("expected index.html content with <title>msgvault</title>")
	}
}

func TestHandler_ServesCSS(t *testing.T) {
	handler := Handler()

	req := httptest.NewRequest(http.MethodGet, "/style.css", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /style.css returned %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "--bg:") {
		t.Fatal("expected CSS with --bg variable")
	}
}

func TestHandler_ServesJS(t *testing.T) {
	handler := Handler()

	req := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /app.js returned %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "apiFetch") {
		t.Fatal("expected JS with apiFetch function")
	}
}
