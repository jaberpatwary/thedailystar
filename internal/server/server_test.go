package server

import (
	"net/http/httptest"
	"testing"
)

func TestTokenMatches(t *testing.T) {
	if !tokenMatches("secret", "secret") || tokenMatches("wrong", "secret") || tokenMatches("", "") {
		t.Fatal("token comparison returned an unexpected result")
	}
}

func TestRateLimit(t *testing.T) {
	s := &Server{visitors: make(map[string]*visitor)}
	r := httptest.NewRequest("GET", "/api/health", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	for i := 0; i < 60; i++ {
		if !s.allowRequest(r) {
			t.Fatalf("request %d was rejected early", i)
		}
	}
	if s.allowRequest(r) {
		t.Fatal("request above rate limit was accepted")
	}
}
