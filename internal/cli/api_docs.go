package cli

import (
	_ "embed"
	"net/http"
)

// openAPISpec is the hand-written OpenAPI 3.1 spec for /api/v1. Hand-written
// rather than generated to keep the dep surface clean (no huma, no swag,
// no codegen step). When endpoint shapes change, edit web/openapi.json
// and the matching apiHandler/*JSON struct in lockstep — there's a
// handful of endpoints, drift risk is low.
//
//go:embed web/openapi.json
var openAPISpec []byte

// docsHTML loads Swagger UI from unpkg and points it at openAPISpec.
// Browser-side CDN fetch — pier's binary stays slim. The page is
// self-explanatory: open http://<bind>:<port>/api/docs.
//
//go:embed web/docs.html
var docsHTML []byte

func (h *apiHandler) getOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(openAPISpec)
}

func (h *apiHandler) getDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(docsHTML)
}
