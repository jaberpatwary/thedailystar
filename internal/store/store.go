package store

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"dailystar-chatbot/internal/models"
	"dailystar-chatbot/internal/ollama"
	"regexp"
)

// Store keeps all employees in memory and persists them to a JSON file
// so embeddings don't need to be regenerated on every restart.
type Store struct {
	mu        sync.RWMutex
	Employees []models.Employee
	jsonPath  string
}

type DepartmentCount struct {
	Department string
	Count      int
}

type DataQuality struct {
	Employees              int `json:"employees"`
	EmployeesWithAliases   int `json:"employees_with_aliases"`
	DuplicateNames         int `json:"duplicate_names"`
	MissingName            int `json:"missing_name"`
	MissingDesignation     int `json:"missing_designation"`
	MissingDepartment      int `json:"missing_department"`
	MissingJoiningDate     int `json:"missing_joining_date"`
	MissingAge             int `json:"missing_age"`
	MissingServiceLength   int `json:"missing_service_length"`
	MissingPersonalEmail   int `json:"missing_personal_email"`
	MissingEmploymentType  int `json:"missing_employment_type"`
	MissingWhatsAppNumber  int `json:"missing_whatsapp_number"`
	MissingBloodGroup      int `json:"missing_blood_group"`
	DuplicateEmployeeIDs   int `json:"duplicate_employee_ids"`
	InvalidJoiningDates    int `json:"invalid_joining_dates"`
	InvalidBloodGroups     int `json:"invalid_blood_groups"`
	InvalidStatuses        int `json:"invalid_employment_statuses"`
	MissingRecordUpdatedAt int `json:"missing_record_updated_at"`
	InvalidRecordUpdatedAt int `json:"invalid_record_updated_at"`
}

func New(jsonPath string) *Store {
	return &Store{jsonPath: jsonPath}
}

