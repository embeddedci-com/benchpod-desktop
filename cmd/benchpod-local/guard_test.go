package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGuardRefusesCrossSiteReads(t *testing.T) {
	h := guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	cases := []struct {
		site string
		path string
		want int
	}{
		{"cross-site", "/api/chat/tools", http.StatusForbidden},
		{"same-site", "/api/capture/la", http.StatusForbidden},
		{"same-origin", "/api/chat/tools", http.StatusOK},
		{"none", "/api/chat/tools", http.StatusOK},
		{"", "/api/chat/tools", http.StatusOK},
		{"cross-site", "/index.html", http.StatusOK},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "http://127.0.0.1:7466"+c.path, nil)
		if c.site != "" {
			r.Header.Set("Sec-Fetch-Site", c.site)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("GET %s with Sec-Fetch-Site %q = %d, want %d", c.path, c.site, w.Code, c.want)
		}
		if w.Header().Get("Content-Security-Policy") != contentSecurityPolicy {
			t.Errorf("no content security policy on %s", c.path)
		}
	}
}
