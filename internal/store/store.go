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
			e.Text = e.BuildText()
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
	return cacheErr != nil || csvErr != nil || csvInfo.ModTime().After(cacheInfo.ModTime())
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

// BuildFromCSV reads the HR export CSV (columns: name,designation,department,
// extension,email,desk), builds a text description for each row, fetches an
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

	var employees []models.Employee
	for i, row := range rows[1:] {
		e := models.Employee{
			ID:          fmt.Sprintf("emp-%d", i+1),
			Name:        get(row, "name"),
			Designation: get(row, "designation"),
			Department:  get(row, "department"),
			Extension:   get(row, "extension"),
			Email:       get(row, "email"),
			Desk:        get(row, "desk"),
			LinkedIn:    get(row, "linkedin"),
			Twitter:     get(row, "twitter"),
			Facebook:    get(row, "facebook"),
			Instagram:   get(row, "instagram"),
		}
		if e.Name == "" {
			continue
		}
		e.Email = ""
		e.Desk = ""
		e.NormalizeDirectoryFields(row)
		e.Text = e.BuildText()

		emb, err := client.Embed(e.Text)
		if err != nil {
			return fmt.Errorf("embedding failed for %s: %w", e.Name, err)
		}
		e.Embedding = emb
		employees = append(employees, e)
		log.Printf("indexed %d/%d: %s", i+1, len(rows)-1, e.Name)
	}

	s.mu.Lock()
	s.Employees = employees
	s.mu.Unlock()

	return s.SaveJSON()
}

var reNonAlpha = regexp.MustCompile(`[^a-zA-Z0-9\s]+`)

var stopWords = map[string]bool{
	"who": true, "is": true, "the": true, "in": true, "on": true, "at": true,
	"for": true, "and": true, "to": true, "of": true, "a": true, "an": true,
	"all": true, "list": true, "tell": true, "me": true, "about": true,
	"er": true, "ke": true, "kake": true, "ache": true, "kothay": true,
	"daw": true, "dao": true, "details": true, "contact": true, "phone": true,
	"email": true, "number": true, "please": true, "give": true, "show": true, "info": true,
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
// It matches names, designations, departments, desks, emails, extensions, and phone numbers.
func (s *Store) Search(rawQuery string) ([]models.Employee, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rawLower := strings.ToLower(strings.TrimSpace(rawQuery))
	cleaned := CleanQuery(rawQuery)
	if cleaned == "" {
		cleaned = rawLower
	}

	rawTerms := strings.Fields(reNonAlpha.ReplaceAllString(cleaned, " "))
	var terms []string
	for _, t := range rawTerms {
		if !stopWords[t] {
			terms = append(terms, t)
		}
	}
	if len(terms) == 0 {
		terms = rawTerms
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
		emailLower := strings.ToLower(e.Email)
		deskLower := strings.ToLower(e.Desk)
		extLower := strings.ToLower(e.Extension)

		// Filter out records that don't match specific term if searching a distinct section (e.g. sports, metro)
		if primaryTerm != "" && !isITQuery && !isHRQuery && !isEditorQuery {
			hasPrimary := strings.Contains(nameLower, primaryTerm) ||
				strings.Contains(desigLower, primaryTerm) ||
				strings.Contains(deptLower, primaryTerm) ||
				strings.Contains(deskLower, primaryTerm) ||
				strings.Contains(emailLower, primaryTerm) ||
				strings.Contains(extLower, primaryTerm)
			if !hasPrimary {
				continue
			}
		}

		// 1. Exact Full Name match
		if nameLower == rawLower || nameLower == cleaned {
			score += 3000
		} else if strings.Contains(nameLower, cleaned) && len(cleaned) >= 3 {
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
			if desigLower == cleaned {
				score += 2000
			} else if strings.Contains(desigLower, cleaned) && len(cleaned) >= 3 {
				score += 1000
			}
		}

		// 3. Desk / Location match
		if deskLower != "" {
			if deskLower == cleaned {
				score += 1500
			} else if containsWord(deskLower, cleaned) || (len(cleaned) >= 4 && strings.Contains(deskLower, cleaned)) {
				score += 1200
			}
		}

		// 4. Department match
		if deptLower == cleaned {
			score += 1500
		} else if containsWord(deptLower, cleaned) || (len(cleaned) >= 4 && strings.Contains(deptLower, cleaned)) {
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
	for _, se := range scoredList {
		if se.Score >= 400 && se.Score >= int(float64(topScore)*0.45) {
			res = append(res, se.Emp)
			if len(res) >= 25 {
				break
			}
		}
	}
	return res, topScore
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