// LoadJSON loads a previously saved employee+embedding cache, if it exists.
// Returns false if the file doesn't exist yet (first run).
func (s *Store) LoadJSON() (bool, error) {
	b, err := os.ReadFile(s.jsonPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := json.Unmarshal(b, &s.Employees); err != nil {
		return false, err
	}
	for i := range s.Employees {
		if s.Employees[i].SchemaVersion < 2 {
			// Upgrade legacy cached records in memory. This keeps the app usable
			// when Ollama is offline; a later CSV update will rebuild embeddings.
			e := &s.Employees[i]
			values := []string{e.Name, e.Designation, e.Department, e.Extension, e.Email, e.Desk, e.LinkedIn, e.Twitter, e.Facebook, e.Instagram}
			e.Email, e.Desk = "", ""
			e.NormalizeDirectoryFields(values)
			e.SchemaVersion = 2 // keep rebuild pending for the current index format
		}
		if s.Employees[i].SchemaVersion < 5 {
			enrichOrganizationFields(&s.Employees[i])
			s.Employees[i].Text = s.Employees[i].BuildText()
		}
		if s.Employees[i].Phone == "" && s.Employees[i].EmployeeID == "" && s.Employees[i].Extension != "" {
			s.Employees[i].Phone, s.Employees[i].EmployeeID = models.ParseExtension(s.Employees[i].Extension)
		}
	}
	return true, nil
}

// NeedsRebuild returns true when the HR source is newer than the cached index.
func (s *Store) NeedsRebuild(csvPath string) bool {
	cacheInfo, cacheErr := os.Stat(s.jsonPath)
	csvInfo, csvErr := os.Stat(csvPath)
	if cacheErr != nil || csvErr != nil || csvInfo.ModTime().After(cacheInfo.ModTime()) {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, employee := range s.Employees {
		if employee.SchemaVersion < 5 {
			return true
		}
	}
	return false
}

// enrichOrganizationFields repairs only organization details that can be
// inferred from explicit HR directory IDs or obvious non-title values.
func enrichOrganizationFields(e *models.Employee) {
	extension := strings.ToLower(strings.TrimSpace(e.Extension))
	isCafe := strings.Contains(extension, "id: caf")
	isDistrictCorrespondent := strings.Contains(extension, "id: dc-")

	if strings.TrimSpace(e.Department) == "" || (isCafe && strings.EqualFold(strings.TrimSpace(e.Department), "Male")) {
		switch {
		case isCafe:
			e.Department = "DS Caf?"
		case isDistrictCorrespondent:
			e.Department = "District Correspondent"
		case strings.Contains(strings.ToLower(e.Designation), "district correspondent"):
			e.Department = "District Correspondent"
		}
	}

	if isCafe {
		switch strings.ToLower(strings.TrimSpace(e.Designation)) {
		case "male", "female":
			e.Designation = ""
		case "4th":
			e.Designation = ""
			if strings.TrimSpace(e.Desk) == "" {
				e.Desk = "4th Floor"
			}
		case "head office":
			e.Designation = ""
			if strings.TrimSpace(e.Desk) == "" {
				e.Desk = "Head Office"
			}
		}
	}
}

// SaveJSON persists the current employee list (with embeddings) to disk.
func (s *Store) SaveJSON() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, err := json.MarshalIndent(s.Employees, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.jsonPath, b, 0644)
}

// BuildFromCSV reads the HR export CSV, builds a text description for each row, fetches an
// embedding from Ollama, and replaces the in-memory employee list.
func (s *Store) BuildFromCSV(csvPath string, client *ollama.Client) error {
	f, err := os.Open(csvPath)
	if err != nil {
		return fmt.Errorf("cannot open CSV %s: %w", csvPath, err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	rows, err := r.ReadAll()
	if err != nil {
		return fmt.Errorf("cannot parse CSV: %w", err)
	}
	if len(rows) < 2 {
		return fmt.Errorf("CSV has no data rows")
	}

	header := rows[0]
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	required := []string{"name"}
	for _, req := range required {
		if _, ok := col[req]; !ok {
			return fmt.Errorf("CSV missing required column %q", req)
		}
	}
	get := func(row []string, key string) string {
		if idx, ok := col[key]; ok && idx < len(row) {
			return strings.TrimSpace(row[idx])
		}
		return ""
	}
	getFirst := func(row []string, keys ...string) string {
		for _, key := range keys {
			if value := get(row, key); value != "" {
				return value
			}
		}
		return ""
	}
	getAliases := func(row []string, key string) []string {
		value := get(row, key)
		if value == "" {
			return nil
		}
		parts := strings.Split(value, "|")
		aliases := make([]string, 0, len(parts))
		for _, part := range parts {
			if part = strings.TrimSpace(part); part != "" {
				aliases = append(aliases, part)
			}
		}
		return aliases
	}
	// Reuse cached vectors for unchanged employee text so a small HR CSV edit
	// does not force an expensive embed request for every employee.
	cachedEmbeddings := make(map[string][]float32)
	if cached, readErr := os.ReadFile(s.jsonPath); readErr == nil {
		var oldEmployees []models.Employee
		if json.Unmarshal(cached, &oldEmployees) == nil {
			for _, employee := range oldEmployees {
				if employee.Text != "" && len(employee.Embedding) > 0 {
					cachedEmbeddings[employee.Text] = employee.Embedding
				}
			}
		}
	}

	var employees []models.Employee
	for i, row := range rows[1:] {
		e := models.Employee{
			ID:                 fmt.Sprintf("emp-%d", i+1),
			Name:               get(row, "name"),
			NameAliases:        getAliases(row, "name_aliases"),
			Designation:        get(row, "designation"),
			DesignationAliases: getAliases(row, "designation_aliases"),
			Department:         get(row, "department"),
			DepartmentAliases:  getAliases(row, "department_aliases"),
			EmploymentStatus:   getFirst(row, "employment_status", "status"),
			EmploymentType:     getFirst(row, "employment_type", "employment_category", "contract_type"),
			RecordUpdatedAt:    getFirst(row, "record_updated_at", "updated_at", "last_updated_at"),
			Extension:          get(row, "extension"),
			EmployeeID:         getFirst(row, "hr_id", "employee_id", "staff_id"),
			JoiningDate:        getFirst(row, "joining_date", "join_date", "date_of_joining"),
			ServiceLength:      getFirst(row, "service_length", "tenure", "years_of_service"),
			Age:                get(row, "age"),
			Email:              get(row, "email"),
			PersonalEmail:      getFirst(row, "personal_email", "personal_mail", "private_email"),
			WhatsAppNumber:     getFirst(row, "whatsapp_number", "whatsapp", "contact_mobile"),
			Desk:               get(row, "desk"),
			LinkedIn:           get(row, "linkedin"),
			Twitter:            get(row, "twitter"),
			Facebook:           get(row, "facebook"),
			Instagram:          get(row, "instagram"),
			BloodGroup:         get(row, "blood_group"),
		}
		if e.Name == "" {
			continue
		}
		if isInactiveStatus(e.EmploymentStatus) {
			log.Printf("skipped inactive employee %s (%s)", e.Name, e.EmploymentStatus)
			continue
		}
		e.Email = ""
		e.Desk = ""
		e.NormalizeDirectoryFields(row)
		enrichOrganizationFields(&e)
		e.Text = e.BuildText()

		if cachedEmbedding, ok := cachedEmbeddings[e.Text]; ok {
			e.Embedding = cachedEmbedding
		} else {
			emb, err := client.Embed(e.Text)
			if err != nil {
				return fmt.Errorf("embedding failed for %s: %w", e.Name, err)
			}
			e.Embedding = emb
		}
		employees = append(employees, e)
		log.Printf("indexed %d/%d: %s", i+1, len(rows)-1, e.Name)
	}

	s.mu.Lock()
	s.Employees = employees
	s.mu.Unlock()

	return s.SaveJSON()
}

func isInactiveStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "inactive", "former", "left", "resigned", "terminated", "retired", "separated":
		return true
	default:
		return false
	}
}

var reNonAlpha = regexp.MustCompile(`[^\p{L}\p{M}\p{N}\s]+`)

func normalizeSearchTerm(term string) string {
	term = canonicalSearchToken(term)
	if strings.HasSuffix(term, "ies") && len(term) > 4 {
		return strings.TrimSuffix(term, "ies") + "y"
	}
	if strings.HasSuffix(term, "s") && len(term) > 3 && !strings.HasSuffix(term, "ss") {
		return strings.TrimSuffix(term, "s")
	}
	return term
}

func canonicalSearchToken(term string) string {
	switch strings.ToLower(term) {
	case "shob", "sob", "\u09b8\u09ac", "\u09b8\u0995\u09b2":
		return "all"
	case "rokom", "rokomer", "\u09b0\u0995\u09ae", "\u09a7\u09b0\u09a8\u09c7\u09b0":
		return "type"
	case "sompadok", "shompadok", "\u09b8\u09ae\u09cd\u09aa\u09be\u09a6\u0995":
		return "editor"
	case "jugmo", "\u09af\u09c1\u0997\u09cd\u09ae":
		return "joint"
	case "uposompadok", "uposhompadok", "\u0989\u09aa\u09b8\u09ae\u09cd\u09aa\u09be\u09a6\u0995":
		return "sub"
	case "sangbadik", "\u09b8\u09be\u0982\u09ac\u09be\u09a6\u09bf\u0995":
		return "reporter"
	case "bibhag", "bivag", "\u09ac\u09bf\u09ad\u09be\u0997":
		return "department"
	case "podobi", "podobhi", "\u09aa\u09a6\u09ac\u09bf", "\u09aa\u09a6\u09ac\u09c0":
		return "designation"
	case "prodhan", "\u09aa\u09cd\u09b0\u09a7\u09be\u09a8":
		return "head"
	case "sr", "snr":
		return "senior"
	case "asst", "ast":
		return "assistant"
	case "exec":
		return "executive"
	case "jr":
		return "junior"
	case "mgr":
		return "manager"
	case "dept":
		return "department"
	default:
		return strings.ToLower(term)
	}
}

func wantsAllEditorTypes(query string) bool {
	normalized := normalizeSearchTokens(query)
	if !containsWord(normalized, "editor") {
		return false
	}
	for _, phrase := range []string{"all editor", "all editor role", "all type editor", "all type of editor", "all types of editor", "all kind of editor", "all kinds of editor", "type editor", "editor type", "all kinds editor", "editor hierarchy"} {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	return containsWord(normalized, "all") && (containsWord(normalized, "type") || containsWord(normalized, "kind"))
}

func employeeHasEditorTitle(employee models.Employee) bool {
	titles := append([]string{employee.Designation}, employee.DesignationAliases...)
	for _, title := range titles {
		words := strings.Fields(normalizeSearchText(strings.SplitN(title, ",", 2)[0]))
		for _, word := range words {
			if normalizeSearchTerm(word) == "editor" {
				return true
			}
		}
	}
	return false
}

func isHeadQuestion(query string) bool {
	for _, term := range strings.Fields(normalizeSearchTokens(query)) {
		if term == "head" {
			return true
		}
	}
	return false
}

func employeeHasHeadTitle(employee models.Employee) bool {
	titles := append([]string{employee.Designation}, employee.DesignationAliases...)
	for _, title := range titles {
		if containsWord(strings.SplitN(title, ",", 2)[0], "head") {
			return true
		}
	}
	return false
}

func headQueryTopicTerms(query string) []string {
	ignored := map[string]bool{"head": true, "of": true, "the": true, "who": true, "is": true, "a": true, "an": true, "department": true, "dept": true, "team": true, "please": true, "tell": true, "me": true, "find": true, "what": true, "all": true, "staff": true, "employee": true, "employees": true, "person": true, "people": true}
	var terms []string
	for _, term := range strings.Fields(normalizeSearchTokens(query)) {
		if !ignored[term] {
			terms = append(terms, term)
		}
	}
	return terms
}

func employeeHeadNameTerms(employee models.Employee, queryTerms []string) []string {
	var contextWords []string
	for _, context := range append([]string{employee.Department}, employee.DepartmentAliases...) {
		contextWords = append(contextWords, strings.Fields(normalizeSearchTokens(context))...)
	}
	for _, title := range append([]string{employee.Designation}, employee.DesignationAliases...) {
		if !containsWord(strings.SplitN(title, ",", 2)[0], "head") {
			continue
		}
		for _, word := range strings.Fields(normalizeSearchTokens(strings.SplitN(title, ",", 2)[0])) {
			if word != "head" && word != "of" && word != "department" {
				contextWords = append(contextWords, word)
			}
		}
	}
	var nameTerms []string
	for _, queryTerm := range queryTerms {
		isContext := false
		for _, contextWord := range contextWords {
			if wordsMatchWithTypo(queryTerm, contextWord) {
				isContext = true
				break
			}
		}
		if !isContext {
			nameTerms = append(nameTerms, queryTerm)
		}
	}
	return nameTerms
}

func employeeNameMatchesTerms(employee models.Employee, terms []string) bool {
	if len(terms) == 0 {
		return true
	}
	names := append([]string{employee.Name}, employee.NameAliases...)
	for _, name := range names {
		if matched, _ := fieldMatchesTerms(terms, name); matched {
			return true
		}
		nameWords := strings.Fields(normalizeSearchTokens(name))
		used := make([]bool, len(nameWords))
		allMatched := true
		for _, term := range terms {
			found := false
			for i, word := range nameWords {
				if used[i] {
					continue
				}
				if wordsMatchWithTypo(term, word) || (len([]rune(term)) >= 4 && strings.HasPrefix(word, term)) {
					used[i] = true
					found = true
					break
				}
			}
			if !found {
				allMatched = false
				break
			}
		}
		if allMatched {
			return true
		}
	}
	return false
}

func employeeHeadDepartmentMatchesTerms(employee models.Employee, terms []string) bool {
	for _, department := range append([]string{employee.Department}, employee.DepartmentAliases...) {
		departmentWords := strings.Fields(normalizeSearchTokens(department))
		for _, departmentWord := range departmentWords {
			for _, term := range terms {
				if term == departmentWord || wordsMatchWithTypo(term, departmentWord) {
					return true
				}
			}
		}
	}
	return false
}

func employmentTypeFromQuery(query string) string {
	normalized := " " + normalizeSearchTokens(query) + " "
	switch {
	case strings.Contains(normalized, " part time ") || strings.Contains(normalized, " part timer "):
		return "Part-Time"
	case containsWord(normalized, "permanent") || containsWord(normalized, "regular"):
		return "Permanent"
	case containsWord(normalized, "contractual") || containsWord(normalized, "contract"):
		return "Contractual"
	case containsWord(normalized, "intern") || containsWord(normalized, "internship"):
		return "Intern"
	default:
		return ""
	}
}

func employmentTypeNameTerms(query string) []string {
	ignored := map[string]bool{
		"permanent": true, "regular": true, "part": true, "time": true, "timer": true,
		"contractual": true, "contract": true, "intern": true, "internship": true,
		"employment": true, "type": true, "status": true, "job": true,
		"employee": true, "employees": true, "staff": true, "worker": true, "workers": true,
		"in": true, "at": true, "with": true, "from": true, "department": true, "team": true, "division": true,
		"who": true, "is": true, "are": true, "the": true, "a": true, "an": true,
		"all": true, "list": true, "show": true, "find": true, "tell": true,
		"me": true, "of": true, "for": true, "please": true,
	}
	var terms []string
	for _, term := range strings.Fields(normalizeSearchTokens(query)) {
		if !ignored[term] {
			terms = append(terms, term)
		}
	}
	return terms
}

func wantsAllDepartmentHeads(query string) bool {
	normalized := normalizeSearchTokens(query)
	if containsWord(normalized, "all") || containsWord(normalized, "every") || containsWord(normalized, "list") {
		return true
	}
	return strings.Contains(normalized, "department head") && len(headQueryTopicTerms(query)) == 0
}

func headTitleMatchesTerms(employee models.Employee, terms []string) bool {
	if len(terms) == 0 {
		return false
	}
	titles := append([]string{employee.Designation}, employee.DesignationAliases...)
	for _, title := range titles {
		title = strings.SplitN(title, ",", 2)[0]
		if !containsWord(title, "head") {
			continue
		}
		titleWords := strings.Fields(normalizeSearchText(title))
		var searchableTitle strings.Builder
		for _, word := range titleWords {
			if normalizeSearchTerm(word) != "head" && normalizeSearchTerm(word) != "of" && normalizeSearchTerm(word) != "department" {
				searchableTitle.WriteString(word + " ")
			}
		}
		if matched, _ := fieldMatchesTerms(terms, searchableTitle.String()); matched {
			return true
		}
	}
	return false
}

func validJoiningDate(value string) bool {
	value = strings.TrimSpace(value)
	for _, layout := range []string{"2006-01-02", "02/01/2006", "1/2/2006", "02-01-2006", "2 Jan 2006", "02 Jan 2006", "January 2, 2006", "Jan 2, 2006"} {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}

func validBloodGroup(value string) bool {
	group := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(value), " ", ""))
	group = strings.NewReplacer("POSITIVE", "+", "POS", "+", "NEGATIVE", "-", "NEG", "-").Replace(group)
	switch group {
	case "A+", "A-", "B+", "B-", "AB+", "AB-", "O+", "O-":
		return true
	default:
		return false
	}
}

func validEmploymentStatus(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "active", "current", "employed", "inactive", "former", "left", "resigned", "terminated", "retired", "separated":
		return true
	default:
		return false
	}
}

