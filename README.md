# Staff Directory Chatbot (Go + Ollama, 100% free)

An internal RAG-style chatbot: type an employee's name and get their
work-related directory info (designation, department, extension, email,
desk). Falls back to AI-assisted search for broader questions like
*"who is on the metro desk"*.

**Cost: ৳0.** The Go app uses the standard library, and Ollama handles local
embeddings. When Groq is configured, broader AI-assisted questions send the
selected standard directory context to that provider. Direct lookups stay in
the local directory; personal email, age, and service length are excluded from
embedding and semantic LLM context.

## How it works

1. HR's employee list (CSV) is loaded once.
2. Each employee gets a short text description, which is turned into a
   vector **embedding** by a local Ollama model and cached to
   `data/employees_index.json`.
3. Directory questions are matched directly against names, designations,
   departments, desks, and contact fields. Common English, Banglish, and
   Bengali query wording around department/designation searches is ignored
   when matching the actual CSV values. Results come from stored data —
   no AI guessing involved.
4. When someone asks a broader **question**, the bot finds the most
   relevant employee records by embedding similarity and asks the configured
   LLM to answer using *only* those records as context.

The employee directory is searched, not used to train the language model.
Add or correct employee facts in the HR CSV, then reload/rebuild the index
so the chatbot can use them.
Every indexed employee record explicitly includes the source designation/job
title and department in its search text. If the CSV omits a value, the bot marks
it as not specified; it infers a department only from clear directory IDs.

Examples of directory questions:

- `Digital Operations department e koto jon?` — returns the verified count
  and each matching employee's name and designation.
- `department wise koyjon?` — returns counts for each department, including
  rows whose department is missing in the CSV.
- `News Desk department` — lists matching employees; exact department and
  designation searches are not limited to the first 25 results.
- `Who is the editor and publisher in The Daily Star?` — matches the complete
  designation and returns the person whose title is **Editor & Publisher**.
  A one-character typo in a role word (for example `publishar`) is tolerated.
- `join editor` — tolerates the typo and matches **Joint Editor** only, rather
  than returning everyone whose designation contains “Editor”.
- `editor my dhaka` — applies both the designation and department filters,
  returning only editors in **My Dhaka**.
- `IT team` or `Who is the head of the IT team?` — returns the IT head rather
  than every person in the department.
- `Who is the CBO?` — matches the designation acronym to **Chief Business
  Officer** in the directory and returns its verified employee record.
- Minor spelling errors are tolerated in employee names, designations, and
  complete department names, for example `Raffat Binte Rashad`,
  `edtor my dahka`, or `digtal operatons`.
- Common Banglish and Bengali job words such as `sompadok`, `jugmo sompadok`,
  `sangbadik`, `সম্পাদক`, and `যুগ্ম সম্পাদক` are normalized to directory titles.
  Employee-name spellings in another script still need a verified
  `name_aliases` entry; the bot does not guess transliterations.
- `all types of editors`, `all editor roles`, or `sob rokom editor` deliberately
  includes Joint Editors, Sub-Editors, and other editor titles; a plain `editor`
  query stays limited to the exact Editor category.
- Uppercase initials such as `RR` can find employees by first/last-name initials;
  common title abbreviations such as `Sr.` and `Asst.` normalize to their full
  forms. Acronyms such as `CBO` are matched from the title before its comma.
- A role plus department is treated as an intersection, while a role-only or
  department-only query returns the matching directory records with both fields.
- `/api/health` includes aggregate data-quality counts for missing directory
  fields, invalid joining dates/blood groups/status values, duplicate names/IDs,
  alias coverage, age/service-length/personal-email coverage, and source CSV
  modification time; empty HR facts remain
  explicitly unavailable rather than inferred.
- Updating the CSV reuses embeddings for unchanged employee records and only
  asks Ollama to embed records whose searchable text has changed.

Head-of-department lookup uses the HR designation and department fields:
`Who is head of IT?` uses the IT department acronym and returns only the staff
record whose verified designation is a head title. `list department heads`
lists all records with a head title; departments without a verified head return
a no-match response rather than unrelated department staff.
Queries can combine a head role, department, and a person's name, for example
`head of HR Mahmud` narrows the HR heads to Md. Mahmudul Hasan Khan.

