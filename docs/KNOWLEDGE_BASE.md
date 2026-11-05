# Approved company knowledge base

The staff CSV remains the source of employee facts. This document store is for
approved policies, FAQs, organization references, and other company material.
It does not fine-tune the language model: local Ollama embeddings index the
source files, and answers cite the matched file and section.

## Add sources

Place approved `.md`, `.txt`, or `.csv` files under `data/knowledge/`. Markdown
headings become citation sections. CSV files should have a header row; each
data row becomes a separate searchable record. Subfolders are supported.

Use clear titles and include the policy owner, effective date, and review date
inside each source. Remove superseded copies so an old rule cannot be retrieved
alongside the current one. Do not add personal, medical, payroll, or otherwise
restricted information unless HR and the data owner have approved its use and
audience. PDF and Word files must be converted to text/Markdown before adding;
review the conversion for missing tables or footnotes.

## Recommended sources to provide

- Current approved organization chart and reporting-line reference. Include
  employee IDs, department, reports-to ID, effective date, and acting-head
  end dates where relevant.
- HR handbook and current policies: leave, attendance, holidays, benefits,
  onboarding, and HR request/contact processes.
- IT help and security procedures, office/emergency contacts, and approved
  employee FAQs.
- Department/title glossary with approved abbreviations and Bengali/Banglish
  aliases. Keep aliases in the matching CSV alias columns when they describe
  staff, titles, or departments.
- Public company/about information only if the chatbot should answer those
  questions from an approved source.

## Refresh and check

After adding or replacing source files, call `POST /api/reload` with the admin
token. The endpoint returns immediately; poll `/api/health` until `reload.running`
is false. Check `reload.error`, `knowledge_chunks`, and the employee quality
report before allowing staff to rely on the refreshed data.

Knowledge-backed answers use local Ollama chat and include source citations.
General questions without a matching approved source continue to use the
configured general AI provider; they are not evidence of Daily Star policy.

## Evaluation worksheet

Fill `data/evaluation/qa_cases.csv` with realistic English, Bengali, and
Banglish questions, the approved expected answer, and the source that supports
it. Include ambiguous questions and questions whose answer is intentionally
missing. Keep this worksheet out of `data/knowledge/` so expected answers are
not used as answer context.
