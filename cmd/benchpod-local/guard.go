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
//   - A change with a body under /api must say it is JSON (or, for a firmware upload, raw bytes):
//     a browser sends a cross-site text/plain POST without asking first; application/json and
//     application/octet-stream always need a preflight, which this server never grants.
//   - A read under /api from another site is refused too (Sec-Fetch-Site cross-site or same-site,
//     which covers another localhost port): some reads start the MCP server or download its
//     installer, and a page's <img src> must not set that off.
//   - No other site may frame the app, so it cannot be clickjacked, and the page runs only its own
//     scripts (a content security policy), so an injected script has nothing to load.
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("X-Content-Type-Options", "nosniff")
		if !allowedHost(r.Host) {
			http.Error(w, "this server only answers requests addressed to localhost", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && crossSite(r) {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}
		if !safeMethod(r.Method) {
			if o := r.Header.Get("Origin"); o != "" && !strings.EqualFold(o, "http://"+r.Host) {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/api/") && hasBody(r) && !preflighted(r.Header.Get("Content-Type")) {
				http.Error(w, "send the request body as application/json", http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// contentSecurityPolicy: the app's own scripts, styles (Mantine sets inline styles), images and
// WebAssembly (the sensor engine), and connections back to this server only.
const contentSecurityPolicy = "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; " +
	"style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; " +
	"connect-src 'self' ws: wss:; worker-src 'self' blob:; object-src 'none'; base-uri 'none'; " +
	"frame-ancestors 'self'"

// crossSite says whether a browser marked the request as sent by another site. A request from the
// app's own page is same-origin, a typed URL is none, and curl sends no header.
func crossSite(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		return true
	}
	return false
}

func hasBody(r *http.Request) bool {
	return r.ContentLength > 0 || (r.ContentLength < 0 && r.Body != nil && r.Body != http.NoBody)
}

// preflighted says whether a browser must ask (CORS preflight) before sending this body type
// cross-site: JSON for the API, raw bytes for a firmware upload.
func preflighted(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	return err == nil && (mt == "application/json" || mt == "application/octet-stream")
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
