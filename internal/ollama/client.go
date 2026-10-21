package ollama

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to a local Ollama instance (http://localhost:11434 by default).
// No API key, no external network call, no cost.
type Client struct {
	BaseURL    string
	EmbedModel string
	ChatModel  string
	HTTP       *http.Client
}

func New(baseURL, embedModel, chatModel string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		EmbedModel: embedModel,
		ChatModel:  chatModel,
		HTTP:       &http.Client{Timeout: 180 * time.Second},
	}
}

// IsAvailable performs a short health check before a potentially long re-index.
func (c *Client) IsAvailable() bool {
	probe := &http.Client{Timeout: 2 * time.Second}
	resp, err := probe.Get(c.BaseURL + "/api/tags")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

type embedRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type embedResponse struct {
	Embedding []float32 `json:"embedding"`
}

// Embed returns a vector embedding for the given text using Ollama's
// /api/embeddings endpoint.
func (c *Client) Embed(text string) ([]float32, error) {
	body, err := json.Marshal(embedRequest{Model: c.EmbedModel, Prompt: text})
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Post(c.BaseURL+"/api/embeddings", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama embeddings request failed (is Ollama running at %s?): %w", c.BaseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama embeddings error (%d): %s", resp.StatusCode, string(b))
	}

	var er embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, err
	}
	return er.Embedding, nil
}

type generateRequest struct {
	Model   string `json:"model"`
	Prompt  string `json:"prompt"`
	System  string `json:"system,omitempty"`
	Stream  bool   `json:"stream"`
	Options struct {
		Temperature float32 `json:"temperature"`
	} `json:"options"`
}

type generateResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
}

// Chat sends a system prompt + user prompt to Ollama's /api/generate
// endpoint (non-streaming) and returns the full text response.
func (c *Client) Chat(systemPrompt, userPrompt string) (string, error) {
	req := generateRequest{
		Model:  c.ChatModel,
		Prompt: userPrompt,
		System: systemPrompt,
		Stream: false,
	}
	req.Options.Temperature = 0.2 // keep it factual, not creative

	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	resp, err := c.HTTP.Post(c.BaseURL+"/api/generate", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("ollama generate request failed (is Ollama running at %s?): %w", c.BaseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ollama generate error (%d): %s", resp.StatusCode, string(b))
	}

	var gr generateResponse
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		return "", err
	}
	return gr.Response, nil
}