Age, service length, and personal email are direct directory lookups when HR
provides them. They remain structured fields and are excluded from embedding
text and semantic LLM context. Ask `Aasha age`, `Aasha personal mail`, or
`Aasha service length`; `how long has Aasha worked` also resolves the HR-provided
service length.
Employment status and employment type are separate: ask `Aasha employment
status` for Active/Inactive, `Aasha employment type` for Permanent/Part-Time,
or `list part-time employees` to search by type. `service log` is also accepted
as a query for the source's service length.

Remaining HR-backed directory learning checklist:

- Add HR-approved alternate spellings, Banglish/transliterated names, and
  department short forms in the alias columns. The search supports these
  aliases, but none are populated until HR verifies them.
- Supply an approved organization chart or reporting rule if `Chief`,
  `In-Charge`, or other titles should count as a department's “Head”. The bot
  searches those titles by their actual designation and does not invent a
  reporting hierarchy.
- Fill any still-missing joining dates, ages, service lengths, blood groups,
  personal email addresses, and record update dates from HR. Blank source cells
  remain unavailable rather than inferred.
- Mark current/former status in the source when HR wants former employees
  excluded; status filtering is ready, but blank status is not guessed.
- Review duplicate names and incomplete/malformed source rows with HR. The bot
  reports IDs and departments to disambiguate duplicate names and exposes
  aggregate quality counts at `/api/health`; it does not delete or repair rows
  based on guesses.
- After approved HR changes, reload the CSV and manually verify head queries,
  title-plus-department filters, typo queries, duplicate-name disambiguation,
  and employee attribute lookups against the source.

For a manual directory review after HR changes, check queries such as `Who is
head of IT?`, `head of HR`, `Who is in charge of Star Bangla?`, `all types of
editors`, an exact duplicate employee name, and a named employee's email or
record update date. Confirm results against the current HR source.

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
`designation`, `department`, `extension`, `email`, `desk`, `joining_date`,
`blood_group`, `age`, `service_length`, `personal_email`, `employment_type`, `hr_id`,
`name_aliases`, `designation_aliases`, `department_aliases`, `employment_status`,
and `record_updated_at`. Add verified join dates, ages, service lengths, blood
groups, personal email addresses, and record update dates from HR; blank fields
are shown as not listed. Keep only fields specifically approved for this
directory. Personal email, age, and service length are direct-lookup fields and
are excluded from embedding text and semantic LLM context.
Optional `employment_status` (or `status`) accepts values such as `active`,
`inactive`, `former`, `left`, `resigned`, `terminated`, or `retired`. Rows with
an inactive status are excluded from the searchable directory; new or changed
HR rows are picked up after the CSV reload.
`employment_type` records categories such as `Permanent`, `Part-Time`, `Intern`,
or `Contractual`; it does not replace the Active/Inactive `employment_status`.

For verified alternate spellings or commonly used short forms, put aliases in
the matching alias column separated by `|`. For example, `name_aliases` can
contain official alternate spellings/Banglish forms; title and department
aliases work the same way. Search returns the employee's original HR name,
designation, and department. Do not add guessed aliases or unsupported HR data.

To import the three employee sheets from the Manpower General Info workbook,
run the reusable importer below. It matches records by HR ID, stores the
workbook-provided age/service length/personal email, keeps inactive rows in the
CSV while excluding them from the active search index, and uses the workbook's
`Sheet14` A+ list only to fill a missing A+ group by matching HR ID. Use
`-DryRun` first to preview the merge counts.
Remove `-DryRun` to apply the import.

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\import_manpower.ps1 `
  -Workbook 'C:\path\to\lDS Manpower General Info.xlsx' `
  -CsvPath '.\data\employees.csv' -DryRun
```

```csv
name,designation,department,extension,email,desk,joining_date,blood_group,age,service_length,personal_email,hr_id,name_aliases,designation_aliases,department_aliases,employment_status,record_updated_at
Rahim Uddin,Staff Reporter,Metro Desk,2101,rahim.uddin@example.com,3rd Floor,2020-04-12,O+,"36 years, 2 months, 1 day","2 years, 8 months, 4 days",rahim.personal@example.com,1234,Rahim Uddin|Rahimuddin,Reporter,Metro,active,2026-10-26
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
