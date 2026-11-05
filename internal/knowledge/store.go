// Package knowledge indexes approved company documents separately from the
// employee directory. It supports Markdown, plain text, and CSV source files.
package knowledge

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"dailystar-chatbot/internal/ollama"
)

const schemaVersion = 2

type Chunk struct {
	Source    string    `json:"source"`
	Section   string    `json:"section,omitempty"`
	Text      string    `json:"text"`
	Embedding []float32 `json:"embedding,omitempty"`
}

type Result struct {
	Chunk Chunk
	Score float64
}

type cache struct {
	SchemaVersion int     `json:"schema_version"`
	EmbedModel    string  `json:"embed_model"`
	Chunks        []Chunk `json:"chunks"`
}

type Store struct {
	mu         sync.RWMutex
	chunks     []Chunk
	embedModel string
	sourceDir  string
	indexPath  string
}

func New(sourceDir, indexPath string) *Store {
	return &Store{sourceDir: sourceDir, indexPath: indexPath}
}

func (s *Store) Load() error {
	b, err := os.ReadFile(s.indexPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var saved cache
	if err := json.Unmarshal(b, &saved); err != nil {
		return err
	}
	if saved.SchemaVersion != schemaVersion {
		return nil
	}
	s.mu.Lock()
	s.chunks = saved.Chunks
	s.embedModel = saved.EmbedModel
	s.mu.Unlock()
	return nil
}

// Sync rebuilds chunks from approved source files, reusing vectors for
// unchanged source sections. It commits the new cache only after every embed
// request succeeds, so a failed rebuild leaves the last good index intact.
func (s *Store) Sync(client *ollama.Client) error {
	if err := os.MkdirAll(s.sourceDir, 0o755); err != nil {
		return err
	}
	files, err := sourceFiles(s.sourceDir)
	if err != nil {
		return err
	}
	old := make(map[string][]float32)
	s.mu.RLock()
	cacheModel := s.embedModel
	targetModel := cacheModel
	if client != nil {
		targetModel = client.EmbedModel
	}
	if cacheModel == targetModel {
		for _, chunk := range s.chunks {
			old[chunkKey(chunk)] = chunk.Embedding
		}
	}
	s.mu.RUnlock()
	if len(old) == 0 {
		if b, readErr := os.ReadFile(s.indexPath); readErr == nil {
			var saved cache
			if json.Unmarshal(b, &saved) == nil && saved.SchemaVersion == schemaVersion && saved.EmbedModel == targetModel {
				for _, chunk := range saved.Chunks {
					old[chunkKey(chunk)] = chunk.Embedding
				}
			}
		}
	}

	var chunks []Chunk
	for _, path := range files {
		rel, _ := filepath.Rel(s.sourceDir, path)
		contents, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read knowledge source %s: %w", rel, err)
		}
		parts, err := parseSource(filepath.ToSlash(rel), filepath.Ext(path), contents)
		if err != nil {
			return fmt.Errorf("parse knowledge source %s: %w", rel, err)
		}
		chunks = append(chunks, parts...)
	}

	for i := range chunks {
		if embedding := old[chunkKey(chunks[i])]; len(embedding) > 0 {
			chunks[i].Embedding = embedding
			continue
		}
		if client == nil {
			return errors.New("Ollama is required to index new or changed knowledge sources")
		}
		embedding, err := client.Embed(chunks[i].Text)
		if err != nil {
			return fmt.Errorf("embed %s (%s): %w", chunks[i].Source, chunks[i].Section, err)
		}
		chunks[i].Embedding = embedding
	}

	encoded, err := json.Marshal(cache{SchemaVersion: schemaVersion, EmbedModel: targetModel, Chunks: chunks})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.indexPath), 0o755); err != nil {
		return err
	}
	tmp := s.indexPath + ".tmp"
	if err := os.WriteFile(tmp, encoded, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.indexPath); err != nil {
		backup := s.indexPath + ".bak"
		_ = os.Remove(backup)
		hadOldIndex := false
		if _, statErr := os.Stat(s.indexPath); statErr == nil {
			if moveErr := os.Rename(s.indexPath, backup); moveErr != nil {
				_ = os.Remove(tmp)
				return err
			}
			hadOldIndex = true
		}
		if retryErr := os.Rename(tmp, s.indexPath); retryErr != nil {
			if hadOldIndex {
				_ = os.Rename(backup, s.indexPath)
			}
			_ = os.Remove(tmp)
			return retryErr
		}
		_ = os.Remove(backup)
	}
	s.mu.Lock()
	s.chunks = chunks
	s.embedModel = targetModel
	s.mu.Unlock()
	return nil
}

// Clear disables a stale knowledge cache when source documents changed but
// could not be re-embedded. The previous file remains available for recovery.
func (s *Store) Clear() {
	s.mu.Lock()
	s.chunks = nil
	s.mu.Unlock()
}

func (s *Store) Search(client *ollama.Client, query string, topK int, minScore float64) ([]Result, error) {
	if client == nil {
		return nil, errors.New("Ollama is not configured for knowledge search")
	}
	queryEmbedding, err := client.Embed(query)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	results := make([]Result, 0, len(s.chunks))
	for _, chunk := range s.chunks {
		if len(chunk.Embedding) == 0 {
			continue
		}
		score := cosine(queryEmbedding, chunk.Embedding)
		if score >= minScore {
			results = append(results, Result{Chunk: chunk, Score: score})
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	if topK < 0 {
		topK = 0
	}
	if topK < len(results) {
		results = results[:topK]
	}
	return results, nil
}

func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.chunks)
}

func sourceFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".md", ".txt", ".csv":
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

func parseSource(source, ext string, raw []byte) ([]Chunk, error) {
	if strings.EqualFold(ext, ".csv") {
		return parseCSV(source, raw)
	}
	return parseText(source, string(raw)), nil
}

func parseCSV(source string, raw []byte) ([]Chunk, error) {
	reader := csv.NewReader(strings.NewReader(string(raw)))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(records) < 2 {
		return nil, nil
	}
	var chunks []Chunk
	for i, record := range records[1:] {
		var fields []string
		for col, value := range record {
			if strings.TrimSpace(value) == "" {
				continue
			}
			label := fmt.Sprintf("Column %d", col+1)
			if col < len(records[0]) && strings.TrimSpace(records[0][col]) != "" {
				label = strings.TrimSpace(records[0][col])
			}
			fields = append(fields, label+": "+strings.TrimSpace(value))
		}
		if len(fields) > 0 {
			chunks = append(chunks, Chunk{Source: source, Section: fmt.Sprintf("Row %d", i+2), Text: strings.Join(fields, ". ")})
		}
	}
	return chunks, nil
}

func parseText(source, contents string) []Chunk {
	var chunks []Chunk
	section := ""
	var current strings.Builder
	flush := func() {
		text := strings.TrimSpace(current.String())
		current.Reset()
		if text != "" {
			chunks = append(chunks, Chunk{Source: source, Section: section, Text: text})
		}
	}
	for _, line := range strings.Split(strings.ReplaceAll(contents, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			flush()
			section = strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			continue
		}
		if trimmed == "" {
			flush()
			continue
		}
		if current.Len() > 0 && current.Len()+len(trimmed)+1 > 1100 {
			flush()
		}
		if current.Len() > 0 {
			current.WriteByte('\n')
		}
		current.WriteString(trimmed)
	}
	flush()
	return chunks
}

func chunkKey(chunk Chunk) string {
	return chunk.Source + "\x00" + chunk.Section + "\x00" + chunk.Text
}

func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
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
