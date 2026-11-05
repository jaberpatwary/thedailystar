package models

import (
	"regexp"
	"strings"
)

// Employee holds the employee-directory fields explicitly approved for this
// local index. Personal phone numbers, home addresses, NID, and DOB are not stored.
type Employee struct {
	ID                 string    `json:"id"`
	Name               string    `json:"name"`
	NameAliases        []string  `json:"name_aliases,omitempty"`
	Designation        string    `json:"designation"`
	DesignationAliases []string  `json:"designation_aliases,omitempty"`
	Department         string    `json:"department"`
	DepartmentAliases  []string  `json:"department_aliases,omitempty"`
	WorkStation        string    `json:"work_station,omitempty"`
	Section            string    `json:"section,omitempty"`
	EmploymentStatus   string    `json:"employment_status,omitempty"`
	EmploymentType     string    `json:"employment_type,omitempty"`
	RecordUpdatedAt    string    `json:"record_updated_at,omitempty"`
	Extension          string    `json:"extension"`   // raw value from CSV
	Phone              string    `json:"phone"`       // parsed phone number (if any)
	EmployeeID         string    `json:"employee_id"` // HR ID, numeric or coded (e.g. "1005", "DC-5002")
	JoiningDate        string    `json:"joining_date"`
	ServiceLength      string    `json:"service_length,omitempty"`
	Age                string    `json:"age,omitempty"`
	Email              string    `json:"email"`
	PersonalEmail      string    `json:"personal_email,omitempty"`
	WhatsAppNumber     string    `json:"whatsapp_number,omitempty"` // direct contact link only; omitted from BuildText
	Desk               string    `json:"desk"`
	LinkedIn           string    `json:"linkedin"`
	Twitter            string    `json:"twitter"`
	Facebook           string    `json:"facebook"`
	Instagram          string    `json:"instagram"`
	BloodGroup         string    `json:"blood_group"`
	Photo              string    `json:"photo"`     // URL or base64 data URI of profile photo
	Text               string    `json:"text"`      // flattened text used for embedding/search
	Embedding          []float32 `json:"embedding"` // semantic embedding of Text
	SchemaVersion      int       `json:"schema_version"`
}

var reEmail = regexp.MustCompile(`(?i)^[^\s@]+@[^\s@]+\.[^\s@]+$`)
var reFloor = regexp.MustCompile(`(?i)\b\d+(?:st|nd|rd|th)\s+floor\b`)
var reHTTPURL = regexp.MustCompile(`(?i)^https?://[^\s]+$`)

func IsEmail(value string) bool { return reEmail.MatchString(strings.TrimSpace(value)) }

// NormalizeDirectoryFields extracts contact fields from imperfect HR exports.
// Some legacy rows contain extra organisational columns, which shift later values.
func (e *Employee) NormalizeDirectoryFields(values []string) {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if e.Email == "" && IsEmail(value) {
			e.Email = value
		}
		if e.Desk == "" && reFloor.MatchString(value) {
			e.Desk = value
		}
	}
	var contact string
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), "id:") {
			contact = value
			break
		}
	}
	if contact == "" {
		for _, value := range values {
			if p, _ := ParseExtension(value); p != "" {
				contact = value
				break
			}
		}
	}
	if contact != "" {
		e.Extension = contact
		phone, employeeID := ParseExtension(contact)
		if phone != "" {
			e.Phone = phone
		}
		if employeeID != "" && e.EmployeeID == "" {
			e.EmployeeID = employeeID
		}
	}
	// Only expose actual URLs as social links; labels shifted from a CSV column are not links.
	if !reHTTPURL.MatchString(e.LinkedIn) {
		e.LinkedIn = ""
	}
	if !reHTTPURL.MatchString(e.Twitter) {
		e.Twitter = ""
	}
	if !reHTTPURL.MatchString(e.Facebook) {
		e.Facebook = ""
	}
	if !reHTTPURL.MatchString(e.Instagram) {
		e.Instagram = ""
	}
	e.SchemaVersion = 5
}