func wordsMatchWithTypo(queryWord, fieldWord string) bool {
	if queryWord == fieldWord {
		return true
	}
	return editDistanceWithin(queryWord, fieldWord, typoDistanceLimit(queryWord))
}

func typoDistanceLimit(word string) int {
	switch n := len([]rune(word)); {
	case n < 4:
		return 0
	case n < 8:
		return 1
	default:
		return 2
	}
}

func editDistanceWithin(left, right string, limit int) bool {
	if limit == 0 {
		return left == right
	}

	a, b := []rune(left), []rune(right)
	if len(a) == len(b) {
		firstDifference := -1
		transposed := true
		for i := range a {
			if a[i] == b[i] {
				continue
			}
			if firstDifference < 0 {
				firstDifference = i
				continue
			}
			if i != firstDifference+1 || a[firstDifference] != b[i] || a[i] != b[firstDifference] {
				transposed = false
				break
			}
			for j := i + 1; j < len(a); j++ {
				if a[j] != b[j] {
					transposed = false
					break
				}
			}
			if transposed {
				return true
			}
			break
		}
	}
	if len(a)-len(b) > limit || len(b)-len(a) > limit {
		return false
	}
	if len(a) > len(b) {
		a, b = b, a
	}

	previous := make([]int, len(a)+1)
	for i := range previous {
		previous[i] = i
	}
	for row, bRune := range b {
		current := make([]int, len(a)+1)
		current[0] = row + 1
		minimum := current[0]
		for column, aRune := range a {
			cost := 0
			if aRune != bRune {
				cost = 1
			}
			current[column+1] = min(
				previous[column+1]+1,
				current[column]+1,
				previous[column]+cost,
			)
			if current[column+1] < minimum {
				minimum = current[column+1]
			}
		}
		if minimum > limit {
			return false
		}
		previous = current
	}
	return previous[len(a)] <= limit
}

// fieldMatchesTerms compares every meaningful query token with a distinct
// token in one employee field. Short words must match exactly, preventing
// broad fuzzy matches such as IT matching unrelated text.
func fieldMatchesTerms(queryTerms []string, field string) (bool, int) {
	fieldTerms := strings.Fields(normalizeSearchText(field))
	for i := range fieldTerms {
		fieldTerms[i] = normalizeSearchTerm(fieldTerms[i])
	}
	if len(queryTerms) == 0 || len(queryTerms) > len(fieldTerms) {
		return false, 0
	}
	used := make([]bool, len(fieldTerms))
	exactMatches, totalDistance := 0, 0
	for _, queryTerm := range queryTerms {
		bestIndex, bestDistance := -1, int(^uint(0)>>1)
		for i, fieldTerm := range fieldTerms {
			if used[i] {
				continue
			}
			limit := typoDistanceLimit(queryTerm)
			if !editDistanceWithin(queryTerm, fieldTerm, limit) {
				continue
			}
			distance := editDistance(queryTerm, fieldTerm)
			if distance < bestDistance {
				bestIndex, bestDistance = i, distance
			}
		}
		if bestIndex < 0 {
			return false, 0
		}
		used[bestIndex] = true
		totalDistance += bestDistance
		if bestDistance == 0 {
			exactMatches++
		}
	}
	return true, exactMatches*10 - totalDistance
}

