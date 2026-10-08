// Package server exposes the engine over HTTP for executors, Dex, the CLI
// and the built-in web UI.
package server

import (
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/officialmelon/betterdecompiler/internal/config"
	"github.com/officialmelon/betterdecompiler/internal/engine"
)

//go:embed web/index.html
var indexHTML []byte

// Server is the HTTP API.
type Server struct {
	Engine    *engine.Engine
	Providers []config.ProviderInfo
	Token     string
	Version   string
	Log       *log.Logger
	// AllowAnyHost disables the Host header check (needed when serving on a
	// LAN address or behind a reverse proxy).
	AllowAnyHost bool
}

const maxBody = 32 << 20

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.index)
	mux.HandleFunc("/v1/health", s.health)
	mux.HandleFunc("/v1/providers", s.auth(s.providers))
	mux.HandleFunc("/v1/clean", s.auth(s.clean))
	mux.HandleFunc("/fix_script", s.auth(s.legacy)) // BetterDecompiler v1 / Dex compatibility
	return s.guard(mux)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func isLoopbackHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// guard blocks browser-based attacks on a local server: other websites
// cannot call the API (Origin check, JSON-only bodies) and DNS rebinding is
// prevented by only answering to loopback host names.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		w = rec
		defer func() {
			if s.Log != nil && r.URL.Path != "/v1/health" {
				s.Log.Printf("%s %s %d %s%s", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond), rec.note)
			}
		}()
		if !s.AllowAnyHost && !isLoopbackHost(r.Host) {
			writeError(w, http.StatusForbidden, "host not allowed (start the server with --allow-any-host to serve other hosts)")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			o := strings.TrimPrefix(strings.TrimPrefix(origin, "http://"), "https://")
			if o != r.Host {
				writeError(w, http.StatusForbidden, "cross-origin requests are not allowed")
				return
			}
		}
		if r.Method == http.MethodPost && !strings.Contains(r.Header.Get("Content-Type"), "json") {
			writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	note   string
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Token != "" {
			got := r.Header.Get("X-BD-Token")
			if b := r.Header.Get("Authorization"); got == "" && strings.HasPrefix(b, "Bearer ") {
				got = strings.TrimPrefix(b, "Bearer ")
			}
			if subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) != 1 {
				writeError(w, http.StatusUnauthorized, "missing or invalid token")
				return
			}
		}
		h(w, r)
	}
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; frame-ancestors 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	_, _ = w.Write(indexHTML)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	mode, chain := s.Engine.Settings().Mode, s.Engine.Chain()
	if len(chain) == 1 && chain[0] == "offline" {
		mode = "offline"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "name": "BetterDecompiler", "version": s.Version,
		"mode": mode, "chain": chain, "auth": s.Token != "", "stats": s.Engine.Stats(),
	})
}

func (s *Server) providers(w http.ResponseWriter, r *http.Request) {
	var ready []config.ProviderInfo
	for _, p := range s.Providers {
		if _, ok := s.Engine.Provider(p.Name); ok {
			ready = append(ready, p)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"chain": s.Engine.Chain(), "providers": ready})
}

type cleanRequest struct {
	Source string `json:"source"`
	Script string `json:"script"` // alias used by v1 clients
	engine.Options
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "use POST")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "script too large")
		} else {
			writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		}
		return false
	}
	return true
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, engine.ErrUnknownProvider):
		return http.StatusBadRequest
	case strings.HasPrefix(err.Error(), "unknown "):
		return http.StatusBadRequest
	}
	return http.StatusBadGateway
}

func (s *Server) clean(w http.ResponseWriter, r *http.Request) {
	var req cleanRequest
	if !decode(w, r, &req) {
		return
	}
	src := req.Source
	if src == "" {
		src = req.Script
	}
	res, err := s.Engine.Clean(r.Context(), src, req.Options)
	if err != nil {
		writeError(w, statusFor(err), err.Error())
		return
	}
	if rec, ok := w.(*statusRecorder); ok {
		rec.note = fmt.Sprintf(" %s/%s cached=%t", res.Provider, res.Mode, res.Cached)
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) legacy(w http.ResponseWriter, r *http.Request) {
	var req cleanRequest
	if !decode(w, r, &req) {
		return
	}
	src := req.Script
	if src == "" {
		src = req.Source
	}
	res, err := s.Engine.Clean(r.Context(), src, req.Options)
	if err != nil {
		writeError(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"fixed_script": res.Source})
}