// rePhone matches Bangladeshi mobile numbers: optional leading 0/+88, then 11 digits
// e.g. 01738326082, 1713017529, 01728-471997, +8801711234567
var rePhone = regexp.MustCompile(`(?:(?:\+88|0088)?0?1[3-9][0-9]{8}|1[3-9][0-9]{8})`)

// reEmpID matches (ID: 1234) or ID: 1234
var reEmpID = regexp.MustCompile(`(?i)ID:\s*((?:[A-Za-z]+-)?[0-9]+)\b`)

// ParseExtension splits the raw extension CSV field into a clean Phone and EmployeeID.
// Example inputs:
//
//	"01738326082 (ID: 1005)"  → Phone="01738326082", EmployeeID="1005"
//	"ID: 1001"                → Phone="",             EmployeeID="1001"
//	"01728-471997 (ID: 1006)" → Phone="01728471997",  EmployeeID="1006"
func ParseExtension(raw string) (phone, empID string) {
	raw = strings.TrimSpace(raw)

	// Extract staff ID
	if m := reEmpID.FindStringSubmatch(raw); len(m) == 2 {
		empID = m[1]
	}

	// Extract phone number after normalising separators.
	compact := strings.ReplaceAll(strings.ReplaceAll(raw, "-", ""), " ", "")
	if m := rePhone.FindString(compact); m != "" {
		phone = m
		// Ensure leading 0
		if len(phone) == 10 && phone[0] == '1' {
			phone = "0" + phone
		}
	}
	return
}

// BuildText creates a short natural-language description of the employee.
// This is what gets embedded and what the LLM sees as context.
func (e *Employee) BuildText() string {
	designation := strings.TrimSpace(e.Designation)
	department := strings.TrimSpace(e.Department)
	if designation == "" {
		designation = "Not specified in the source directory"
	}
	if department == "" {
		department = "Not specified in the source directory"
	}
	t := "Name: " + strings.TrimSpace(e.Name) + ". Designation / job title: " + designation + ". Department: " + department + "."
	if workStation := strings.TrimSpace(e.WorkStation); workStation != "" {
		t += " Work station: " + workStation + "."
	}
	if section := strings.TrimSpace(e.Section); section != "" {
		t += " Section: " + section + "."
	}
	if len(e.NameAliases) > 0 {
		t += " Also known as: " + strings.Join(e.NameAliases, ", ") + "."
	}
	if len(e.DesignationAliases) > 0 {
		t += " Designation aliases: " + strings.Join(e.DesignationAliases, ", ") + "."
	}
	if len(e.DepartmentAliases) > 0 {
		t += " Department aliases: " + strings.Join(e.DepartmentAliases, ", ") + "."
	}
	joiningDate := strings.TrimSpace(e.JoiningDate)
	if joiningDate == "" {
		joiningDate = "Not listed in the source directory"
	}
	bloodGroup := strings.TrimSpace(e.BloodGroup)
	if bloodGroup == "" {
		bloodGroup = "Not listed in the source directory"
	}
	t += " Joining date: " + joiningDate + ". Blood group: " + bloodGroup + "."
	if updatedAt := strings.TrimSpace(e.RecordUpdatedAt); updatedAt != "" {
		t += " HR record last verified / updated: " + updatedAt + "."
	}
	if e.Phone != "" {
		t += " Phone: " + e.Phone + "."
	}
	if e.EmployeeID != "" {
		t += " Staff ID: " + e.EmployeeID + "."
	}
	if e.Email != "" {
		t += " Email: " + e.Email + "."
	}
	if e.Desk != "" {
		t += " Desk/Floor: " + e.Desk + "."
	}
	if e.LinkedIn != "" {
		t += " LinkedIn: " + e.LinkedIn + "."
	}
	if e.Twitter != "" {
		t += " Twitter/X: " + e.Twitter + "."
	}
	if e.Facebook != "" {
		t += " Facebook: " + e.Facebook + "."
	}
	if e.Instagram != "" {
		t += " Instagram: " + e.Instagram + "."
	}
	return t
}
