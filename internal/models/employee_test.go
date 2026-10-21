package models

import "testing"

func TestNormalizeDirectoryFieldsWithShiftedLegacyRow(t *testing.T) {
	e := Employee{Name: "Example"}
	e.NormalizeDirectoryFields([]string{"Example", "Editor", "News", "News Desk", "01712345678 (ID: 1001)", "example@thedailystar.net", "7th Floor"})
	if e.Email != "example@thedailystar.net" || e.Phone != "01712345678" || e.EmployeeID != "1001" || e.Desk != "7th Floor" {
		t.Fatalf("normalization failed: %+v", e)
	}
}

func TestParseExtension(t *testing.T) {
	phone, id := ParseExtension("01728-471997 (ID: 1006)")
	if phone != "01728471997" || id != "1006" {
		t.Fatalf("got %q, %q", phone, id)
	}
}
