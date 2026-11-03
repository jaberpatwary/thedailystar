package server

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"dailystar-chatbot/internal/groq"
	"dailystar-chatbot/internal/models"
	"dailystar-chatbot/internal/ollama"
	"dailystar-chatbot/internal/store"
)

type Server struct {
	Store           *store.Store
	Ollama          *ollama.Client
	Groq            *groq.Client
	PreferredEngine string // "groq" or "ollama"
	CSVPath         string
	EnvPath         string
	WebFS           fs.FS
	mux             *http.ServeMux
	AccessToken     string
	AdminToken      string
	rateMu          sync.Mutex
	visitors        map[string]*visitor
}

type visitor struct {
	count   int
	resetAt time.Time
}

func New(st *store.Store, cl *ollama.Client, gr *groq.Client, csvPath, envPath string, webFS fs.FS, accessToken, adminToken string) *Server {
	pref := "ollama"
	if gr != nil && gr.IsConfigured() {
		pref = "groq"
	}
	s := &Server{
		Store:           st,
		Ollama:          cl,
		Groq:            gr,
		PreferredEngine: pref,
		CSVPath:         csvPath,
		EnvPath:         envPath,
		WebFS:           webFS,
		mux:             http.NewServeMux(),
		AccessToken:     strings.TrimSpace(accessToken),
		AdminToken:      strings.TrimSpace(adminToken),
		visitors:        make(map[string]*visitor),
	}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Cache-Control", "no-store")
		if strings.HasPrefix(r.URL.Path, "/api/") && !s.allowRequest(r) {
			http.Error(w, "too many requests; please wait a minute", http.StatusTooManyRequests)
			return
		}
		s.mux.ServeHTTP(w, r)
	})
}

// allowRequest is a small in-process guard against accidental loops and simple abuse.
func (s *Server) allowRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	now := time.Now()
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	v := s.visitors[host]
	if v == nil || now.After(v.resetAt) {
		s.visitors[host] = &visitor{count: 1, resetAt: now.Add(time.Minute)}
		return true
	}
	v.count++
	return v.count <= 60
}

func tokenMatches(got, want string) bool {
	return want != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (s *Server) requireAccess(w http.ResponseWriter, r *http.Request) bool {
	if s.AccessToken == "" || tokenMatches(r.Header.Get("X-Access-Token"), s.AccessToken) {
		return true
	}
	http.Error(w, "authentication required", http.StatusUnauthorized)
	return false
}

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if s.AdminToken == "" || tokenMatches(r.Header.Get("X-Admin-Token"), s.AdminToken) {
		return true
	}
	http.Error(w, "administrator authentication required", http.StatusUnauthorized)
	return false
}

func (s *Server) routes() {
	s.mux.Handle("/", http.FileServer(http.FS(s.WebFS)))
	s.mux.HandleFunc("/api/chat", s.handleChat)
	s.mux.HandleFunc("/api/reload", s.handleReload)
	s.mux.HandleFunc("/api/health", s.handleHealth)
	s.mux.HandleFunc("/api/config", s.handleConfig)
}

type chatHistoryItem struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Message string            `json:"message"`
	History []chatHistoryItem `json:"history,omitempty"`
}

type chatResponse struct {
	Answer    string            `json:"answer"`
	Employees []models.Employee `json:"employees,omitempty"`
	Mode      string            `json:"mode"` // "direct_lookup" | "semantic" | "greeting" | "no_match"
	Provider  string            `json:"provider,omitempty"`
	Engine    string            `json:"engine,omitempty"` // "groq" or "ollama"
}

func (s *Server) activeEngineInfo() (string, string) {
	if s.PreferredEngine == "ollama" || s.Groq == nil || !s.Groq.IsConfigured() {
		model := "llama3.2"
		if s.Ollama != nil && s.Ollama.ChatModel != "" {
			model = s.Ollama.ChatModel
		}
		return "ollama", "Local Ollama (" + model + ")"
	}
	model := "openai/gpt-oss-120b"
	if s.Groq != nil && s.Groq.Model != "" {
		model = s.Groq.Model
	}
	return "groq", "Groq Cloud (" + model + ")"
}

