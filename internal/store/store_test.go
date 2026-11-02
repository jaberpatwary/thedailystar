package store

import (
	"encoding/json"
	"fmt"
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

func TestSearchMatchesDepartmentAndDesignationQueries(t *testing.T) {
	s := New("")
	s.Employees = []models.Employee{
		{Name: "Rafi Reporter", Designation: "Staff Reporter", Department: "News"},
		{Name: "Mina Reporter", Designation: "Senior Staff Reporter", Department: "News"},
		{Name: "Nila Editor", Designation: "Assistant Editor", Department: "Digital Operations"},
	}

	tests := []struct {
		query string
		want  []string
	}{
		{query: "designation onujayi reporters", want: []string{"Rafi Reporter", "Mina Reporter"}},
		{query: "Digital Operations department e kara ache", want: []string{"Nila Editor"}},
		{query: "Digital বিভাগে কারা কাজ করে", want: []string{"Nila Editor"}},
	}

	for _, tt := range tests {
		got, _ := s.Search(tt.query)
		if len(got) != len(tt.want) {
			t.Errorf("Search(%q) returned %d employees, want %d", tt.query, len(got), len(tt.want))
			continue
		}
		for _, name := range tt.want {
			found := false
			for _, emp := range got {
				if emp.Name == name {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("Search(%q) did not return %q", tt.query, name)
			}
		}
	}
}

func TestSearchReturnsEveryEmployeeForExactDepartment(t *testing.T) {
	s := New("")
	for i := 0; i < 30; i++ {
		s.Employees = append(s.Employees, models.Employee{
			Name:        fmt.Sprintf("Reporter %02d", i),
			Designation: "Staff Reporter",
			Department:  "News Desk",
		})
	}

	got, score := s.Search("News Desk department")
	if len(got) != 30 {
		t.Fatalf("Search returned %d employees (score %d), want all 30", len(got), score)
	}
}

func TestDepartmentQueriesAndCounts(t *testing.T) {
	s := New("")
	s.Employees = []models.Employee{
		{Name: "A", Department: "Print/Digital"},
		{Name: "B", Department: "Print/Digital"},
		{Name: "C", Department: "News"},
		{Name: "D"},
	}

	if got := s.DepartmentFromQuery("Print/Digital department e koto jon"); got != "Print/Digital" {
		t.Fatalf("DepartmentFromQuery returned %q", got)
	}
	employees := s.EmployeesByDepartment("print/digital")
	if len(employees) != 2 {
		t.Fatalf("EmployeesByDepartment returned %d employees, want 2", len(employees))
	}
	counts := s.DepartmentCounts()
	if len(counts) != 3 || counts[0].Department != "Print/Digital" || counts[0].Count != 2 {
		t.Fatalf("unexpected department counts: %+v", counts)
	}
	if counts[2].Department != "Not specified" || counts[2].Count != 1 {
		t.Fatalf("missing department count not reported: %+v", counts[2])
	}
}

func TestSearchUsesCompleteDesignationForSpecificRoleQuestions(t *testing.T) {
	s := New("")
	s.Employees = []models.Employee{
		{Name: "Mahfuz Anam", Designation: "Editor & Publisher", Department: "The Daily Star"},
		{Name: "Zarif Faiaz", Designation: "Editor", Department: "Tech & Startup"},
		{Name: "Md. Tanvir Ahamed Sarkar", Designation: "Head of IT", Department: "Information Technology"},
		{Name: "Rupok Kumar Roy", Designation: "Sr. System Engineer", Department: "Information Technology"},
	}

	got, _ := s.Search("Who is the editor and publisher in the Daily Star?")
	if len(got) != 1 || got[0].Name != "Mahfuz Anam" {
		t.Fatalf("editor and publisher search returned %+v, want only Mahfuz Anam", got)
	}

	got, _ = s.Search("how is the editor and publishar in thedailystar")
	if len(got) != 1 || got[0].Name != "Mahfuz Anam" {
		t.Fatalf("misspelled editor and publisher search returned %+v, want only Mahfuz Anam", got)
	}

	got, _ = s.Search("Who is the head of the IT team?")
	if len(got) != 1 || got[0].Name != "Md. Tanvir Ahamed Sarkar" {
		t.Fatalf("IT team head search returned %+v, want only the IT head", got)
	}

	got, _ = s.Search("IT team")
	if len(got) != 1 || got[0].Name != "Md. Tanvir Ahamed Sarkar" {
		t.Fatalf("IT team search returned %+v, want only the IT head", got)
	}
}

func TestSearchMatchesDesignationAcronym(t *testing.T) {
	s := New("")
	s.Employees = []models.Employee{
		{Name: "Md. Tajdin Hassan", Designation: "Chief Business Officer", Department: "Business Development"},
		{Name: "Other Employee", Designation: "Business Officer", Department: "Business Development"},
	}

	got, _ := s.Search("who is CBO in the dailytstar?")
	if len(got) != 1 || got[0].Name != "Md. Tajdin Hassan" {
		t.Fatalf("CBO search returned %+v, want only Md. Tajdin Hassan", got)
	}
}
