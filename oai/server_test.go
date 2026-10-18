package oai

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pierrebrunelle/milou/driver"
)

func srv() *httptest.Server {
	s := &Server{
		Drivers:  map[string]driver.Driver{"echo": driver.Echo{}},
		Fallback: "echo",
		Order:    []string{"echo"},
	}
	return httptest.NewServer(s.Handler())
}

func TestModelsList(t *testing.T) {
	ts := srv()
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Data) != 1 || body.Data[0].ID != "milou/echo" {
		t.Fatalf("models=%+v", body.Data)
	}
}

func TestNonStreamCompletion(t *testing.T) {
	ts := srv()
	defer ts.Close()
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"milou/echo","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Choices) != 1 || body.Choices[0].Message.Content != "[echo] hello" {
		t.Fatalf("body=%+v", body)
	}
	if body.Choices[0].FinishReason != "stop" {
		t.Fatalf("finish=%q", body.Choices[0].FinishReason)
	}
}

func TestStreamCompletion(t *testing.T) {
	ts := srv()
	defer ts.Close()
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"milou/echo","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var chunks []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "data: ") {
			chunks = append(chunks, line)
		}
	}
	if len(chunks) < 2 || chunks[len(chunks)-1] != "data: [DONE]" {
		t.Fatalf("chunks=%v", chunks)
	}
	var c struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	json.Unmarshal([]byte(strings.TrimPrefix(chunks[0], "data: ")), &c)
	if c.Choices[0].Delta.Content != "[echo] hi" {
		t.Fatalf("first chunk=%v", c)
	}
}

func TestUnknownDriver(t *testing.T) {
	ts := srv()
	defer ts.Close()
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"milou/nope","messages":[{"role":"user","content":"x"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestTagOverridesModel(t *testing.T) {
	// @tag inside the message beats the model= driver selection
	ts := srv()
	defer ts.Close()
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"milou","messages":[{"role":"user","content":"@echo tagged"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	if body.Choices[0].Message.Content != "[echo] tagged" {
		t.Fatalf("body=%+v", body)
	}
}
