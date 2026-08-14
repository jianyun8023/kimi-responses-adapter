package adapter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

type Server struct {
	cfg    Config
	client *http.Client
	models *modelRegistry
}

func NewServer(cfg Config) *Server {
	// No client-level timeout: streaming responses can run for minutes.
	// Cancellation propagates from the inbound request context.
	return &Server{cfg: cfg, client: &http.Client{}, models: newModelRegistry(10 * time.Minute)}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/responses", s.handleResponses)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	// Everything else (e.g. /v1/messages, /v1/chat/completions, /v1/models)
	// is proxied to the Kimi upstream byte-for-byte.
	mux.HandleFunc("/", s.handlePassthrough)
	return mux
}

// forwardAuth copies the inbound client credential (Authorization Bearer or
// x-api-key) onto an upstream request. The adapter holds no keys of its own.
func forwardAuth(inbound http.Header, outbound http.Header) {
	if v := inbound.Get("Authorization"); v != "" {
		outbound.Set("Authorization", v)
	}
	if v := inbound.Get("x-api-key"); v != "" {
		outbound.Set("x-api-key", v)
	}
}

// inboundAuthHeaders extracts just the credential headers from an inbound
// request, for reuse on auxiliary upstream calls such as GET /v1/models.
func inboundAuthHeaders(r *http.Request) http.Header {
	h := http.Header{}
	forwardAuth(r.Header, h)
	return h
}

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req ResponsesRequest
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"message": "invalid request body", "type": "invalid_request_error"},
		})
		return
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"message": "invalid request body", "type": "invalid_request_error"},
		})
		return
	}

	resolveModel := func(model string) (ModelInfo, bool) {
		s.models.ensureFresh(r.Context(), s.cfg.KimiBaseURL, inboundAuthHeaders(r))
		return s.models.lookup(model)
	}
	anthReq, err := buildAnthropicRequest(&s.cfg, &req, resolveModel)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"message": err.Error(), "type": "invalid_request_error"},
		})
		return
	}
	upBody, _ := json.Marshal(anthReq)

	upstream, err := s.newUpstreamRequest(r, http.MethodPost, "/v1/messages", bytes.NewReader(upBody))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": map[string]any{"message": err.Error(), "type": "api_error"},
		})
		return
	}
	upstream.Header.Set("Content-Type", "application/json")
	if anthReq.Stream {
		upstream.Header.Set("Accept", "text/event-stream")
	}

	resp, err := s.client.Do(upstream)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": map[string]any{"message": "upstream request failed: " + err.Error(), "type": "api_error"},
		})
		return
	}
	defer resp.Body.Close()

	log.Printf("responses model=%s upstream_model=%s max_tokens=%d stream=%v status=%d",
		req.Model, anthReq.Model, anthReq.MaxTokens, anthReq.Stream, resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		s.relayUpstreamError(w, &req, resp.StatusCode, errBody)
		return
	}

	if anthReq.Stream {
		var upstreamReader io.Reader = resp.Body
		if debugFile := os.Getenv("KIMI_DEBUG_SSE_FILE"); debugFile != "" {
			if f, ferr := os.OpenFile(debugFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); ferr == nil {
				defer f.Close()
				upstreamReader = io.TeeReader(resp.Body, f)
				log.Printf("debug: teeing upstream SSE to %s", debugFile)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		flush := func() {
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err := translateSSE(&s.cfg, &req, upstreamReader, w, flush); err != nil {
			log.Printf("stream translation error: %v", err)
		}
		log.Printf("responses model=%s done in %s", req.Model, time.Since(start).Round(time.Millisecond))
		return
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": map[string]any{"message": "failed reading upstream response", "type": "api_error"},
		})
		return
	}
	var msg AnthropicMessageObj
	if err := json.Unmarshal(respBody, &msg); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": map[string]any{"message": "invalid upstream response", "type": "api_error"},
		})
		return
	}
	writeJSON(w, http.StatusOK, anthropicToResponse(&s.cfg, &req, &msg))
}

func (s *Server) relayUpstreamError(w http.ResponseWriter, req *ResponsesRequest, status int, body []byte) {
	msg := string(body)
	var parsed struct {
		Error *AnthropicError `json:"error"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Error != nil && parsed.Error.Message != "" {
		msg = parsed.Error.Message
	}
	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		failed := map[string]any{
			"id": randID("resp_"), "object": "response", "created_at": time.Now().Unix(),
			"status": "failed", "model": req.Model, "output": []any{},
			"error": map[string]any{"code": "upstream_error", "message": msg},
		}
		payload, _ := json.Marshal(map[string]any{
			"type": "response.failed", "sequence_number": 0, "response": failed,
		})
		fmt.Fprintf(w, "event: response.failed\ndata: %s\n\n", payload)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return
	}
	writeJSON(w, status, map[string]any{
		"error": map[string]any{"message": msg, "type": "upstream_error"},
	})
}

// handlePassthrough proxies any non-Responses endpoint to the Kimi upstream
// unchanged: same method, path, query, body, and (streaming) response.
func (s *Server) handlePassthrough(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	upstream, err := s.newUpstreamRequest(r, r.Method, path, r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": map[string]any{"message": err.Error(), "type": "api_error"},
		})
		return
	}
	copyHeaders(upstream.Header, r.Header)

	resp, err := s.client.Do(upstream)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": map[string]any{"message": "upstream request failed: " + err.Error(), "type": "api_error"},
		})
		return
	}
	defer resp.Body.Close()

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	copyFlush(w, resp.Body)
	log.Printf("passthrough %s %s status=%d", r.Method, r.URL.Path, resp.StatusCode)
}

func (s *Server) newUpstreamRequest(r *http.Request, method, path string, body io.Reader) (*http.Request, error) {
	url := s.cfg.KimiBaseURL + path
	req, err := http.NewRequestWithContext(r.Context(), method, url, body)
	if err != nil {
		return nil, err
	}
	forwardAuth(r.Header, req.Header)
	if req.Header.Get("anthropic-version") == "" {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	if s.cfg.AnthropicBeta != "" {
		req.Header.Set("anthropic-beta", s.cfg.AnthropicBeta)
	}
	return req, nil
}

var hopByHop = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true,
	"Proxy-Authorization": true, "Te": true, "Trailer": true,
	"Transfer-Encoding": true, "Upgrade": true, "Content-Length": true,
}

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		if hopByHop[http.CanonicalHeaderKey(k)] {
			continue
		}
		ck := http.CanonicalHeaderKey(k)
		dst.Del(ck)
		for _, v := range vs {
			dst.Add(ck, v)
		}
	}
}

// copyFlush copies the body while flushing after every write so SSE streams
// reach the client incrementally.
func copyFlush(w io.Writer, r io.Reader) {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 16*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
