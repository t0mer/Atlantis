package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServesIndexAndFallsBack(t *testing.T) {
	for _, p := range []string{"/", "/clients/aa:bb", "/nodes"} {
		rr := httptest.NewRecorder()
		Handler().ServeHTTP(rr, httptest.NewRequest("GET", p, nil))
		if rr.Code != 200 || !strings.Contains(rr.Body.String(), "Atlantis") {
			t.Fatalf("%s: %d", p, rr.Code)
		}
	}
}
