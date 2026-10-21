package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"dailystar-chatbot/internal/models"
)

func TestLoadJSONUpgradesLegacyShiftedRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	legacy := []models.Employee{{
		Name: "Example", Designation: "Editor", Department: "News",
		Extension: "News Desk", Email: "01712345678 (ID: 1001)",
		Desk: "example@thedailystar.net", LinkedIn: "7th Floor",
	}}
	b, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	s := New(path)
	loaded, err := s.LoadJSON()
	if err != nil || !loaded {
		t.Fatalf("loaded=%v err=%v", loaded, err)
	}
	e := s.Employees[0]
	if e.Email != "example@thedailystar.net" || e.Phone != "01712345678" || e.EmployeeID != "1001" || e.Desk != "7th Floor" {
		t.Fatalf("legacy migration failed: %+v", e)
	}
}
