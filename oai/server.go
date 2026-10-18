// Package oai exposes milou's driver fleet behind an OpenAI-compatible
// /v1/chat/completions endpoint. Any client that can set a model string can
// pick a harness: "milou/pi", "milou/mojo", or bare "milou" for the default.
package oai

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/pierrebrunelle/milou/driver"
	"github.com/pierrebrunelle/milou/router"
)

type Server struct {
	Drivers  map[string]driver.Driver
	Fallback string
	Order    []string
}

type chatReq struct {
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", s.models)
	mux.HandleFunc("/v1/chat/completions", s.completions)
	return mux
}

func (s *Server) models(w http.ResponseWriter, _ *http.Request) {
	out := map[string]any{"object": "list"}
	list := []any{}
	for _, name := range s.Order {
		list = append(list, map[string]any{
			"id": "milou/" + name, "object": "model", "created": 0, "owned_by": "milou",
		})
	}
	out["data"] = list
	writeJSON(w, out)
}

func (s *Server) completions(w http.ResponseWriter, r *http.Request) {
	var req chatReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	name := s.Fallback
	if strings.HasPrefix(req.Model, "milou/") {
		name = strings.TrimPrefix(req.Model, "milou/")
	}
	d, ok := s.Drivers[name]
	if !ok {
		http.Error(w, "unknown driver: "+name, 400)
		return
	}
	prompt := ""
	if n := len(req.Messages); n > 0 {
		prompt = req.Messages[n-1].Content
	}
	tag, clean := router.ParsePrompt(prompt)
	if tag != "" {
		if td, ok := s.Drivers[tag]; ok {
			d, prompt = td, clean
			name = tag
		}
	}
	cwd := "."
	st, err := d.Start(r.Context(), prompt, cwd)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	id := fmt.Sprintf("milou-%d", time.Now().UnixNano())
	if req.Stream {
		s.sse(w, st, id, req.Model)
		return
	}
	var text strings.Builder
	for ev := range st.Events() {
		switch ev.Kind {
		case "text":
			text.WriteString(ev.Text)
		case "tool_call":
			fmt.Fprintf(&text, "\n$ %s\n", ev.Text)
		case "error":
			fmt.Fprintf(&text, "\n[error] %s\n", ev.Text)
		}
	}
	res, _ := st.Wait()
	finish := "stop"
	if res.StopReason == "cancelled" {
		finish = "length"
	}
	writeJSON(w, map[string]any{
		"id": id, "object": "chat.completion", "model": req.Model,
		"choices": []any{map[string]any{
			"index": 0, "finish_reason": finish,
			"message": map[string]any{"role": "assistant", "content": text.String()},
		}},
	})
}

func (s *Server) sse(w http.ResponseWriter, st driver.Stream, id, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fl, _ := w.(http.Flusher)
	bw := bufio.NewWriter(w)
	emit := func(v any) {
		b, _ := json.Marshal(v)
		bw.WriteString("data: " + string(b) + "\n\n")
		bw.Flush()
		fl.Flush()
	}
	chunk := func(delta map[string]any, finish any) {
		emit(map[string]any{
			"id": id, "object": "chat.completion.chunk", "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		})
	}
	for ev := range st.Events() {
		switch ev.Kind {
		case "text":
			chunk(map[string]any{"content": ev.Text}, nil)
		case "tool_call":
			chunk(map[string]any{"content": "\n$ " + ev.Text + "\n"}, nil)
		case "error":
			chunk(map[string]any{"content": "\n[error] " + ev.Text + "\n"}, nil)
		}
	}
	res, _ := st.Wait()
	finish := "stop"
	if res.StopReason != "end_turn" {
		finish = "length"
	}
	chunk(map[string]any{}, finish)
	bw.WriteString("data: [DONE]\n\n")
	bw.Flush()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
