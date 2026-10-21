package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"dailystar-chatbot/internal/groq"
	"dailystar-chatbot/internal/ollama"
	"dailystar-chatbot/internal/server"
	"dailystar-chatbot/internal/store"
)

//go:embed web/*
var webFiles embed.FS

func loadEnv(filename string) {
	b, err := os.ReadFile(filename)
	if err != nil {
		return
	}
	lines := strings.Split(string(b), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			k := strings.TrimSpace(parts[0])
			v := strings.Trim(strings.TrimSpace(parts[1]), "\"'")
			if os.Getenv(k) == "" {
				os.Setenv(k, v)
			}
		}
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	envPath := ".env"
	loadEnv(envPath)

	ollamaURL := getenv("OLLAMA_URL", "http://localhost:11434")
	embedModel := getenv("EMBED_MODEL", "nomic-embed-text")
	chatModel := getenv("CHAT_MODEL", "llama3.1")
	csvPath := getenv("CSV_PATH", "data/employees.csv")
	dbPath := getenv("DB_PATH", "data/employees_index.json")
	port := getenv("PORT", "8080")
	host := getenv("HOST", "127.0.0.1")
	accessToken := getenv("ACCESS_TOKEN", "")
	adminToken := getenv("ADMIN_TOKEN", "")
	groqKey := getenv("GROQ_API_KEY", "")
	groqModel := getenv("GROQ_MODEL", "openai/gpt-oss-120b")

	client := ollama.New(ollamaURL, embedModel, chatModel)
	grClient := groq.New(groqKey, groqModel)
	st := store.New(dbPath)

	loaded, err := st.LoadJSON()
	if err != nil {
		log.Fatalf("failed to load cached index: %v", err)
	}
	if !loaded {
		log.Println("no cached index found, building from CSV (this calls Ollama once per employee)...")
		if err := st.BuildFromCSV(csvPath, client); err != nil {
			log.Fatalf("failed to build index from CSV: %v", err)
		}
	} else if st.NeedsRebuild(csvPath) {
		log.Println("CSV is newer than the cached index; rebuilding embeddings...")
		if err := st.BuildFromCSV(csvPath, client); err != nil {
			log.Printf("re-index skipped because Ollama is unavailable; serving the existing cached directory: %v", err)
		}
	}
	log.Printf("ready: %d employees indexed", st.Count())

	webSub, err := fs.Sub(webFiles, "web")
	if err != nil {
		log.Fatalf("failed to load embedded web assets: %v", err)
	}

	if host != "127.0.0.1" && host != "localhost" && host != "::1" && accessToken == "" {
		log.Fatal("ACCESS_TOKEN must be set when HOST is not a loopback address")
	}
	srv := server.New(st, client, grClient, csvPath, envPath, webSub, accessToken, adminToken)

	log.Printf("Daily Star staff-directory chatbot listening on %s:%s", host, port)
	if grClient.IsConfigured() {
		log.Printf("  AI Engine:  ⚡ Groq Cloud LPU (%s) [ACTIVE - Ultra Fast]", groqModel)
		log.Printf("  Fallback:   💻 Ollama %s (%s)", ollamaURL, chatModel)
	} else {
		log.Printf("  AI Engine:  💻 Ollama Local %s (chat=%s, embed=%s)", ollamaURL, chatModel, embedModel)
		log.Printf("  Groq LPU:   (Not configured. Set GROQ_API_KEY in .env or via Web UI to enable ultra-fast inference)")
	}
	log.Printf("  POST /api/reload to re-index after HR updates the CSV")
	httpServer := &http.Server{
		Addr:              host + ":" + port,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      90 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
