# Staff Directory Chatbot (Go + Ollama, 100% free)

An internal RAG-style chatbot: type an employee's name and get their
work-related directory info (designation, department, extension, email,
desk). Falls back to AI-assisted search for broader questions like
*"who is on the metro desk"*.

**Cost: ৳0.** Everything runs locally — Go standard library only (no
external Go packages), and Ollama for the AI model (no API key, no
per-request charge). Your HR data never leaves your own server.

## How it works

1. HR's employee list (CSV) is loaded once.
2. Each employee gets a short text description, which is turned into a
   vector **embedding** by a local Ollama model and cached to
   `data/employees_index.json`.
3. When someone types a **name**, the bot does a direct, exact/substring
   match against the directory and returns the real stored data —
   no AI guessing involved, so it can't hallucinate wrong details.
4. When someone asks a broader **question**, the bot finds the most
   relevant employee records by embedding similarity and asks the local
   LLM to answer using *only* those records as context.

## 1. Install Ollama (one-time, free)

```bash
curl -fsSL https://ollama.com/install.sh | sh
```

Then pull the two models used by this project:

```bash
ollama pull nomic-embed-text   # for embeddings
ollama pull llama3.1           # for chat answers (or: mistral, llama3.2, etc.)
```

Ollama runs as a local service on `http://localhost:11434` — nothing
leaves your machine/server.

## 2. Add your real HR data

Edit `data/employees.csv`. Required column: `name`. Optional columns:
`designation`, `department`, `extension`, `email`, `desk`. Keep this to
work-related fields only, as agreed with HR.

```csv
name,designation,department,extension,email,desk
Rahim Uddin,Staff Reporter,Metro Desk,2101,rahim.uddin@example.com,3rd Floor
```

## 3. Build and run

```bash
go build -o staffbot .
./staffbot
```

First run will call Ollama once per employee to build the search index
(this can take a few minutes for a large staff list) and cache it to
`data/employees_index.json`. Later restarts load instantly from that
cache.

Open **http://127.0.0.1:8080** in a browser. The service binds to localhost by
default, so staff data is not exposed to the network accidentally.

## 4. Updating data later

Whenever HR updates `data/employees.csv`, restart the app (it detects a newer
CSV), or re-index without restarting using an administrator token:

```bash
curl -X POST -H "X-Admin-Token: $ADMIN_TOKEN" http://127.0.0.1:8080/api/reload
```

## Configuration (environment variables, all optional)

| Variable       | Default                        | Purpose                                                      |
|----------------|--------------------------------|--------------------------------------------------------------|
| `GROQ_API_KEY` | *(empty)*                      | Free Groq Cloud API key for 300+ tok/s ultra-fast LLaMA 3.3  |
| `GROQ_MODEL`   | `llama-3.3-70b-versatile`      | Groq model (`llama-3.3-70b-versatile`, `llama-3.1-8b-instant`)|
| `OLLAMA_URL`   | `http://localhost:11434`       | Local Ollama address (used for embeddings & backup chat)      |
| `EMBED_MODEL`  | `nomic-embed-text`             | Embedding model                                              |
| `CHAT_MODEL`   | `llama3.1`                     | Local fallback chat model                                    |
| `CSV_PATH`     | `data/employees.csv`           | Source HR file                                               |
| `DB_PATH`      | `data/employees_index.json`    | Cached index file                                            |
| `PORT`         | `8080`                         | HTTP port                                                    |
| `HOST`         | `127.0.0.1`                    | Bind address; local-only by default                          |
| `ACCESS_TOKEN` | *(empty)*                      | Required `X-Access-Token` when exposing the service remotely |
| `ADMIN_TOKEN`  | *(empty)*                      | Required `X-Admin-Token` for reload and AI settings          |

## Security and privacy

- The server listens on `127.0.0.1` by default. Do not change `HOST` to a
  network address without setting both `ACCESS_TOKEN` and `ADMIN_TOKEN`.
- Directory lookup responses are generated from stored fields, not an LLM;
  employee records are not sent to Groq.
- The administrator token is required for re-indexing and Groq configuration.
- A Groq key entered through the browser is held only for the current process;
  use a protected server environment variable if it must persist after restart.
- Keep `.env`, the CSV, and the generated index out of source control.

### ⚡ Ultra-Fast Groq Cloud LPU Integration (100% Free)

You can activate **Groq Cloud API** for instant (300-500 tokens/sec) LLaMA-3.3 70B inference:
1. Get a **100% free** API key at [console.groq.com/keys](https://console.groq.com/keys) (no credit card needed).
2. Either click the **"⚡ Groq API Settings"** button directly in the Web UI, or set `GROQ_API_KEY=gsk_...` in your `.env` file.
3. The chatbot will automatically use Groq for lightning-fast RAG answers and seamlessly fall back to local Ollama if offline!

## Running as a background service (optional)

Example `systemd` unit (`/etc/systemd/system/staffbot.service`):

```ini
[Unit]
Description=Daily Star Staff Directory Chatbot
After=network.target ollama.service

[Service]
WorkingDirectory=/opt/staffbot
ExecStart=/opt/staffbot/staffbot
Restart=on-failure
Environment=PORT=8080

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now staffbot
```

## Project layout

```
.
├── main.go                     # entry point, config, startup
├── internal/
│   ├── models/employee.go      # Employee struct
│   ├── ollama/client.go        # calls Ollama's local HTTP API
│   ├── store/store.go          # CSV loading, JSON cache, search
│   └── server/server.go        # HTTP handlers (/api/chat, /api/reload)
├── web/index.html              # chat UI (embedded into the binary)
└── data/employees.csv          # sample data — replace with real HR export
```

## Privacy notes

- Only work-related fields are stored by design (no phone numbers, home
  addresses, or national ID numbers) — keep it that way unless HR/legal
  explicitly signs off on storing more.
- Because Ollama runs locally, no employee data is sent to any outside
  company or API.
- Access to this tool should still be restricted to your office network
  or behind company login, since it is still a lookup of staff data.
