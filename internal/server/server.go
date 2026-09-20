// Package server serves the dependency-free web UI and its JSON API.
package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/wh-chromium/codespace-status/internal/config"
	"github.com/wh-chromium/codespace-status/internal/ghcli"
	"github.com/wh-chromium/codespace-status/internal/monitor"
)

//go:embed static
var staticFS embed.FS

// Server exposes a monitor over HTTP.
type Server struct {
	mon *monitor.Monitor
}

// New builds a server for a monitor.
func New(mon *monitor.Monitor) *Server { return &Server{mon: mon} }

// Handler returns the HTTP routes of the web UI.
func (s *Server) Handler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(sub))))
	mux.HandleFunc("/", s.index(sub))
	mux.HandleFunc("/api/status", s.status)
	mux.HandleFunc("/api/select", s.selectCodespace)
	mux.HandleFunc("/api/deselect", s.deselect)
	mux.HandleFunc("/api/sync", s.sync)
	mux.HandleFunc("/api/poll", s.poll)
	mux.HandleFunc("/api/theme", s.theme)
	mux.HandleFunc("/api/permissions", s.permissions)
	mux.HandleFunc("/api/auth", s.auth)
	return mux
}

// Listen starts the server on port, returning the bound address.
func (s *Server) Listen(ctx context.Context, port int) (string, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return "", err
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Println("server error:", err)
		}
	}()
	return ln.Addr().String(), nil
}

// index serves the single page, falling through to 404 for unknown paths.
func (s *Server) index(sub fs.FS) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		data, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(data)
	}
}

// status returns the full monitor state.
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mon.Status())
}

// selectCodespace activates the codespace named by the query parameter.
func (s *Server) selectCodespace(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if err := s.mon.Select(name); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"selected": name})
}

// deselect clears the active codespace.
func (s *Server) deselect(w http.ResponseWriter, r *http.Request) {
	if err := s.mon.Deselect(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"selected": ""})
}

// sync refreshes the codespace list from gh.
func (s *Server) sync(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := s.mon.Sync(ctx); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// poll changes the sampling interval.
func (s *Server) poll(w http.ResponseWriter, r *http.Request) {
	ms, err := strconv.Atoi(r.URL.Query().Get("ms"))
	if err != nil || ms < config.MinPollMS || ms > config.MaxPollMS {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ms must be between 500 and 60000"})
		return
	}
	if err := s.mon.SetPollMS(ms); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"poll_ms": ms})
}

// theme stores the preferred colour scheme.
func (s *Server) theme(w http.ResponseWriter, r *http.Request) {
	theme := r.URL.Query().Get("value")
	if err := s.mon.SetTheme(theme); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"theme": s.mon.Config().Theme})
}

// permissions reports whether gh can reach codespaces.
func (s *Server) permissions(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.mon.Client().Permissions(ctx))
}

// auth starts the gh device-code login and returns its instructions.
func (s *Server) auth(w http.ResponseWriter, r *http.Request) {
	out, err := StartAuth(r.Context())
	resp := map[string]string{"command": ghcli.LoginCommand, "output": out}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// writeJSON renders v as a JSON response.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
