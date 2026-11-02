package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"dailystar-chatbot/internal/models"
	"dailystar-chatbot/internal/store"
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

func TestIsCountQuestion(t *testing.T) {
	for _, query := range []string{
		"How many people are in Digital Operations?",
		"Digital Operations e koto jon?",
		"প্রিন্ট বিভাগে কতজন কাজ করেন?",
	} {
		if !isCountQuestion(query) {
			t.Errorf("isCountQuestion(%q) = false, want true", query)
		}
	}
	if isCountQuestion("Who works in Digital Operations?") {
		t.Fatal("ordinary directory lookup was classified as a count question")
	}
}

func TestHandleChatDepartmentCountIncludesNamesAndDesignations(t *testing.T) {
	directory := store.New("")
	directory.Employees = []models.Employee{
		{Name: "Asha", Designation: "Reporter", Department: "News"},
		{Name: "Bashir", Designation: "Editor", Department: "News"},
		{Name: "Chaya", Designation: "Designer", Department: "Graphics"},
	}
	handler := New(directory, nil, nil, "", "", fstest.MapFS{}, "", "").Handler()
	request := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewBufferString(`{"message":"News department e koto jon?"}`))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}

	var result chatResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Answer, "2 staff members") ||
		!strings.Contains(result.Answer, "Asha") ||
		!strings.Contains(result.Answer, "Reporter") ||
		!strings.Contains(result.Answer, "Bashir") ||
		len(result.Employees) != 2 {
		t.Fatalf("department count response omitted employee details: %+v", result)
	}
}

func TestHandleChatCBOUsesVerifiedDirectoryRecord(t *testing.T) {
	directory := store.New("")
	directory.Employees = []models.Employee{
		{Name: "Md. Tajdin Hassan", Designation: "Chief Business Officer", Department: "Business Development"},
		{Name: "Other Employee", Designation: "Business Officer", Department: "Business Development"},
	}
	handler := New(directory, nil, nil, "", "", fstest.MapFS{}, "", "").Handler()
	request := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewBufferString(`{"message":"who is CBO in the dailytstar?"}`))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	var result chatResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Mode != "direct_lookup" || len(result.Employees) != 1 ||
		result.Employees[0].Name != "Md. Tajdin Hassan" ||
		result.Employees[0].Designation != "Chief Business Officer" {
		t.Fatalf("CBO query did not use the single verified directory record: %+v", result)
	}
}