func editDistance(left, right string) int {
	a, b := []rune(left), []rune(right)
	// Treat one adjacent transposition as the common single typo it is.
	if len(a) == len(b) {
		firstDifference := -1
		for i := range a {
			if a[i] != b[i] {
				if firstDifference < 0 {
					firstDifference = i
					continue
				}
				if i == firstDifference+1 && a[firstDifference] == b[i] && a[i] == b[firstDifference] {
					for j := i + 1; j < len(a); j++ {
						if a[j] != b[j] {
							return 2
						}
					}
					return 1
				}
				return 2
			}
		}
	}
	previous := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i, aRune := range a {
		current := make([]int, len(b)+1)
		current[0] = i + 1
		for j, bRune := range b {
			cost := 0
			if aRune != bRune {
				cost = 1
			}
			current[j+1] = min(previous[j+1]+1, current[j]+1, previous[j]+cost)
		}
		previous = current
	}
	return previous[len(b)]
}

var stopWords = map[string]bool{
	"who": true, "is": true, "the": true, "in": true, "on": true, "at": true,
	"many": true, "what": true, "which": true, "that": true, "whatismy": true,
	"for": true, "and": true, "to": true, "of": true, "a": true, "an": true,
	"all": true, "list": true, "tell": true, "me": true, "about": true,
	"er": true, "ke": true, "kake": true, "ache": true, "kothay": true,
	"ki": true, "keno": true, "konjon": true, "kotogulo": true, "koto": true,
	"daw": true, "dao": true, "details": true, "contact": true, "phone": true,
	"email": true, "number": true, "please": true, "give": true, "show": true, "info": true,
	"are": true, "work": true, "works": true, "working": true, "from": true,
	"employee": true, "employees": true, "people": true, "person": true,
	"department": true, "departments": true, "dept": true, "designation": true,
	"designations": true, "title": true, "wise": true, "onujayi": true,
	"onujai": true, "anuyayi": true, "anuyai": true,
	"kara": true, "ase": true, "kon": true, "kono": true, "e": true, "te": true,
	"daily": true, "star": true, "team": true, "head": true, "how": true,
	"blood": true, "group": true, "bloodgroup": true,
	"joining": true, "joined": true, "date": true,
	"rokter": true, "rokt": true, "jogdan": true, "jogdane": true,
	"jogdaner": true, "kobe": true, "tarikh": true,
	"রক্তের": true, "গ্রুপ": true, "এর": true,
	"যোগদান": true, "যোগদানের": true, "তারিখ": true,
	"bivag": true, "bibhag": true, "bivager": true, "bibhager": true,
	"bivage": true, "bibhage": true, "podobi": true, "podobhi": true,
	"podobir": true, "onujayee": true,
	"bivagta": true, "bivagti": true, "bibhagta": true, "bibhagti": true,
	"designationta": true, "designationti": true, "podobita": true,
	"janbo": true, "jante": true, "bolben": true, "kemon": true,
	"diye": true, "diya": true, "bol": true, "bolo": true, "janan": true,
	"keu": true, "kaj": true, "kore": true, "koren": true, "kora": true,
	"সব": true, "সকল": true, "অনুযায়ী": true, "অনুযায়ী": true,
	"বিভাগ": true, "বিভাগের": true, "বিভাগে": true, "পদবি": true, "পদবী": true,
	"কর্মী": true, "কর্মীরা": true, "কারা": true, "কে": true,
	"আছে": true, "কাজ": true, "করে": true, "করা": true,
}

func CleanQuery(q string) string {
	q = strings.ToLower(strings.TrimSpace(q))
	prefixes := []string{
		"who is the ", "who is on the ", "who is in the ", "who works in the ", "who works on the ",
		"who is ", "who are the ", "who are on the ", "who are in the ", "who are ", "who works at ",
		"who works on ", "who works in ", "tell me about ", "details of ", "find ", "search for ",
		"contact of ", "email of ", "phone of ", "phone number of ", "mobile of ", "mobile number of ",
		"contact number of ", "list of ", "show me ", "give me ",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(q, p) {
			q = strings.TrimPrefix(q, p)
			break
		}
	}
	suffixes := []string{
		" er phone number daw", " er phone number dao", " er number daw", " er number dao",
		" er mobile number", " er phone number", " er contact number", " er details",
		" er number", " er phone", " er mobile", " er email", " er contact", " er",
		" phone number", " mobile number", " contact number", " number", " phone", " mobile",
		" ke ache", " ke ke ache", " ke", " kake", " kothay boshe", " kothay thake",
	}
	for _, s := range suffixes {
		if strings.HasSuffix(q, s) {
			q = strings.TrimSuffix(q, s)
			break
		}
	}
	q = strings.Trim(q, "?!.,\"' ")
	return strings.TrimSpace(q)
}

func containsWord(text, word string) bool {
	words := strings.Fields(reNonAlpha.ReplaceAllString(strings.ToLower(text), " "))
	for _, w := range words {
		if w == word {
			return true
		}
	}
	return false
}

// FindByName kept for backward compatibility; calls Search.
func (s *Store) FindByName(query string) []models.Employee {
	res, _ := s.Search(query)
	return res
}