func handleCasualChat(msg string, inBengali bool) (string, bool) {
	lower := strings.ToLower(strings.Trim(msg, " ?!.,\"'।;:-"))

	// 1. Gratitude / Thanks
	thanks := []string{"thanks", "thank you", "thx", "dhonnobad", "dhonnobaad", "dhonnobadd", "ধন্যবাদ", "অনেক ধন্যবাদ", "shukriya", "thank u", "many thanks"}
	for _, t := range thanks {
		if lower == t || strings.HasPrefix(lower, t+" ") {
			if inBengali {
				return "আপনাকে অনেক ধন্যবাদ! 😊 The Daily Star-এর আর কোনো কর্মী, পদবী বা ডিপার্টমেন্টের বিষয়ে জানতে চাইলে বলুন।", true
			}
			return "You are very welcome! 😊 Feel free to ask about any other colleague, desk, or department at The Daily Star.", true
		}
	}

	// 2. Acknowledgements
	acks := []string{"ok", "okay", "thik ache", "accha", "acha", "humm", "hum", "got it", "fine", "cool", "হুম", "ঠিক আছে", "আচ্ছা", "বুঝলাম"}
	for _, a := range acks {
		if lower == a || strings.HasPrefix(lower, a+" ") {
			if inBengali {
				return "ঠিক আছে! যেকোনো প্রয়োজনে আমাকে জানাতে পারেন। 👍", true
			}
			return "Great! Let me know whenever you need any directory information. 👍", true
		}
	}

	// 3. Goodbyes
	byes := []string{"bye", "goodbye", "good bye", "see you", "allah hafez", "tata", "বিদায়", "বিদায়", "আল্লাহ হাফেজ"}
	for _, b := range byes {
		if lower == b || strings.HasPrefix(lower, b+" ") {
			if inBengali {
				return "বিদায়! আপনার দিনটি শুভ হোক। প্রয়োজনে যেকোনো সময় নক দিতে পারেন! 👋", true
			}
			return "Goodbye! Have a great day ahead! Feel free to ask anytime. 👋", true
		}
	}

	// 4. How are you
	howAreYou := []string{"kemon acho", "kemon achen", "how are you", "কেমন আছেন", "কেমন আছো", "valo acho", "bhalo acho"}
	for _, h := range howAreYou {
		if lower == h || strings.HasPrefix(lower, h+" ") {
			if inBengali {
				return "আলহামদুলিল্লাহ, আমি খুব ভালো আছি! 😊 আশা করি আপনিও ভালো আছেন। The Daily Star-এর কার সম্পর্কে তথ্য জানতে চান বলুন?", true
			}
			return "I'm doing great, thank you! 😊 How can I help you with The Daily Star staff directory today?", true
		}
	}

	// 5. Bot Identity
	botIdentity := []string{"who are you", "tumi ke", "apni ke", "তুমি কে", "আপনি কে", "what can you do", "ki korte paro", "what is your name", "নাম কি", "nam ki"}
	for _, b := range botIdentity {
		if lower == b || strings.HasPrefix(lower, b+" ") {
			if inBengali {
				return "আমি **The Daily Star**-এর AI অ্যাসিস্ট্যান্ট, আমাকে ডেভেলপ করেছেন **জাবের পাটোয়ারী (Jaber Patwary)**। 🤖\n\nআপনি আমাকে দ্য ডেইলি স্টারের যেকোনো কর্মী, সাংবাদিক, বিভাগ (যেমন: Metro, Sports, IT), ইমেইল, ফোন নম্বর বা যেকোনো সাধারণ বিষয়ে প্রশ্ন করতে পারেন।", true
			}
			return "I am **The Daily Star** AI Assistant, created and developed by **Jaber Patwary**! 🤖\n\nYou can ask me to find any reporter, editor, department, desk location, email, or ask any general question.", true
		}
	}

	// 6. Creator / Developer (Jaber Patwary)
	creatorPhrases := []string{
		"who made you", "who created you", "who built you", "who developed you", "who is your creator",
		"who is your developer", "who made", "ke baniyeche", "ke banise", "ke toiri korche", "ke toiri koreche",
		"tumi ke baniye", "tomake ke baniye", "tomake ke banise", "tomar developer ke", "tomar creator ke",
		"tomar malik ke", "ke toiri korlo", "ore ke baniye", "toke ke baniye", "toke ke banise", "ke baniye",
		"কার তৈরি", "কে তৈরি করেছে", "কে বানিয়েছে", "কে বানিয়েসে", "কে বানাইছে", "কে বানাইসে",
		"তোমার ডেভেলপার কে", "তোমাকে কে বানিয়েছে", "তোমাকে কে বানাইছে", "ডেভেলপার কে", "তৈরি করেছে কে",
	}
	for _, c := range creatorPhrases {
		if strings.Contains(lower, c) {
			if inBengali {
				return "আমাকে তৈরি এবং ডেভেলপ করেছেন **জাবের পাটোয়ারী (Jaber Patwary)**। 🚀\n\nThe Daily Star-এর স্টাফ ডিরেক্টরি এবং সার্বিক সাধারণ প্রশ্নের জন্য আমি একটি পূর্ণাঙ্গ স্মার্ট AI অ্যাসিস্ট্যান্ট।", true
			}
			return "I was created and developed by **Jaber Patwary**! 🚀\n\nI am designed as a smart AI assistant for The Daily Star staff directory as well as general knowledge queries.", true
		}
	}

	return "", false
}

