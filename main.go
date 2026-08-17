// Command task052-flagger runs the in-process feature flag evaluation service.
//
// Use --smoke-test to run the built-in self-check, which exits the process on
// completion. Without flags it starts an HTTP server on the address given by
// the --addr flag (default ":8080").
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

	"task052-flagger/internal/flagger"
	"task052-flagger/internal/registry"
	"task052-flagger/internal/selfcheck"
)

func main() {
	smoke := flag.Bool("smoke-test", false, "run the built-in self-check and exit")
	addr := flag.String("addr", ":8080", "HTTP listen address")
	flag.Parse()

	if *smoke {
		if err := selfcheck.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "smoke-test FAILED: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("smoke-test PASSED")
		return
	}

	srv := newServer()
	mux := srv.routes()
	log.Printf("flagger service listening on %s", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

// server wires the registry to HTTP handlers.
type server struct {
	reg *registry.Registry
}

func newServer() *server { return &server{reg: registry.New()} }

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/flags", s.handleFlagsCollection)
	mux.HandleFunc("/flags/", s.handleFlagItem)
	mux.HandleFunc("/stats", s.handleStats)
	return mux
}

func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// POST /flags        — register/replace a flag (body = full flag config)
// GET  /flags        — list all flags, sorted by key
func (s *server) handleFlagsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.registerFlag(w, r)
	case http.MethodGet:
		s.listFlags(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, errMsg("method not allowed"))
	}
}

func (s *server) registerFlag(w http.ResponseWriter, r *http.Request) {
	var f flagger.Flag
	if err := decodeJSON(r, &f); err != nil {
		writeJSON(w, http.StatusBadRequest, errMsg(err.Error()))
		return
	}
	if err := s.reg.Register(f); err != nil {
		writeJSON(w, http.StatusBadRequest, errMsg(err.Error()))
		return
	}
	// Re-read the stored copy so the response reflects the canonical config.
	got, _ := s.reg.Get(f.Key)
	writeJSON(w, http.StatusOK, got)
}

func (s *server) listFlags(w http.ResponseWriter, r *http.Request) {
	flags := s.reg.List()
	writeJSON(w, http.StatusOK, map[string]any{"flags": flags})
}

// GET    /flags/{key}           — fetch one flag config
// DELETE /flags/{key}           — delete one flag
// POST   /flags/{key}/evaluate  — evaluate a flag against a context
func (s *server) handleFlagItem(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/flags/")
	// Split into key and optional "/evaluate" suffix.
	var key, rest string
	if i := strings.Index(path, "/"); i >= 0 {
		key, rest = path[:i], path[i+1:]
	} else {
		key = path
	}
	if key == "" {
		writeJSON(w, http.StatusBadRequest, errMsg("missing flag key"))
		return
	}
	switch {
	case rest == "" && r.Method == http.MethodGet:
		s.getFlag(w, r, key)
	case rest == "" && r.Method == http.MethodDelete:
		s.deleteFlag(w, r, key)
	case rest == "evaluate" && r.Method == http.MethodPost:
		s.evaluateFlag(w, r, key)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, errMsg("method not allowed"))
	}
}

func (s *server) getFlag(w http.ResponseWriter, r *http.Request, key string) {
	f, ok := s.reg.Get(key)
	if !ok {
		writeJSON(w, http.StatusNotFound, errMsg("flag not found"))
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *server) deleteFlag(w http.ResponseWriter, r *http.Request, key string) {
	if err := s.reg.Delete(key); err != nil {
		writeJSON(w, http.StatusNotFound, errMsg(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": key})
}

// evalRequest is the body for POST /flags/{key}/evaluate.
type evalRequest struct {
	Attributes map[string]any `json:"attributes"`
}

// evalResponse is the JSON view of an evaluation result.
type evalResponse struct {
	Key     string `json:"key"`
	Value   any    `json:"value"`
	Reason  string `json:"reason"`
	Matched bool   `json:"matched"`
	Bucket  *int   `json:"bucket"`
}

func (s *server) evaluateFlag(w http.ResponseWriter, r *http.Request, key string) {
	var req evalRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errMsg(err.Error()))
		return
	}
	res, err := s.reg.Evaluate(key, flagger.Context{Attributes: req.Attributes})
	if err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, errMsg("flag not found"))
			return
		}
		writeJSON(w, http.StatusBadRequest, errMsg(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, evalResponse{
		Key:     key,
		Value:   res.Value,
		Reason:  res.Reason,
		Matched: res.Matched,
		Bucket:  res.Bucket,
	})
}

func (s *server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errMsg("method not allowed"))
		return
	}
	writeJSON(w, http.StatusOK, s.reg.Stats())
}

// --- HTTP helpers ---

var bufPool = sync.Pool{New: func() any { b := make([]byte, 0, 4096); return &b }}

func decodeJSON(r *http.Request, dst any) error {
	buf := bufPool.Get().(*[]byte)
	defer func() { *buf = (*buf)[:0]; bufPool.Put(buf) }()
	const maxBody = 4 << 20 // 4 MiB
	n, err := io.CopyBuffer(newBufAppender(buf), r.Body, nil)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if n > maxBody {
		return fmt.Errorf("request body too large: %d bytes (max %d)", n, maxBody)
	}
	if len(*buf) == 0 {
		return fmt.Errorf("empty request body")
	}
	if err := json.Unmarshal(*buf, dst); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

// bufAppender wraps a *[]byte as an io.Writer so decodeJSON can reuse a pooled
// buffer without allocating per request.
type bufAppender struct{ b *[]byte }

func newBufAppender(b *[]byte) *bufAppender { return &bufAppender{b: b} }
func (a *bufAppender) Write(p []byte) (int, error) {
	*a.b = append(*a.b, p...)
	return len(p), nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func errMsg(msg string) map[string]any { return map[string]any{"error": msg} }