// Search performs high-accuracy multi-field direct search against employee records.
// It matches names, designations, departments, employment types, desks, and contact fields.
func (s *Store) Search(rawQuery string) ([]models.Employee, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rawLower := strings.ToLower(strings.TrimSpace(rawQuery))
	cleaned := CleanQuery(rawQuery)
	if cleaned == "" {
		cleaned = rawLower
	}

	rawTerms := strings.Fields(reNonAlpha.ReplaceAllString(cleaned, " "))
	attributeQuery := strings.Contains(rawLower, "join date") || strings.Contains(rawLower, "joining date") ||
		strings.Contains(rawLower, "date of joining") || strings.Contains(rawLower, "jogdaner tarikh") ||
		strings.Contains(rawLower, "record updated") || strings.Contains(rawLower, "last updated") || strings.Contains(rawLower, "updated date") ||
		strings.Contains(rawLower, "age") || strings.Contains(rawLower, "boyosh") || strings.Contains(rawLower, "\u09ac\u09af\u09bc\u09b8") || strings.Contains(rawLower, "\u09ac\u09df\u09b8") || strings.Contains(rawLower, "tenure") ||
		strings.Contains(rawLower, "service length") || strings.Contains(rawLower, "length of service") || strings.Contains(rawLower, "how long") ||
		strings.Contains(rawLower, "personal email") || strings.Contains(rawLower, "personal mail") || strings.Contains(rawLower, "private email") ||
		strings.Contains(rawLower, "koto din") || strings.Contains(rawLower, "koto bochor") || strings.Contains(rawLower, "chakri koto") || strings.Contains(rawLower, "kormo jibon") || strings.Contains(rawLower, "how many days") || strings.Contains(rawLower, "how many years") ||
		strings.Contains(rawLower, "employment type") || strings.Contains(rawLower, "employment status") || strings.Contains(rawLower, "job type") || strings.Contains(rawLower, "job status") || strings.Contains(rawLower, "service log")
	personalEmailQuery := strings.Contains(rawLower, "personal email") || strings.Contains(rawLower, "personal mail") || strings.Contains(rawLower, "private email")
	var terms []string
	for _, t := range rawTerms {
		t = normalizeSearchTerm(t)
		if attributeQuery && (t == "join" || t == "joining" || t == "joined" || t == "date" || t == "tarikh" || t == "record" || t == "updated" || t == "update" || t == "last" || t == "age" || t == "boyosh" || t == "tenure" || t == "service" || t == "length" || t == "log" || t == "long" || t == "old" || t == "personal" || t == "private" || t == "mail" || t == "koto" || t == "din" || t == "bochor" || t == "chakri" || t == "job" || t == "type" || t == "status" || t == "employment" || t == "hoyce" || t == "hoise" || t == "worked" || t == "working" || t == "many" || t == "days" || t == "years" || (personalEmailQuery && t == "email")) {
			continue
		}
		if !stopWords[t] {
			terms = append(terms, t)
		}
	}
	if len(terms) == 0 && isHeadQuestion(rawQuery) && wantsAllDepartmentHeads(rawQuery) {
		var heads []models.Employee
		for _, employee := range s.Employees {
			if employeeHasHeadTitle(employee) {
				heads = append(heads, employee)
			}
		}
		if len(heads) > 0 {
			return heads, 5000
		}
	}
	if len(terms) == 0 {
		return nil, 0
	}
	queryText := strings.Join(terms, " ")
	if employmentType := employmentTypeFromQuery(rawQuery); employmentType != "" {
		nameTerms := employmentTypeNameTerms(rawQuery)
		var matches []models.Employee
		for _, employee := range s.Employees {
			if !strings.EqualFold(strings.TrimSpace(employee.EmploymentType), employmentType) {
				continue
			}
			if len(nameTerms) > 0 && !employeeNameMatchesTerms(employee, employeeHeadNameTerms(employee, nameTerms)) {
				continue
			}
			matches = append(matches, employee)
		}
		if len(matches) > 0 {
			return matches, 5000
		}
		return nil, 0
	}
	if initialsQuery(rawQuery) {
		acronym := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(rawQuery), ".", ""))
		isKnownAcronym := false
		for _, employee := range s.Employees {
			if employeeHasDesignationAcronym(employee, acronym) || employeeHasDepartmentAcronym(employee, acronym) {
				isKnownAcronym = true
				break
			}
		}
		var initialMatches []models.Employee
		if !isKnownAcronym {
			for _, employee := range s.Employees {
				if employeeInitials(employee.Name, true) == acronym || employeeInitials(employee.Name, false) == acronym {
					initialMatches = append(initialMatches, employee)
				}
			}
		}
		if len(initialMatches) > 0 {
			return initialMatches, 4500
		}
	}

	// Prefer a name match when all query terms point to one or more names.
	// This allows misspelled employee names to resolve without mixing in other
	// people who share only one token.
	var nameMatches []models.Employee
	nameBestScore := -1
	for _, employee := range s.Employees {
		matched, score := fieldMatchesTerms(terms, employee.Name)
		for _, alias := range employee.NameAliases {
			if aliasMatched, aliasScore := fieldMatchesTerms(terms, alias); aliasMatched && (!matched || aliasScore > score) {
				matched, score = true, aliasScore
			}
		}
		if matched {
			if score > nameBestScore {
				nameMatches = nameMatches[:0]
				nameBestScore = score
			}
			if score == nameBestScore {
				nameMatches = append(nameMatches, employee)
			}
		}
	}
	if len(nameMatches) > 0 {
		return nameMatches, 5000 + nameBestScore
	}

	if wantsAllEditorTypes(rawQuery) {
		department := departmentFromEmployees(rawQuery, s.Employees)
		var editorMatches []models.Employee
		for _, employee := range s.Employees {
			if department != "" && !employeeMatchesDepartment(employee, department) {
				continue
			}
			if employeeHasEditorTitle(employee) {
				editorMatches = append(editorMatches, employee)
			}
		}
		if len(editorMatches) > 0 {
			return editorMatches, 5000
		}
	}

	if isHeadQuestion(rawQuery) {
		topicTerms := headQueryTopicTerms(rawQuery)
		department := departmentFromEmployees(rawQuery, s.Employees)
		// A person name can follow the department in a head query. Resolve
		// department acronyms/names token by token when the full phrase is too
		// specific to match a department record.
		if department == "" {
			for _, term := range topicTerms {
				if candidate := departmentFromEmployees(term, s.Employees); candidate != "" {
					department = candidate
					break
				}
			}
		}
		var heads []models.Employee
		for _, employee := range s.Employees {
			if !employeeHasHeadTitle(employee) {
				continue
			}
			departmentMatch := (department != "" && employeeMatchesDepartment(employee, department)) || employeeHeadDepartmentMatchesTerms(employee, topicTerms)
			titleMatch := headTitleMatchesTerms(employee, topicTerms)
			if (wantsAllDepartmentHeads(rawQuery) || departmentMatch || titleMatch) && employeeNameMatchesTerms(employee, employeeHeadNameTerms(employee, topicTerms)) {
				heads = append(heads, employee)
			}
		}
		if len(heads) > 0 {
			return heads, 5000
		}
		return nil, 0
	}

	// A department name is an exact category key. Designation queries are
	// handled below as title-token matches so titles with added context still
	// belong to the same role category.
	var exactDepartmentMatches []models.Employee
	normalizedQuery := normalizeSearchTokens(queryText)
	for _, employee := range s.Employees {
		if normalizedQuery != "" && employeeMatchesDepartmentQuery(employee, normalizedQuery) {
			exactDepartmentMatches = append(exactDepartmentMatches, employee)
		}
	}
	if len(exactDepartmentMatches) > 0 {
		return exactDepartmentMatches, 5000
	}

	if containsWord(cleaned, "head") && (containsWord(cleaned, "hr") || strings.Contains(cleaned, "human resources")) {
		var matches []models.Employee
		for _, employee := range s.Employees {
			if !containsWord(employee.Designation, "head") {
				continue
			}
			titleAndDepartment := strings.ToLower(employee.Designation + " " + employee.Department)
			isHRRole := containsWord(titleAndDepartment, "hr") || strings.Contains(titleAndDepartment, "human resources")
			if isHRRole {
				matches = append(matches, employee)
			}
		}
		return matches, 5000
	}
	if containsWord(cleaned, "head") {
		if department := departmentFromEmployees(rawQuery, s.Employees); department != "" {
			var matches []models.Employee
			for _, employee := range s.Employees {
				if employeeMatchesDepartment(employee, department) &&
					containsWord(employee.Designation, "head") {
					matches = append(matches, employee)
				}
			}
			return matches, 5000
		}
	}

	for _, term := range terms {
		var acronymMatches []models.Employee
		for _, employee := range s.Employees {
			if employeeHasDesignationAcronym(employee, term) || employeeHasDepartmentAcronym(employee, term) {
				acronymMatches = append(acronymMatches, employee)
			}
		}
		if len(acronymMatches) > 0 {
			return acronymMatches, 5000
		}
	}

	var designationWords []string
	for _, employee := range s.Employees {
		for _, word := range strings.Fields(reNonAlpha.ReplaceAllString(strings.ToLower(employee.Designation), " ")) {
			designationWords = append(designationWords, normalizeSearchTerm(word))
		}
		for _, alias := range employee.DesignationAliases {
			for _, word := range strings.Fields(reNonAlpha.ReplaceAllString(strings.ToLower(alias), " ")) {
				designationWords = append(designationWords, normalizeSearchTerm(word))
			}
		}
	}
	var roleTerms []string
	for _, term := range terms {
		for _, word := range designationWords {
			if wordsMatchWithTypo(term, word) {
				roleTerms = append(roleTerms, term)
				break
			}
		}
	}

	// If a query names both a designation and a department, treat them as
	// required filters. A broad role rule such as "editor" must not override
	// the department constraint (for example, "editor my dhaka").
	if department := departmentFromEmployees(rawQuery, s.Employees); department != "" && len(roleTerms) > 0 {
		departmentWords := strings.Fields(normalizeSearchTokens(department))
		filteredRoleTerms := make([]string, 0, len(roleTerms))
		for _, roleTerm := range roleTerms {
			isDepartmentWord := false
			for _, departmentWord := range departmentWords {
				if editDistanceWithin(roleTerm, departmentWord, typoDistanceLimit(departmentWord)) {
					isDepartmentWord = true
					break
				}
			}
			if !isDepartmentWord {
				filteredRoleTerms = append(filteredRoleTerms, roleTerm)
			}
		}
		var matches []models.Employee
		for _, employee := range s.Employees {
			if !employeeMatchesDepartment(employee, department) {
				continue
			}
			if len(filteredRoleTerms) == 0 || employeeDesignationMatchesTerms(employee, filteredRoleTerms, departmentWords) {
				matches = append(matches, employee)
			}
		}
		if len(matches) > 0 {
			return matches, 5000
		}
		return nil, 0
	}

	if len(roleTerms) > 1 {
		var matches []models.Employee
		for _, employee := range s.Employees {
			if employeeDesignationMatchesTerms(employee, roleTerms, nil) {
				matches = append(matches, employee)
			}
		}
		if len(matches) > 0 {
			return matches, 5000
		}
	}

	if containsWord(cleaned, "it") && (containsWord(cleaned, "team") || containsWord(cleaned, "head")) {
		var matches []models.Employee
		for _, employee := range s.Employees {
			if strings.Contains(strings.ToLower(employee.Department), "information technology") &&
				containsWord(employee.Designation, "head") &&
				containsWord(employee.Designation, "it") {
				matches = append(matches, employee)
			}
		}
		if len(matches) > 0 {
			return matches, 5000
		}
	}

	if len(roleTerms) > 0 && !containsWord(cleaned, "it") && !containsWord(cleaned, "hr") {
		var matches []models.Employee
		for _, employee := range s.Employees {
			if employeeDesignationMatchesTerms(employee, roleTerms, nil) {
				matches = append(matches, employee)
			}
		}
		if len(matches) > 0 {
			return matches, 5000
		}
	}

	isITQuery := cleaned == "it" || containsWord(cleaned, "it")
	isHRQuery := cleaned == "hr" || containsWord(cleaned, "hr")
	isEditorQuery := (cleaned == "editor" || containsWord(cleaned, "editor")) && !strings.Contains(cleaned, "sub") && !strings.Contains(cleaned, "editorial")

	var primaryTerm string
	for _, t := range terms {
		if t != "desk" && t != "department" && t != "team" && len(t) >= 3 {
			primaryTerm = t
			break
		}
	}

	type ScoredEmp struct {
		Emp   models.Employee
		Score int
	}
	var scoredList []ScoredEmp

	for _, e := range s.Employees {
		score := 0
		nameLower := strings.ToLower(e.Name)
		desigLower := strings.ToLower(e.Designation)
		deptLower := strings.ToLower(e.Department)
		joiningDateLower := strings.ToLower(e.JoiningDate)
		serviceLengthLower := strings.ToLower(e.ServiceLength)
		ageLower := strings.ToLower(e.Age)
		bloodGroupLower := strings.ToLower(e.BloodGroup)
		emailLower := strings.ToLower(e.Email)
		personalEmailLower := strings.ToLower(e.PersonalEmail)
		deskLower := strings.ToLower(e.Desk)
		extLower := strings.ToLower(e.Extension)

		// Filter out records that don't match specific term if searching a distinct section (e.g. sports, metro)
		if primaryTerm != "" && !isITQuery && !isHRQuery && !isEditorQuery {
			hasPrimary := strings.Contains(nameLower, primaryTerm) ||
				strings.Contains(desigLower, primaryTerm) ||
				strings.Contains(deptLower, primaryTerm) ||
				strings.Contains(joiningDateLower, primaryTerm) ||
				strings.Contains(serviceLengthLower, primaryTerm) ||
				strings.Contains(ageLower, primaryTerm) ||
				strings.Contains(bloodGroupLower, primaryTerm) ||
				strings.Contains(deskLower, primaryTerm) ||
				strings.Contains(emailLower, primaryTerm) ||
				strings.Contains(personalEmailLower, primaryTerm) ||
				strings.Contains(extLower, primaryTerm)
			if !hasPrimary {
				continue
			}
		}

		// 1. Exact Full Name match
		if nameLower == rawLower || nameLower == cleaned || nameLower == queryText {
			score += 3000
		} else if strings.Contains(nameLower, queryText) && len(queryText) >= 3 {
			score += 1500
		}

		// 2. Editor query handling
		if isEditorQuery {
			if desigLower == "editor & publisher" {
				score += 5000 // Mahfuz Anam, highest priority!
			} else if desigLower == "editor" || strings.HasPrefix(desigLower, "editor,") || strings.HasPrefix(desigLower, "editor ") {
				score += 2500
			} else if strings.Contains(desigLower, "executive editor") || strings.Contains(desigLower, "joint editor") {
				score += 2000
			} else if strings.Contains(desigLower, "assistant editor") {
				score += 800
			} else if strings.Contains(desigLower, "sub-editor") || strings.Contains(desigLower, "sub editor") {
				score += 50
			} else if strings.Contains(desigLower, "editorial assistant") {
				score += 20
			}
		} else {
			// Normal designation match
			if normalizeSearchTokens(desigLower) == normalizeSearchTokens(queryText) {
				score += 2000
			} else if strings.Contains(desigLower, queryText) && len(queryText) >= 3 {
				score += 1000
			}
		}

		// 3. Desk / Location match
		if deskLower != "" {
			if deskLower == queryText {
				score += 1500
			} else if containsWord(deskLower, queryText) || (len(queryText) >= 4 && strings.Contains(deskLower, queryText)) {
				score += 1200
			}
		}

		// 4. Department match
		if normalizeSearchTokens(deptLower) == normalizeSearchTokens(queryText) {
			score += 1500
		} else if containsWord(deptLower, queryText) || (len(queryText) >= 4 && strings.Contains(deptLower, queryText)) {
			score += 1100
		}

		// 5. IT department query specifically
		if isITQuery {
			if strings.Contains(deptLower, "information technology") || containsWord(deskLower, "it") {
				score += 1200
			}
			if strings.Contains(desigLower, "head of it") {
				score += 2000
			}
		}

		// 6. HR department query specifically
		if isHRQuery {
			if strings.Contains(deptLower, "hr") || strings.Contains(deptLower, "human resources") {
				score += 1200
			}
		}

		// 7. Email match
		if emailLower != "" {
			if emailLower == cleaned || emailLower == rawLower {
				score += 3000
			} else if strings.HasPrefix(emailLower, cleaned+"@") || strings.HasPrefix(emailLower, rawLower+"@") {
				score += 2500
			} else if len(cleaned) >= 3 && strings.Contains(emailLower, cleaned) {
				score += 1800
			}
		}

		// 8. ID or phone match
		phoneLower := strings.ToLower(e.Phone)
		empIDLower := strings.ToLower(e.EmployeeID)
		for _, term := range terms {
			if len(term) >= 3 && (strings.Contains(extLower, term) || (phoneLower != "" && strings.Contains(phoneLower, term)) || (empIDLower != "" && empIDLower == term)) {
				score += 2000
			}
		}

		// 9. Multi-term matching across fields
		matchedTerms := 0
		for _, term := range terms {
			if len(term) < 2 {
				continue
			}
			tMatch := false
			if len(term) <= 3 {
				if containsWord(nameLower, term) {
					score += 250
					tMatch = true
				}
				if containsWord(desigLower, term) {
					score += 200
					tMatch = true
				}
				if containsWord(deptLower, term) || containsWord(deskLower, term) {
					score += 200
					tMatch = true
				}
			} else {
				if strings.Contains(nameLower, term) {
					score += 250
					tMatch = true
				}
				if strings.Contains(desigLower, term) {
					score += 200
					tMatch = true
				}
				if strings.Contains(deptLower, term) || strings.Contains(deskLower, term) {
					score += 350
					tMatch = true
				}
				if strings.Contains(joiningDateLower, term) || strings.Contains(bloodGroupLower, term) {
					score += 350
					tMatch = true
				}
			}
			if tMatch {
				matchedTerms++
			}
		}

		// Bonus if multiple terms all matched
		if len(terms) >= 2 && matchedTerms == len(terms) {
			score += 500
		}

		if score > 0 {
			scoredList = append(scoredList, ScoredEmp{Emp: e, Score: score})
		}
	}

	if len(scoredList) == 0 {
		return nil, 0
	}

	sort.Slice(scoredList, func(i, j int) bool {
		return scoredList[i].Score > scoredList[j].Score
	})

	topScore := scoredList[0].Score
	var res []models.Employee
	exactCategoryMatch := false
	for _, employee := range s.Employees {
		if normalizeSearchTokens(employee.Department) == normalizeSearchTokens(queryText) ||
			normalizeSearchTokens(employee.Designation) == normalizeSearchTokens(queryText) {
			exactCategoryMatch = true
			break
		}
	}
	for _, se := range scoredList {
		if se.Score >= 400 && se.Score >= int(float64(topScore)*0.45) {
			res = append(res, se.Emp)
			if !exactCategoryMatch && len(res) >= 25 {
				break
			}
		}
	}
	return res, topScore
}

