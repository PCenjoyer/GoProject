package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesConsoleAndAssets(t *testing.T) {
	handler := Handler()
	for _, path := range []string{"/", "/assets/styles.css", "/assets/app.js"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()

		handler.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want %d", path, response.Code, http.StatusOK)
		}
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if len(body) == 0 {
			t.Fatalf("%s returned an empty body", path)
		}
	}
}

func TestHandlerDoesNotExposeUnknownFiles(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/go.mod", nil)
	response := httptest.NewRecorder()

	Handler().ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
	if strings.Contains(response.Body.String(), "module ") {
		t.Fatal("unexpected file content in response")
	}
}