func isQuestion(q string) bool {
	lower := strings.ToLower(q)
	questionWords := []string{
		"who", "what", "where", "how", "when", "which", "give", "tell", "show", "find", "age", "boyosh", "tenure", "service length", "how long", "how old", "how many years", "personal mail", "বয়স", "বয়স", "ব্যক্তিগত মেইল",
		"contact", "phone", "email", "mobile", "number", "desk", "location", "floor",
		"ke", "ki", "kothay", "kake", "konta", "kobe", "koto", "er ", "tar", "or",
		"dao", "daw", "bolun", "bolte", "janan", "dekhao", "khuje", "ache", "thake",
		"boshe", "number ta", "email ta", "details",
	}
	for _, w := range questionWords {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return len(strings.Fields(q)) >= 3
}

func isGreeting(q string) bool {
	q = strings.ToLower(strings.Trim(q, " ?!.,\"'"))
	greetings := []string{
		"hi", "hello", "hey", "hola", "assalamu alaikum", "assalamualaykum",
		"salam", "shalaam", "good morning", "good evening", "good afternoon", "help",
	}
	for _, g := range greetings {
		if q == g || strings.HasPrefix(q, g+" ") {
			return true
		}
	}
	return false
}

func isBengali(s string) bool {
	for _, r := range s {
		if r >= 0x0980 && r <= 0x09FF {
			return true
		}
	}
	lower := strings.ToLower(s)
	banglishWords := []string{
		"ke", "ki", "kake", "ache", "kothay", "daw", "dao", "janan", "thake",
		"er", "koi", "tar", "or", "kemon", "baniyeche", "banise", "banaiyeche",
		"koreche", "korche", "bhalo", "valo", "tumi", "apni", "amake", "tomake",
		"bolun", "shob", "sob", "dekhan", "dekhao", "khuje", "kintu", "aar",
		"sompadok", "shompadok", "jugmo", "uposompadok", "sangbadik", "bibhag", "bivag", "podobi",
	}
	words := strings.Fields(strings.Trim(lower, "?!.,\"';:-।"))
	for _, w := range words {
		wClean := strings.Trim(w, "?!.,\"';:-।")
		for _, bw := range banglishWords {
			if wClean == bw {
				return true
			}
		}
	}
	return false
}

func requestedEmployeeAttribute(e models.Employee, query string, inBengali bool) (string, bool) {
	q := strings.ToLower(query)
	attribute, value := "", ""
	switch {
	case strings.Contains(q, "service length") || strings.Contains(q, "length of service") || strings.Contains(q, "years of service") || strings.Contains(q, "work duration") || strings.Contains(q, "tenure") || strings.Contains(q, "how long") || strings.Contains(q, "how many days") || strings.Contains(q, "how many years") || strings.Contains(q, "koto din") || strings.Contains(q, "koto bochor") || strings.Contains(q, "chakri koto") || strings.Contains(q, "kormo jibon"):
		attribute, value = "service length", e.ServiceLength
	case strings.Contains(q, "age") || strings.Contains(q, "how old") || strings.Contains(q, "boyosh") || strings.Contains(q, "বয়স") || strings.Contains(q, "বয়স"):
		attribute, value = "age", e.Age
	case strings.Contains(q, "personal email") || strings.Contains(q, "personal mail") || strings.Contains(q, "private email") || strings.Contains(q, "ব্যক্তিগত ইমেইল") || strings.Contains(q, "ব্যক্তিগত মেইল"):
		attribute, value = "personal email", e.PersonalEmail
	case strings.Contains(q, "record updated") || strings.Contains(q, "last updated") || strings.Contains(q, "updated date") || strings.Contains(q, "last update date"):
		attribute, value = "record updated date", e.RecordUpdatedAt
	case strings.Contains(q, "blood group") || strings.Contains(q, "bloodgroup") || strings.Contains(q, "rokter group") || strings.Contains(q, "\u09b0\u0995\u09cd\u09a4\u09c7\u09b0 \u0997\u09cd\u09b0\u09c1\u09aa"):
		attribute, value = "blood group", e.BloodGroup
	case strings.Contains(q, "joining date") || strings.Contains(q, "join date") || strings.Contains(q, "date of joining") || strings.Contains(q, "jogdan") || strings.Contains(q, "\u09af\u09cb\u0997\u09a6\u09be\u09a8\u09c7\u09b0 \u09a4\u09be\u09b0\u09bf\u0996"):
		attribute, value = "joining date", e.JoiningDate
	case strings.Contains(q, "email") || strings.Contains(q, "e-mail") || strings.Contains(q, "\u0987\u09ae\u09c7\u0987\u09b2"):
		attribute, value = "email", e.Email
	case strings.Contains(q, "phone") || strings.Contains(q, "mobile") || strings.Contains(q, "\u09ab\u09cb\u09a8"):
		attribute, value = "phone", e.Phone
	case strings.Contains(q, "floor") || strings.Contains(q, "desk") || strings.Contains(q, "location") || strings.Contains(q, "where") || strings.Contains(q, "kothay"):
		attribute, value = "desk/location", e.Desk
	case strings.Contains(q, "designation") || strings.Contains(q, "title") || strings.Contains(q, "podobi") || strings.Contains(q, "\u09aa\u09a6\u09ac\u09bf") || strings.Contains(q, "\u09aa\u09a6\u09ac\u09c0"):
		attribute, value = "designation", e.Designation
	case strings.Contains(q, "department") || strings.Contains(q, "dept") || strings.Contains(q, "bibhag") || strings.Contains(q, "bivag") || strings.Contains(q, "\u09ac\u09bf\u09ad\u09be\u0997"):
		attribute, value = "department", e.Department
	default:
		return "", false
	}
	if strings.TrimSpace(value) == "" {
		if inBengali {
			return fmt.Sprintf("**%s**-\u098f\u09b0 %s: \u09a4\u09a5\u09cd\u09af \u09a1\u09bf\u09b0\u09c7\u0995\u09cd\u099f\u09b0\u09bf\u09a4\u09c7 \u09a8\u09c7\u0987।", e.Name, attribute), true
		}
		return fmt.Sprintf("**%s**: %s is not listed in the HR directory.", e.Name, attribute), true
	}
	if inBengali {
		return fmt.Sprintf("**%s**-\u098f\u09b0 %s: **%s**", e.Name, attribute, value), true
	}
	return fmt.Sprintf("**%s**'s %s: **%s**", e.Name, attribute, value), true
}

func formatSingleEmployee(e models.Employee, inBengali bool) string {
	if inBengali {
		return formatSingleEmployeeBengali(e)
	}
	var b strings.Builder
	if inBengali {
		fmt.Fprintf(&b, "**%s** হলেন The Daily Star-এর **%s**।\n\n", e.Name, e.Designation)
		if e.Department != "" {
			fmt.Fprintf(&b, "🏢 **বিভাগ:** %s\n", e.Department)
		}
		joiningDate := e.JoiningDate
		if joiningDate == "" {
			joiningDate = "তথ্য নেই"
		}
		bloodGroup := e.BloodGroup
		if bloodGroup == "" {
			bloodGroup = "তথ্য নেই"
		}
		fmt.Fprintf(&b, "**যোগদানের তারিখ:** %s\n**রক্তের গ্রুপ:** %s\n", joiningDate, bloodGroup)
		if e.Desk != "" {
			fmt.Fprintf(&b, "📍 **অবস্থান:** %s\n", e.Desk)
		}
		if e.Email != "" {
			fmt.Fprintf(&b, "✉️ **অফিসিয়াল ইমেইল:** %s\n", e.Email)
		}
		if e.Phone != "" {
			fmt.Fprintf(&b, "📞 **ফোন:** %s\n", e.Phone)
		}
		if e.EmployeeID != "" {
			fmt.Fprintf(&b, "🆔 **স্টাফ আইডি:** %s\n", e.EmployeeID)
		}
		return strings.TrimSpace(b.String())
	}
	if inBengali {
		b.WriteString(fmt.Sprintf("**%s** হলেন The Daily Star-এর **%s**।\n\n", e.Name, e.Designation))
		if e.Department != "" {
			b.WriteString(fmt.Sprintf("🏢 **বিভাগ (Department):** %s\n", e.Department))
		}
		joiningDate := e.JoiningDate
		if joiningDate == "" {
			joiningDate = "Not listed in directory"
		}
		bloodGroup := e.BloodGroup
		if bloodGroup == "" {
			bloodGroup = "Not listed in directory"
		}
		b.WriteString(fmt.Sprintf("Joining Date: %s\n", joiningDate))
		b.WriteString(fmt.Sprintf("Blood Group: %s\n", bloodGroup))
		recordUpdatedAt := e.RecordUpdatedAt
		if recordUpdatedAt == "" {
			recordUpdatedAt = "Not listed in directory"
		}
		b.WriteString(fmt.Sprintf("HR Record Updated: %s\n", recordUpdatedAt))

		if e.Desk != "" {
			b.WriteString(fmt.Sprintf("📍 **অবস্থান / ডেস্ক:** %s\n", e.Desk))
		}
		if e.Email != "" {
			b.WriteString(fmt.Sprintf("📧 **অফিসিয়াল ইমেইল:** %s\n", e.Email))
		}
		if e.Phone != "" {
			b.WriteString(fmt.Sprintf("📞 **ফোন নম্বর:** %s\n", e.Phone))
		} else {
			b.WriteString("📞 **ফোন নম্বর:** ডিরেক্টরিতে তালিকাভুক্ত নেই\n")
		}
		if e.EmployeeID != "" {
			b.WriteString(fmt.Sprintf("🆔 **স্টাফ আইডি:** %s\n", e.EmployeeID))
		} else if e.Extension != "" {
			b.WriteString(fmt.Sprintf("📞 **যোগাযোগ / আইডি:** %s\n", e.Extension))
		}
		if e.LinkedIn != "" || e.Twitter != "" || e.Facebook != "" || e.Instagram != "" {
			b.WriteString("\n🌐 **সোশ্যাল মিডিয়া:**\n")
			if e.LinkedIn != "" {
				b.WriteString(fmt.Sprintf("  💼 LinkedIn: %s\n", e.LinkedIn))
			}
			if e.Twitter != "" {
				b.WriteString(fmt.Sprintf("  🐦 Twitter/X: %s\n", e.Twitter))
			}
			if e.Facebook != "" {
				b.WriteString(fmt.Sprintf("  📘 Facebook: %s\n", e.Facebook))
			}
			if e.Instagram != "" {
				b.WriteString(fmt.Sprintf("  📸 Instagram: %s\n", e.Instagram))
			}
		}
	} else {
		b.WriteString(fmt.Sprintf("**%s** is the **%s** at The Daily Star.\n\n", e.Name, e.Designation))
		if e.Department != "" {
			b.WriteString(fmt.Sprintf("🏢 **Department:** %s\n", e.Department))
		}
		joiningDate := e.JoiningDate
		if joiningDate == "" {
			joiningDate = "Not listed in directory"
		}
		bloodGroup := e.BloodGroup
		if bloodGroup == "" {
			bloodGroup = "Not listed in directory"
		}
		b.WriteString(fmt.Sprintf("Joining Date: %s\n", joiningDate))
		b.WriteString(fmt.Sprintf("Blood Group: %s\n", bloodGroup))

		if e.Desk != "" {
			b.WriteString(fmt.Sprintf("📍 **Desk / Location:** %s\n", e.Desk))
		}
		if e.Email != "" {
			b.WriteString(fmt.Sprintf("📧 **Official Email:** %s\n", e.Email))
		}
		if e.Phone != "" {
			b.WriteString(fmt.Sprintf("📞 **Phone Number:** %s\n", e.Phone))
		} else {
			b.WriteString("📞 **Phone Number:** Not listed in directory\n")
		}
		if e.EmployeeID != "" {
			b.WriteString(fmt.Sprintf("🆔 **Staff ID:** %s\n", e.EmployeeID))
		} else if e.Extension != "" {
			b.WriteString(fmt.Sprintf("📞 **Contact / ID:** %s\n", e.Extension))
		}
		if e.LinkedIn != "" || e.Twitter != "" || e.Facebook != "" || e.Instagram != "" {
			b.WriteString("\n🌐 **Social Media:**\n")
			if e.LinkedIn != "" {
				b.WriteString(fmt.Sprintf("  💼 LinkedIn: %s\n", e.LinkedIn))
			}
			if e.Twitter != "" {
				b.WriteString(fmt.Sprintf("  🐦 Twitter/X: %s\n", e.Twitter))
			}
			if e.Facebook != "" {
				b.WriteString(fmt.Sprintf("  📘 Facebook: %s\n", e.Facebook))
			}
			if e.Instagram != "" {
				b.WriteString(fmt.Sprintf("  📸 Instagram: %s\n", e.Instagram))
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func formatMultipleEmployees(topic string, emps []models.Employee, inBengali bool) string {
	if inBengali {
		return formatMultipleEmployeesBengali(topic, emps)
	}
	var b strings.Builder
	if inBengali {
		fmt.Fprintf(&b, "**%s**-এর সংশ্লিষ্ট মোট **%d জন** কর্মী পাওয়া গেছে:\n\n", topic, len(emps))
		for i, e := range emps {
			e.Designation += employeeDisambiguators(e)
			loc := ""
			if e.Desk != "" {
				loc = " (📍 " + e.Desk + ")"
			}
			fmt.Fprintf(&b, "%d. **%s** — %s%s\n", i+1, e.Name, e.Designation, loc)
		}
		return strings.TrimSpace(b.String())
	}
	if inBengali {
		b.WriteString(fmt.Sprintf("**%s**-এর সংশ্লিষ্ট মোট **%d জন** কর্মী পাওয়া গেছে:\n\n", topic, len(emps)))
		for i, e := range emps {
			e.Designation += employeeDisambiguators(e)
			loc := ""
			if e.Desk != "" {
				loc = " (📍 " + e.Desk + ")"
			}
			b.WriteString(fmt.Sprintf("%d. **%s** — %s%s\n", i+1, e.Name, e.Designation, loc))
		}
	} else {
		b.WriteString(fmt.Sprintf("Found **%d staff members** related to **%s**:\n\n", len(emps), topic))
		for i, e := range emps {
			e.Designation += employeeDisambiguators(e)
			loc := ""
			if e.Desk != "" {
				loc = " (📍 " + e.Desk + ")"
			}
			b.WriteString(fmt.Sprintf("%d. **%s** — %s%s\n", i+1, e.Name, e.Designation, loc))
		}
	}
	return strings.TrimSpace(b.String())
}

func formatSingleEmployeeBengali(e models.Employee) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s** \u09b9\u09b2\u09c7\u09a8 The Daily Star-\u098f\u09b0 **%s**\u0964\n\n", e.Name, e.Designation)
	if e.Department != "" {
		fmt.Fprintf(&b, "\U0001f3e2 **\u09ac\u09bf\u09ad\u09be\u0997:** %s\n", e.Department)
	}
	joiningDate, bloodGroup := e.JoiningDate, e.BloodGroup
	if joiningDate == "" {
		joiningDate = "\u09a4\u09a5\u09cd\u09af \u09a8\u09c7\u0987"
	}
	if bloodGroup == "" {
		bloodGroup = "\u09a4\u09a5\u09cd\u09af \u09a8\u09c7\u0987"
	}
	fmt.Fprintf(&b, "**\u09af\u09cb\u0997\u09a6\u09be\u09a8\u09c7\u09b0 \u09a4\u09be\u09b0\u09bf\u0996:** %s\n**\u09b0\u0995\u09cd\u09a4\u09c7\u09b0 \u0997\u09cd\u09b0\u09c1\u09aa:** %s\n", joiningDate, bloodGroup)
	recordUpdatedAt := e.RecordUpdatedAt
	if recordUpdatedAt == "" {
		recordUpdatedAt = "\u09a4\u09a5\u09cd\u09af \u09a8\u09c7\u0987"
	}
	fmt.Fprintf(&b, "**HR record update date:** %s\n", recordUpdatedAt)
	if e.Desk != "" {
		fmt.Fprintf(&b, "\U0001f4cd **\u0985\u09ac\u09b8\u09cd\u09a5\u09be\u09a8:** %s\n", e.Desk)
	}
	if e.Email != "" {
		fmt.Fprintf(&b, "\u2709\ufe0f **\u0985\u09ab\u09bf\u09b8\u09bf\u09af\u09bc\u09be\u09b2 \u0987\u09ae\u09c7\u0987\u09b2:** %s\n", e.Email)
	}
	if e.Phone != "" {
		fmt.Fprintf(&b, "\U0001f4de **\u09ab\u09cb\u09a8:** %s\n", e.Phone)
	}
	if e.EmployeeID != "" {
		fmt.Fprintf(&b, "\U0001f194 **\u09b8\u09cd\u099f\u09be\u09ab \u0986\u0987\u09a1\u09bf:** %s\n", e.EmployeeID)
	}
	return strings.TrimSpace(b.String())
}

func formatMultipleEmployeesBengali(topic string, emps []models.Employee) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s**-\u098f\u09b0 \u09b8\u0982\u09b6\u09cd\u09b2\u09bf\u09b7\u09cd\u099f \u09ae\u09cb\u099f **%d \u099c\u09a8** \u0995\u09b0\u09cd\u09ae\u09c0 \u09aa\u09be\u0993\u09af\u09bc\u09be \u0997\u09c7\u099b\u09c7:\n\n", topic, len(emps))
	for i, e := range emps {
		e.Designation += employeeDisambiguators(e)
		loc := ""
		if e.Desk != "" {
			loc = " (\U0001f4cd " + e.Desk + ")"
		}
		fmt.Fprintf(&b, "%d. **%s** — %s%s\n", i+1, e.Name, e.Designation, loc)
	}
	return strings.TrimSpace(b.String())
}

func employeeDisambiguators(e models.Employee) string {
	var parts []string
	if e.Department != "" {
		parts = append(parts, e.Department)
	}
	if e.EmployeeID != "" {
		parts = append(parts, "Staff ID "+e.EmployeeID)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, "; ") + ")"
}

func allSameEmployeeName(employees []models.Employee) bool {
	if len(employees) < 2 {
		return false
	}
	first := strings.ToLower(strings.TrimSpace(employees[0].Name))
	if first == "" {
		return false
	}
	for _, employee := range employees[1:] {
		if !strings.EqualFold(first, strings.TrimSpace(employee.Name)) {
			return false
		}
	}
	return true
}

func (s *Server) askLLM(systemPrompt, userPrompt string) (string, string, string) {
	if s.PreferredEngine == "ollama" {
		// Use Local Ollama first
		if s.Ollama != nil {
			ans, err := s.Ollama.Chat(systemPrompt, userPrompt)
			if err == nil && strings.TrimSpace(ans) != "" {
				return strings.TrimSpace(ans), "Local Ollama (" + s.Ollama.ChatModel + ")", "ollama"
			}
			log.Printf("Ollama chat failed: %v", err)
		}
		// Fallback to Groq if configured
		if s.Groq != nil && s.Groq.IsConfigured() {
			ans, err := s.Groq.Chat(systemPrompt, userPrompt)
			if err == nil && strings.TrimSpace(ans) != "" {
				return strings.TrimSpace(ans), "Groq Cloud (" + s.Groq.Model + ")", "groq"
			}
		}
		return "", "", "ollama"
	}

	// Default: Try Groq Cloud LPU first
	if s.Groq != nil && s.Groq.IsConfigured() {
		ans, err := s.Groq.Chat(systemPrompt, userPrompt)
		if err == nil && strings.TrimSpace(ans) != "" {
			return strings.TrimSpace(ans), "Groq Cloud (" + s.Groq.Model + ")", "groq"
		}
		log.Printf("Groq chat failed: %v. Falling back to local Ollama.", err)
	}

	// Fallback to Local Ollama
	if s.Ollama != nil {
		ans, err := s.Ollama.Chat(systemPrompt, userPrompt)
		if err == nil && strings.TrimSpace(ans) != "" {
			return strings.TrimSpace(ans), "Local Ollama (" + s.Ollama.ChatModel + ")", "ollama"
		}
		log.Printf("Ollama chat failed: %v", err)
	}

	return "", "", "groq"
}

func buildEmployeeDetails(e models.Employee) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Name: %s\n", e.Name))
	b.WriteString(fmt.Sprintf("Designation: %s\n", e.Designation))
	b.WriteString(fmt.Sprintf("Department: %s\n", e.Department))
	if e.Email != "" {
		b.WriteString(fmt.Sprintf("Email: %s\n", e.Email))
	} else {
		b.WriteString("Email: Not listed in directory\n")
	}
	if e.Phone != "" {
		b.WriteString(fmt.Sprintf("Phone Number: %s\n", e.Phone))
	} else {
		b.WriteString("Phone Number: Not listed in directory\n")
	}
	if e.Desk != "" {
		b.WriteString(fmt.Sprintf("Desk / Floor Location: %s\n", e.Desk))
	}
	if e.EmployeeID != "" {
		b.WriteString(fmt.Sprintf("Staff ID: %s\n", e.EmployeeID))
	}
	if e.LinkedIn != "" {
		b.WriteString(fmt.Sprintf("LinkedIn: %s\n", e.LinkedIn))
	}
	if e.Twitter != "" {
		b.WriteString(fmt.Sprintf("Twitter: %s\n", e.Twitter))
	}
	if e.Facebook != "" {
		b.WriteString(fmt.Sprintf("Facebook: %s\n", e.Facebook))
	}
	if e.Instagram != "" {
		b.WriteString(fmt.Sprintf("Instagram: %s\n", e.Instagram))
	}
	return b.String()
}

func hasPronounOrFollowUp(q string) bool {
	lower := strings.ToLower(q)
	terms := []string{
		"tar ", " or ", " tar?", " or?", "uni ", "tini ", "his ", "her ", "their ",
		"phone number", "email", "number ta", "email ta", "kothay boshe", "kothay thake",
		"desk kothay", "desig ki", "podobi ki", "contact ki",
	}
	for _, t := range terms {
		if strings.Contains(lower, t) || strings.HasPrefix(lower, strings.TrimSpace(t)) {
			return true
		}
	}
	return false
}

// isDirectoryQuestion returns true if the message is specifically asking about Daily Star staff, desks, or offices.
func isDirectoryQuestion(q string) bool {
	lower := strings.ToLower(q)
	directoryKeywords := []string{
		"head of", "heads ", "department head", "department-er head",
		"\u09b8\u09ae\u09cd\u09aa\u09be\u09a6\u0995", "\u09af\u09c1\u0997\u09cd\u09ae", "\u0989\u09aa\u09b8\u09ae\u09cd\u09aa\u09be\u09a6\u0995",
		"\u09b8\u09be\u0982\u09ac\u09be\u09a6\u09bf\u0995", "\u09ac\u09bf\u09ad\u09be\u0997", "\u09aa\u09a6\u09ac\u09bf", "\u09aa\u09a6\u09ac\u09c0",
		"mahfuz", "anam", "naznin", "reporter", "reporters", "editor", "editors", "journalist", "journalists",
		"staff", "employee", "employees", "desk", "desks", "department", "departments", "bureau",
		"metro desk", "sports desk", "editorial desk", "circulation desk", "business desk",
		"it department", "it team", "digital desk", "online desk", "extension", "staff id", "employee id",
		"blood group", "bloodgroup", "joining date", "join date",
		"রক্তের গ্রুপ", "যোগদানের তারিখ",
		"daily star", "dailystar", "tds", "who works in", "who works at", "who works on",
		"who is the editor", "who is the reporter", "who is in charge", "contact info",
		"পদবী", "বিভাগ", "কর্মী", "সাংবাদিক", "ডেস্ক", "ইমেইল", "ফোন", "আইডি", "কর্মকর্তা", "ডেইলি স্টার",
	}
	for _, kw := range directoryKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func isHeadDirectoryQuestion(q string) bool {
	lower := strings.ToLower(q)
	return strings.Contains(lower, "head") || strings.Contains(lower, "heads") || strings.Contains(lower, "\u09aa\u09cd\u09b0\u09a7\u09be\u09a8")
}

func isEmployeeAttributeQuestion(q string) bool {
	lower := strings.ToLower(q)
	for _, term := range []string{"blood group", "bloodgroup", "joining date", "join date", "record updated", "last updated", "updated date", "last update date", "rokter group", "jogdaner tarikh", "\u09b0\u0995\u09cd\u09a4\u09c7\u09b0 \u0997\u09cd\u09b0\u09c1\u09aa", "\u09af\u09cb\u0997\u09a6\u09be\u09a8\u09c7\u09b0 \u09a4\u09be\u09b0\u09bf\u0996"} {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}

func isCountQuestion(q string) bool {
	lower := strings.ToLower(q)
	countTerms := []string{
		"how many", "count", "total staff", "total employee", "number of staff",
		"koto jon", "kotojon", "koy jon", "koyjon", "koijon", "kotojon",
		"কতজন", "কত জন", "কতজনের", "কত জনের", "সংখ্যা", "মোট",
	}
	for _, term := range countTerms {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}

func formatDepartmentMembers(department string, employees []models.Employee, inBengali bool) string {
	var b strings.Builder
	if inBengali {
		fmt.Fprintf(&b, "**%s** বিভাগে মোট **%d জন** কর্মী আছেন:\n\n", department, len(employees))
	} else {
		fmt.Fprintf(&b, "**%s** has **%d staff members**:\n\n", department, len(employees))
	}
	for i, employee := range employees {
		fmt.Fprintf(&b, "%d. **%s** — %s\n", i+1, employee.Name, employee.Designation)
	}
	return strings.TrimSpace(b.String())
}

func formatDepartmentCounts(counts []store.DepartmentCount, inBengali bool) string {
	var total int
	for _, item := range counts {
		total += item.Count
	}
	var b strings.Builder
	if inBengali {
		fmt.Fprintf(&b, "বিভাগ অনুযায়ী কর্মীসংখ্যা (মোট **%d জন**):\n\n", total)
	} else {
		fmt.Fprintf(&b, "Staff count by department (total **%d**):\n\n", total)
	}
	for _, item := range counts {
		if inBengali {
			fmt.Fprintf(&b, "- **%s**: %d জন\n", item.Department, item.Count)
		} else {
			fmt.Fprintf(&b, "- **%s**: %d\n", item.Department, item.Count)
		}
	}
	return strings.TrimSpace(b.String())
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if !s.requireAccess(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req chatRequest
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	msg := strings.TrimSpace(req.Message)
	if len([]rune(msg)) > 2000 {
		http.Error(w, "message is too long", http.StatusBadRequest)
		return
	}
	if len(req.History) > 8 {
		req.History = req.History[len(req.History)-8:]
	}
	if msg == "" {
		writeJSON(w, chatResponse{Answer: "আপনি কী জানতে চান? 😊", Mode: "no_match"})
		return
	}

	bengali := isBengali(msg)
	activeEng, activeProv := s.activeEngineInfo()

	// 1. Casual conversational replies (Thanks, Bye, How are you, Developer, etc.)
	if casualReply, handled := handleCasualChat(msg, bengali); handled {
		writeJSON(w, chatResponse{Answer: casualReply, Mode: "greeting", Provider: activeProv, Engine: activeEng})
		return
	}

	// 2. Greeting
	if isGreeting(msg) {
		greeting := "Hello! 👋 I'm your AI Assistant powered by **The Daily Star**.\n\nI can help you with:\n• 📋 **Staff Directory** — Find any employee, designation, department, email, phone\n• 💬 **General Questions** — Ask me anything! Write emails, translate, explain topics, coding, and more.\n\nHow can I help you today?"
		if bengali {
			greeting = "হ্যালো! 👋 আমি **The Daily Star**-এর AI অ্যাসিস্ট্যান্ট।\n\nআমি আপনাকে যেকোনো বিষয়ে সাহায্য করতে পারি:\n• 📋 **স্টাফ ডিরেক্টরি** — যেকোনো কর্মীর নাম, পদবী, ইমেইল, ফোন, ডেস্ক\n• 💬 **সাধারণ প্রশ্ন** — যেকোনো প্রশ্ন করুন! ইমেইল লেখা, অনুবাদ, ব্যাখ্যা, কোডিং সহায়তা ইত্যাদি।\n\nআপনাকে কীভাবে সাহায্য করতে পারি?"
		}
		writeJSON(w, chatResponse{Answer: greeting, Mode: "greeting", Provider: activeProv, Engine: activeEng})
		return
	}

	if isCountQuestion(msg) {
		if department := s.Store.DepartmentFromQuery(msg); department != "" {
			employees := s.Store.EmployeesByDepartment(department)
			writeJSON(w, chatResponse{
				Answer:    formatDepartmentMembers(department, employees, bengali),
				Employees: employees,
				Mode:      "direct_lookup",
				Provider:  "Verified Staff Directory (" + activeProv + ")",
				Engine:    activeEng,
			})
			return
		}
		if matches, score := s.Store.Search(msg); len(matches) > 0 && score >= 400 {
			answer := fmt.Sprintf("Found **%d staff members** matching the requested designation.", len(matches))
			if bengali {
				answer = fmt.Sprintf("চাওয়া পদবির সঙ্গে মিলে **%d জন** কর্মী পাওয়া গেছে।", len(matches))
			}
			writeJSON(w, chatResponse{
				Answer:    answer,
				Employees: matches,
				Mode:      "direct_lookup",
				Provider:  "Verified Staff Directory (" + activeProv + ")",
				Engine:    activeEng,
			})
			return
		}
		writeJSON(w, chatResponse{
			Answer:   formatDepartmentCounts(s.Store.DepartmentCounts(), bengali),
			Mode:     "direct_lookup",
			Provider: "Verified Staff Directory (" + activeProv + ")",
			Engine:   activeEng,
		})
		return
	}

	// 3. Direct multi-field staff search (Instantly matches full name, partial name, desk, desig, dept)
	matches, topScore := s.Store.Search(msg)
	cleaned := store.CleanQuery(msg)

	// 4. Follow-up pronoun resolution using conversation history (e.g. "tar email ki?", "phone number dao")
	if len(matches) == 0 && len(req.History) > 0 && hasPronounOrFollowUp(msg) {
		for i := len(req.History) - 1; i >= 0; i-- {
			hText := req.History[i].Content
			for j := range s.Store.Employees {
				emp := &s.Store.Employees[j]
				if strings.Contains(strings.ToLower(hText), strings.ToLower(emp.Name)) {
					matches = []models.Employee{*emp}
					topScore = 1500
					break
				}
			}
			if len(matches) > 0 {
				break
			}
		}
	}

	// 5. IF AN EMPLOYEE / DESK / DEPARTMENT DIRECTLY MATCHED (topScore >= 400):
	// Return full employee profile and cards immediately!
	if len(matches) > 0 && topScore >= 400 {
		// Single employee match
		if len(matches) == 1 {
			emp := matches[0]
			var ans string
			if bengali {
				ans = fmt.Sprintf("The Daily Star-এর **%s** হলেন **%s** (%s)। নিচে তার সম্পূর্ণ পরিচিতি কার্ড দেওয়া হলো:", emp.Name, emp.Designation, emp.Department)
			} else {
				ans = fmt.Sprintf("Here are the directory details for **%s**, **%s** (%s):", emp.Name, emp.Designation, emp.Department)
			}
			if attributeAnswer, handled := requestedEmployeeAttribute(emp, msg, bengali); handled {
				ans = attributeAnswer
			}
			writeJSON(w, chatResponse{
				Answer:    ans,
				Employees: matches,
				Mode:      "direct_lookup",
				Provider:  "Verified Staff Directory (" + activeProv + ")",
				Engine:    activeEng,
			})
			return
		}

		// Multiple employee matches
		if len(matches) == 1 && (strings.EqualFold(matches[0].Name, cleaned) || strings.EqualFold(matches[0].Name, strings.TrimSpace(msg))) {
			emp := matches[0]
			var ans string
			if bengali {
				ans = fmt.Sprintf("The Daily Star-এর **%s** হলেন **%s** (%s)। নিচে তার পরিচিতি কার্ড দেওয়া হলো:", emp.Name, emp.Designation, emp.Department)
			} else {
				ans = fmt.Sprintf("Here is the directory card for **%s**, %s (%s):", emp.Name, emp.Designation, emp.Department)
			}
			writeJSON(w, chatResponse{
				Answer:    ans,
				Employees: []models.Employee{emp},
				Mode:      "direct_lookup",
				Provider:  "Verified Staff Directory (" + activeProv + ")",
				Engine:    activeEng,
			})
			return
		}
		topic := cleaned
		if topic == "" {
			topic = msg
		}
		answer := formatMultipleEmployees(topic, matches, bengali)
		if allSameEmployeeName(matches) {
			if bengali {
				answer += "\n\n\u098f\u0995\u0987 \u09a8\u09be\u09ae\u09c7 \u098f\u0995\u09be\u09a7\u09bf\u0995 \u0995\u09b0\u09cd\u09ae\u09c0 \u0986\u099b\u09c7\u09a8; \u09a8\u09bf\u09b0\u09cd\u09a6\u09bf\u09b7\u09cd\u099f \u0995\u09b0\u09a4\u09c7 \u09ac\u09bf\u09ad\u09be\u0997 \u09ac\u09be Staff ID \u09ac\u09b2\u09c1\u09a8\u0964"
			} else {
				answer += "\n\nSeveral employees share this name. Specify a department or Staff ID to choose one."
			}
		}
		writeJSON(w, chatResponse{
			Answer:    answer,
			Employees: matches,
			Mode:      "direct_lookup",
			Provider:  "Verified Staff Directory (" + activeProv + ")",
			Engine:    activeEng,
		})
		return
	}

	if isEmployeeAttributeQuestion(msg) {
		writeJSON(w, chatResponse{
			Answer:   "Please include the employee's name. Joining dates and blood groups are shown only when HR has provided them in the directory.",
			Mode:     "no_match",
			Provider: activeProv,
			Engine:   activeEng,
		})
		return
	}

	if isHeadDirectoryQuestion(msg) {
		answer := "I could not find a verified department head matching that department in the current HR directory."
		if bengali {
			answer = "বর্তমান HR ডিরেক্টরিতে এই বিভাগের জন্য যাচাইকৃত head-এর তথ্য পাওয়া যায়নি।"
		}
		writeJSON(w, chatResponse{Answer: answer, Mode: "no_match", Provider: "Verified Staff Directory (" + activeProv + ")", Engine: activeEng})
		return
	}

	// 6. IF SPECIFICALLY A DAILY STAR DIRECTORY INQUIRY (e.g. mentions daily star/desk/reporters) BUT NO DIRECT MATCH:
	if isDirectoryQuestion(msg) {
		results, _ := s.Store.SemanticSearch(s.Ollama, msg, 8, 0.45)
		if len(results) > 0 {
			writeJSON(w, chatResponse{
				Answer:    formatMultipleEmployees(msg, results, bengali),
				Employees: results,
				Mode:      "semantic",
				Provider:  "Directory Semantic Search (" + activeProv + ")",
				Engine:    activeEng,
			})
			return
		}

		noMatchMsg := fmt.Sprintf("The Daily Star-এর ডিরেক্টরিতে **%q**-এর সাথে মিলে এমন কাউকে খুঁজে পাওয়া যায়নি। অনুগ্রহ করে সঠিক নাম বা বিভাগ লিখে আবার বলুন।", msg)
		if !bengali {
			noMatchMsg = fmt.Sprintf("I could not find anyone in The Daily Star directory matching %q. Please check the spelling or ask by department.", msg)
		}
		writeJSON(w, chatResponse{
			Answer:   noMatchMsg,
			Mode:     "no_match",
			Provider: activeProv,
			Engine:   activeEng,
		})
		return
	}

	// 7. GENERAL AI QUESTION (ChatGPT Mode)
	// No employee matched, and not a directory query -> Clean ChatGPT answer with ZERO employee cards!
	generalSystem := "You are a helpful, intelligent, polite AI assistant (like ChatGPT) built and developed by Jaber Patwary (জাবের পাটোয়ারী) for The Daily Star newspaper in Bangladesh. " +
		"IMPORTANT RULE: If the user asks who made, created, built, or developed you (in Bengali, Banglish, or English), you must ALWAYS state that you were created and developed by Jaber Patwary (জাবের পাটোয়ারী). " +
		"Answer the user's question conversationally, clearly, and accurately. " +
		"Do NOT mention internal staff, employees, or employee records unless the user specifically asks about them. " +
		"Format your answer using markdown nicely. " +
		"If the question is in Bengali or Banglish, reply in natural fluent Bengali. If in English, reply in English."

	// Do not send browser-supplied history to a cloud model; it can contain directory data.
	generalUser := msg

	answer, providerUsed, engineUsed := s.askLLM(generalSystem, generalUser)
	if answer == "" {
		if bengali {
			answer = "দুঃখিত, আমি এই মুহূর্তে উত্তর দিতে পারছি না। অনুগ্রহ করে কিছুক্ষণ পর আবার চেষ্টা করুন।"
		} else {
			answer = "Sorry, I am having trouble answering right now. Please try again."
		}
	}

	writeJSON(w, chatResponse{
		Answer:    answer,
		Employees: nil, // Clean response with NO employee cards!
		Mode:      "general",
		Provider:  providerUsed,
		Engine:    engineUsed,
	})
}

// handleReload re-reads the CSV and rebuilds all embeddings. Call this
// whenever HR updates the source file.
func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	log.Println("reloading employee data from CSV...")
	if err := s.Store.BuildFromCSV(s.CSVPath, s.Ollama); err != nil {
		log.Printf("reload failed: %v", err)
		http.Error(w, "reload failed; check server logs", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"status": "ok", "employees_indexed": s.Store.Count()})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	groqActive := s.Groq != nil && s.Groq.IsConfigured()
	provider := "ollama"
	model := s.Ollama.ChatModel
	if groqActive {
		provider = "groq"
		model = s.Groq.Model
	}
	sourceUpdatedAt := "unknown"
	if sourceInfo, err := os.Stat(s.CSVPath); err == nil {
		sourceUpdatedAt = sourceInfo.ModTime().Format(time.RFC3339)
	}
	writeJSON(w, map[string]any{
		"status":            "ok",
		"employees_indexed": s.Store.Count(),
		"data_quality":      s.Store.DataQualityReport(),
		"source_updated_at": sourceUpdatedAt,
		"provider":          provider,
		"model":             model,
		"groq_active":       groqActive,
	})
}

type configRequest struct {
	GroqAPIKey      string `json:"groq_api_key"`
	GroqModel       string `json:"groq_model,omitempty"`
	PreferredEngine string `json:"preferred_engine,omitempty"`
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		activeProvider := "Local Ollama"
		activeModel := s.Ollama.ChatModel
		groqConfigured := s.Groq != nil && s.Groq.IsConfigured()
		if groqConfigured && s.PreferredEngine != "ollama" {
			activeProvider = "Groq Cloud LPU (Ultra Fast)"
			activeModel = s.Groq.Model
		}

		writeJSON(w, map[string]any{
			"groq_configured":  groqConfigured,
			"groq_masked_key":  s.Groq.MaskedKey(),
			"groq_model":       s.Groq.Model,
			"ollama_url":       s.Ollama.BaseURL,
			"ollama_model":     s.Ollama.ChatModel,
			"embed_model":      s.Ollama.EmbedModel,
			"preferred_engine": s.PreferredEngine,
			"active_provider":  activeProvider,
			"active_model":     activeModel,
		})
		return
	}

	if r.Method == http.MethodPost {
		var req configRequest
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		// Switch active engine directly if requested
		if req.PreferredEngine != "" {
			if req.PreferredEngine == "ollama" || (req.PreferredEngine == "groq" && s.Groq.IsConfigured()) {
				s.PreferredEngine = req.PreferredEngine
				writeJSON(w, map[string]any{
					"status":           "ok",
					"preferred_engine": s.PreferredEngine,
					"message":          fmt.Sprintf("Active AI Engine switched to %s", s.PreferredEngine),
				})
				return
			}
		}

		apiKey := strings.TrimSpace(req.GroqAPIKey)
		model := strings.TrimSpace(req.GroqModel)
		if model == "" {
			model = "openai/gpt-oss-120b"
		}

		if apiKey != "" {
			// Verify key by testing with Groq
			tempClient := groq.New(apiKey, model)
			if err := tempClient.Test(); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				writeJSON(w, map[string]any{
					"status":  "error",
					"message": fmt.Sprintf("Groq API verification failed: %v", err),
				})
				return
			}

			// Update server Groq client
			s.Groq.SetAPIKey(apiKey)
			s.Groq.Model = model

			writeJSON(w, map[string]any{
				"status":  "ok",
				"message": "Groq API key verified for this running session. Set it in the server environment to persist it after restart.",
				"model":   model,
			})
			return
		} else {
			// Empty key resets to local Ollama
			s.Groq.SetAPIKey("")
			writeJSON(w, map[string]any{
				"status":  "ok",
				"message": "Groq API key removed. Reverted to Local Ollama.",
			})
			return
		}
	}

	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func buildContext(emps []models.Employee) string {
	var b strings.Builder
	for _, e := range emps {
		b.WriteString("- " + e.Text + "\n")
	}
	return b.String()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