func (s *Store) DepartmentFromQuery(query string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return departmentFromEmployees(query, s.Employees)
}

func departmentFromEmployees(query string, employees []models.Employee) string {
	queryTerms := strings.Fields(normalizeSearchText(query))
	departments := make(map[string]bool)
	departmentVocabulary := make(map[string]bool)
	roleWords := make(map[string]int)
	for _, employee := range employees {
		department := strings.TrimSpace(employee.Department)
		if department != "" {
			departments[department] = true
			for _, word := range strings.Fields(normalizeSearchText(department)) {
				departmentVocabulary[word] = true
			}
		}
		for _, alias := range employee.DepartmentAliases {
			alias = strings.TrimSpace(alias)
			if alias != "" {
				departments[alias] = true
				for _, word := range strings.Fields(normalizeSearchText(alias)) {
					departmentVocabulary[word] = true
				}
			}
		}
		title := strings.SplitN(employee.Designation, ",", 2)[0]
		for _, word := range strings.Fields(normalizeSearchText(title)) {
			roleWords[normalizeSearchTerm(word)]++
		}
	}
	var meaningfulQueryTerms []string
	for _, term := range queryTerms {
		term = normalizeSearchTerm(term)
		if !stopWords[term] || departmentVocabulary[term] {
			meaningfulQueryTerms = append(meaningfulQueryTerms, term)
		}
	}
	var completeMatches []string
	partialMatches := make(map[string]int)
	for department := range departments {
		if len(meaningfulQueryTerms) == 1 && len(departmentAcronym(department)) >= 2 && meaningfulQueryTerms[0] == departmentAcronym(department) {
			completeMatches = append(completeMatches, department)
			continue
		}
		departmentTerms := strings.Fields(normalizeSearchText(department))
		used := make([]bool, len(meaningfulQueryTerms))
		matchedDepartmentWords := 0
		for _, departmentTerm := range departmentTerms {
			for i, queryTerm := range meaningfulQueryTerms {
				if !used[i] && editDistanceWithin(departmentTerm, queryTerm, typoDistanceLimit(departmentTerm)) {
					used[i] = true
					matchedDepartmentWords++
					break
				}
			}
		}
		if len(departmentTerms) > 0 && matchedDepartmentWords == len(departmentTerms) {
			completeMatches = append(completeMatches, department)
		} else {
			matchedNonRoleWords := 0
			for i, matched := range used {
				if matched && roleWords[meaningfulQueryTerms[i]] < 3 {
					matchedNonRoleWords++
				}
			}
			if matchedNonRoleWords > 0 {
				partialMatches[department] = matchedNonRoleWords
			}
		}
	}
	if len(completeMatches) > 0 {
		sort.Slice(completeMatches, func(i, j int) bool { return len(completeMatches[i]) > len(completeMatches[j]) })
		return completeMatches[0]
	}
	bestCount, bestDepartment := 0, ""
	ambiguous := false
	for department, count := range partialMatches {
		if count > bestCount {
			bestCount, bestDepartment, ambiguous = count, department, false
		} else if count == bestCount {
			ambiguous = true
		}
	}
	if !ambiguous {
		return bestDepartment
	}
	return ""
}

