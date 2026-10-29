package main

import (
	"mime"
	"net"
	"net/http"
	"strings"
)

// guard is what makes a server with no sign-in safe to run (the same rules as the EMI
// Analyzer's local app).
//
// Listening on loopback keeps the network out, but not the web: any page open in the user's
// browser can send requests to 127.0.0.1. These checks close that.
//
//   - Host must be a loopback name. A site that points its own domain at 127.0.0.1 (DNS
//     rebinding) sends its own name as Host and is refused, so it can never read a response.
//   - A request that changes something must come from this origin exactly, or carry no Origin
//     at all (curl). Otherwise a page on another localhost port could drive the pod.
//   - A change with a body under /api must say it is JSON: a browser sends a cross-site
//     text/plain POST without asking first; application/json always needs a preflight, which
//     this server never grants.
//   - No other site may frame the app, so it cannot be clickjacked.
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "frame-ancestors 'self'")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("X-Content-Type-Options", "nosniff")
		if !allowedHost(r.Host) {
			http.Error(w, "this server only answers requests addressed to localhost", http.StatusForbidden)
			return
		}
		if !safeMethod(r.Method) {
			if o := r.Header.Get("Origin"); o != "" && !strings.EqualFold(o, "http://"+r.Host) {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/api/") && hasBody(r) && !isJSON(r.Header.Get("Content-Type")) {
				http.Error(w, "send the request body as application/json", http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func hasBody(r *http.Request) bool {
	return r.ContentLength > 0 || (r.ContentLength < 0 && r.Body != nil && r.Body != http.NoBody)
}

func isJSON(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	return err == nil && mt == "application/json"
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func allowedHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(strings.ToLower(host), "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
