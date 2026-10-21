package groq

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Client handles communication with the Groq Cloud API (OpenAI-compatible endpoint).
// Groq provides ultra-fast inference on LPUs (300-500 tokens/sec) for LLaMA 3.3 70B.
type Client struct {
	mu     sync.RWMutex
	APIKey string
	Model  string
	HTTP   *http.Client
}

// New creates a new Groq client.
func New(apiKey, model string) *Client {
	if model == "" {
		model = "openai/gpt-oss-120b"
	}
	return &Client{
		APIKey: strings.TrimSpace(apiKey),
		Model:  strings.TrimSpace(model),
		HTTP:   &http.Client{Timeout: 45 * time.Second},
	}
}

// IsConfigured returns true if a valid-looking API key is present.
func (c *Client) IsConfigured() bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.APIKey) > 10
}

// SetAPIKey dynamically updates the API key in memory.
func (c *Client) SetAPIKey(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.APIKey = strings.TrimSpace(key)
}

// GetAPIKey returns the current API key.
func (c *Client) GetAPIKey() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.APIKey
}

// MaskedKey returns a masked representation like gsk_...abcd for display.
func (c *Client) MaskedKey() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.APIKey) <= 8 {
		return ""
	}
	prefix := c.APIKey[:4]
	suffix := c.APIKey[len(c.APIKey)-4:]
	return prefix + "..." + suffix
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float32       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

// Chat sends a system prompt + user prompt to Groq's OpenAI-compatible completions API.
func (c *Client) Chat(systemPrompt, userPrompt string) (string, error) {
	c.mu.RLock()
	apiKey := c.APIKey
	model := c.Model
	c.mu.RUnlock()

	if apiKey == "" {
		return "", fmt.Errorf("groq API key is not configured")
	}

	reqBody := chatRequest{
		Model: model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Temperature: 0.2, // Low temperature for factual RAG responses
		MaxTokens:   1024,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest(http.MethodPost, "https://api.groq.com/openai/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("groq network error: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read groq response: %w", err)
	}

	var cr chatResponse
	if err := json.Unmarshal(respBytes, &cr); err != nil {
		return "", fmt.Errorf("failed to parse groq response (status %d): %w", resp.StatusCode, err)
	}

	if cr.Error != nil && cr.Error.Message != "" {
		return "", fmt.Errorf("groq API error: %s", cr.Error.Message)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("groq error HTTP %d: %s", resp.StatusCode, string(respBytes))
	}

	if len(cr.Choices) == 0 {
		return "", fmt.Errorf("groq returned no choices")
	}

	return cr.Choices[0].Message.Content, nil
}

// Test verifies the API key by sending a short ping prompt.
func (c *Client) Test() error {
	_, err := c.Chat("You are a helpful assistant.", "Say 'ok' if you can read this.")
	return err
}
