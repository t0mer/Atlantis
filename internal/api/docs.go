package api

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.yaml
var spec []byte

func openAPISpec(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	w.Write(spec)
}

// docsPage is Swagger UI loaded from a pinned CDN build, pointed at the
// embedded spec.
func docsPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(`<!doctype html><html><head><meta charset="utf-8"><title>Atlantis API</title>
<link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5.17.14/swagger-ui.css"></head>
<body><div id="ui"></div><script src="https://unpkg.com/swagger-ui-dist@5.17.14/swagger-ui-bundle.js"></script>
<script>SwaggerUIBundle({url: "/api/openapi.yaml", dom_id: "#ui"});</script></body></html>`))
}