func designationMatchesTerms(designation string, terms []string) bool {
	return designationMatchesTermsInDepartment(designation, terms, nil)
}

func employeeDesignationMatchesTerms(employee models.Employee, terms, departmentWords []string) bool {
	if designationMatchesTermsInDepartment(employee.Designation, terms, departmentWords) {
		return true
	}
	for _, alias := range employee.DesignationAliases {
		if designationMatchesTermsInDepartment(alias, terms, departmentWords) {
			return true
		}
	}
	return false
}

func employeeMatchesDepartment(employee models.Employee, department string) bool {
	if strings.EqualFold(strings.TrimSpace(employee.Department), strings.TrimSpace(department)) {
		return true
	}
	for _, alias := range employee.DepartmentAliases {
		if strings.EqualFold(strings.TrimSpace(alias), strings.TrimSpace(department)) {
			return true
		}
	}
	return false
}

func employeeMatchesDepartmentQuery(employee models.Employee, normalizedQuery string) bool {
	if normalizeSearchTokens(employee.Department) == normalizedQuery {
		return true
	}
	for _, alias := range employee.DepartmentAliases {
		if normalizeSearchTokens(alias) == normalizedQuery {
			return true
		}
	}
	return false
}

func designationMatchesTermsInDepartment(designation string, terms, departmentWords []string) bool {
	// Only the title before the comma defines the role. Later text such as
	// "Campus, Rising Stars & Star Youth" is organizational context, and
	// hyphenated titles such as "Sub-Editor" must not match the exact title
	// "Editor".
	title := strings.SplitN(designation, ",", 2)[0]
	fieldWords := strings.Fields(normalizeSearchText(title))
	var normalizedFieldWords []string
	for _, word := range fieldWords {
		word = normalizeSearchTerm(word)
		isDepartmentContext := false
		for _, departmentWord := range departmentWords {
			if word == departmentWord {
				isDepartmentContext = true
				break
			}
		}
		if !stopWords[word] && !isDepartmentContext {
			normalizedFieldWords = append(normalizedFieldWords, word)
		}
	}
	if len(terms) == 1 {
		// "Editor" is an exact title category: don't include Editor & Publisher,
		// Editor In-charge, Joint Editor, Assistant Editor, or Sub-Editor when
		// the user asks for Editor. Department context after a comma was removed
		// above, so "Editor, My Dhaka" still matches this exact role.
		if terms[0] == "editor" {
			return len(normalizedFieldWords) == 1 && wordsMatchWithTypo(terms[0], normalizedFieldWords[0])
		}
		for _, word := range normalizedFieldWords {
			if wordsMatchWithTypo(terms[0], word) {
				return true
			}
		}
		return false
	}
	if len(normalizedFieldWords) != len(terms) {
		return false
	}
	for i, term := range terms {
		if !wordsMatchWithTypo(term, normalizedFieldWords[i]) {
			return false
		}
	}
	return len(terms) > 0
}

func (s *Store) EmployeesByDepartment(department string) []models.Employee {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var employees []models.Employee
	for _, employee := range s.Employees {
		if employeeMatchesDepartment(employee, department) {
			employees = append(employees, employee)
		}
	}
	sort.Slice(employees, func(i, j int) bool {
		return strings.ToLower(employees[i].Name) < strings.ToLower(employees[j].Name)
	})
	return employees
}

func (s *Store) DepartmentCounts() []DepartmentCount {
	s.mu.RLock()
	defer s.mu.RUnlock()

	counts := make(map[string]int)
	for _, employee := range s.Employees {
		department := strings.TrimSpace(employee.Department)
		if department == "" {
			department = "Not specified"
		}
		counts[department]++
	}
	result := make([]DepartmentCount, 0, len(counts))
	for department, count := range counts {
		result = append(result, DepartmentCount{Department: department, Count: count})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Count == result[j].Count {
			return strings.ToLower(result[i].Department) < strings.ToLower(result[j].Department)
		}
		return result[i].Count > result[j].Count
	})
	return result
}

func normalizeSearchText(text string) string {
	text = strings.NewReplacer(
		"human resources", "hr admin",
		"human resource", "hr admin",
		"manob shompod", "hr admin",
		"manobshompod", "hr admin",
		"\u09ae\u09be\u09a8\u09ac\u09b8\u09ae\u09cd\u09aa\u09a6", "hr admin",
	).Replace(strings.ToLower(text))
	return strings.Join(strings.Fields(reNonAlpha.ReplaceAllString(text, " ")), " ")
}

func normalizeSearchTokens(text string) string {
	tokens := strings.Fields(normalizeSearchText(text))
	for i := range tokens {
		tokens[i] = normalizeSearchTerm(tokens[i])
	}
	return strings.Join(tokens, " ")
}

func initialsQuery(query string) bool {
	query = strings.TrimSpace(query)
	letters := strings.ReplaceAll(query, ".", "")
	letters = strings.ToLower(letters)
	switch letters {
	case "hr", "it", "cbo", "ceo", "cfo", "cto", "cmo", "coo":
		return false
	}
	return len([]rune(letters)) >= 2 && strings.EqualFold(query, strings.ToUpper(query)) && !strings.ContainsAny(query, " ,?!")
}

func employeeInitials(name string, firstAndLastOnly bool) string {
	words := strings.Fields(normalizeSearchText(name))
	if len(words) == 0 {
		return ""
	}
	var initials strings.Builder
	if firstAndLastOnly {
		initials.WriteString(words[0][:1])
		if len(words) > 1 {
			initials.WriteString(words[len(words)-1][:1])
		}
		return initials.String()
	}
	for _, word := range words {
		initials.WriteString(word[:1])
	}
	return initials.String()
}

func designationAcronym(designation string) string {
	// HR exports sometimes append a team/context after a comma. Acronyms refer
	// to the actual job title only (e.g. CBO, not CBOBD).
	title := strings.SplitN(designation, ",", 2)[0]
	return makeAcronym(title)
}

func departmentAcronym(department string) string {
	return makeAcronym(department)
}

func employeeHasDesignationAcronym(employee models.Employee, acronym string) bool {
	if len(acronym) >= 2 && designationAcronym(employee.Designation) == acronym {
		return true
	}
	for _, alias := range employee.DesignationAliases {
		if len(acronym) >= 2 && designationAcronym(alias) == acronym {
			return true
		}
	}
	return false
}

func employeeHasDepartmentAcronym(employee models.Employee, acronym string) bool {
	if len(acronym) >= 2 && departmentAcronym(employee.Department) == acronym {
		return true
	}
	for _, alias := range employee.DepartmentAliases {
		if len(acronym) >= 2 && departmentAcronym(alias) == acronym {
			return true
		}
	}
	return false
}

func makeAcronym(value string) string {
	var acronym strings.Builder
	for _, word := range strings.Fields(reNonAlpha.ReplaceAllString(strings.ToLower(value), " ")) {
		if len([]rune(word)) > 0 {
			acronym.WriteRune([]rune(word)[0])
		}
	}
	return acronym.String()
}

// SemanticSearch embeds the query and returns the topK most similar
// employees by cosine similarity, with an optional minimum similarity threshold.
func (s *Store) SemanticSearch(client *ollama.Client, query string, topK int, minScore float64) ([]models.Employee, error) {
	qEmb, err := client.Embed(query)
	if err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	type scored struct {
		emp   models.Employee
		score float64
	}
	var results []scored
	for _, e := range s.Employees {
		if len(e.Embedding) == 0 {
			continue
		}
		sim := cosineSimilarity(qEmb, e.Embedding)
		if sim >= minScore {
			results = append(results, scored{emp: e, score: sim})
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].score > results[j].score })

	if topK > len(results) {
		topK = len(results)
	}
	out := make([]models.Employee, topK)
	for i := 0; i < topK; i++ {
		out[i] = results[i].emp
	}
	return out, nil
}

// Count returns how many employees are currently indexed.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.Employees)
}

func (s *Store) DataQualityReport() DataQuality {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := DataQuality{Employees: len(s.Employees)}
	seenIDs := make(map[string]bool)
	seenNames := make(map[string]bool)
	for _, employee := range s.Employees {
		nameKey := normalizeSearchTokens(employee.Name)
		if nameKey != "" {
			if seenNames[nameKey] {
				result.DuplicateNames++
			} else {
				seenNames[nameKey] = true
			}
		}
		if len(employee.NameAliases)+len(employee.DesignationAliases)+len(employee.DepartmentAliases) > 0 {
			result.EmployeesWithAliases++
		}
		if strings.TrimSpace(employee.Name) == "" {
			result.MissingName++
		}
		if strings.TrimSpace(employee.Designation) == "" {
			result.MissingDesignation++
		}
		if strings.TrimSpace(employee.Department) == "" {
			result.MissingDepartment++
		}
		if strings.TrimSpace(employee.JoiningDate) == "" {
			result.MissingJoiningDate++
		} else if !validJoiningDate(employee.JoiningDate) {
			result.InvalidJoiningDates++
		}
		if strings.TrimSpace(employee.Age) == "" {
			result.MissingAge++
		}
		if strings.TrimSpace(employee.ServiceLength) == "" {
			result.MissingServiceLength++
		}
		if strings.TrimSpace(employee.PersonalEmail) == "" {
			result.MissingPersonalEmail++
		}
		if strings.TrimSpace(employee.EmploymentType) == "" {
			result.MissingEmploymentType++
		}
		if strings.TrimSpace(employee.WhatsAppNumber) == "" && strings.TrimSpace(employee.Phone) == "" {
			result.MissingWhatsAppNumber++
		}
		if strings.TrimSpace(employee.BloodGroup) == "" {
			result.MissingBloodGroup++
		} else if !validBloodGroup(employee.BloodGroup) {
			result.InvalidBloodGroups++
		}
		if strings.TrimSpace(employee.RecordUpdatedAt) == "" {
			result.MissingRecordUpdatedAt++
		} else if !validJoiningDate(employee.RecordUpdatedAt) {
			result.InvalidRecordUpdatedAt++
		}
		if !validEmploymentStatus(employee.EmploymentStatus) {
			result.InvalidStatuses++
		}
		id := strings.TrimSpace(employee.EmployeeID)
		if id != "" {
			if seenIDs[id] {
				result.DuplicateEmployeeIDs++
			} else {
				seenIDs[id] = true
			}
		}
	}
	return result
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return -1
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
