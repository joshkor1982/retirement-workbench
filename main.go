// ARW (Army Retirement Workbench) - a local retirement-transition command center.
//
// One binary, one JSON state file, no cloud, no accounts. It tracks the
// whole transition from active duty to retired: the milestone timeline, the
// to-do list it generates, appointments, uploaded documents, the budget,
// the resume, and federal job searches.
//
// Run it:
//
//	go run . [-port 5252] [-data <folder>]
//	open http://127.0.0.1:5252/
//
// Storage is a single JSON file in your data folder (plus uploaded documents),
// kept outside the source tree by default; see datadir.go.
// Everything binds to loopback only.
package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// ---------- Model ----------------------------------------------------------

type Settings struct {
	Name            string  `json:"name"`
	Branch          string  `json:"branch"`
	RetirementDate  string  `json:"retirement_date"` // YYYY-MM-DD
	USAJobsEmail    string  `json:"usajobs_email"`
	USAJobsKey      string  `json:"usajobs_key"`
	WeatherHere     string  `json:"weather_here,omitempty"` // ZIP or city
	WeatherRetire   string  `json:"weather_retire,omitempty"`
	AdzunaID        string  `json:"adzuna_id,omitempty"`
	AdzunaKey       string  `json:"adzuna_key,omitempty"`
	VaEstimate      int     `json:"va_estimate,omitempty"` // working percent, user-set
	ResumeHeadline  string  `json:"resume_headline,omitempty"`
	ResumeContact   string  `json:"resume_contact,omitempty"`
	LinksSeeded     bool    `json:"links_seeded,omitempty"`
	AdvisorProvider string  `json:"advisor_provider,omitempty"` // "" (off) | claude | chatgpt; see ai.go
	MonthlyBudget   int64   `json:"monthly_budget_cents"`
	PowerEngine     int64   `json:"power_engine_cents"`
	MonthlyIncome   int64   `json:"monthly_income_cents"`
	PayMid          int64   `json:"pay_mid_cents,omitempty"`
	PayEnd          int64   `json:"pay_end_cents,omitempty"`
	DebtBaseline    int64   `json:"debt_baseline_cents"`
	DebtFreeBy      string  `json:"debt_free_by"` // YYYY-MM-DD goal date
	LeaveDays       float64 `json:"leave_days,omitempty"`
	LeaveSet        bool    `json:"leave_set,omitempty"`
	LeaveAsOf       string  `json:"leave_as_of,omitempty"` // YYYY-MM-DD the balance was read
	RetSystem       string  `json:"ret_system,omitempty"`  // high3 | brs
	RetYears        float64 `json:"ret_years,omitempty"`
	RetHigh3        int64   `json:"ret_high3_cents,omitempty"`
	RetSBP          bool    `json:"ret_sbp,omitempty"`
	RetSpouse       bool    `json:"ret_spouse,omitempty"`      // VA dependents: spouse
	RetKids         int     `json:"ret_kids,omitempty"`        // children under 18
	RetSchoolKids   int     `json:"ret_school_kids,omitempty"` // children 18 to 23 in school
	RetParents      int     `json:"ret_parents,omitempty"`     // dependent parents
	SavingsGoal     int64   `json:"savings_goal_cents,omitempty"`
	SavingsGoalName string  `json:"savings_goal_name,omitempty"`
	TimelineSeeded  bool    `json:"timeline_seeded"`
}

type Todo struct {
	ID     int    `json:"id"`
	Title  string `json:"title"`
	Notes  string `json:"notes"`
	Due    string `json:"due"`             // YYYY-MM-DD, may be empty
	Start  string `json:"start,omitempty"` // range start for the chart; empty = point
	Done   bool   `json:"done"`
	Source string `json:"source"` // timeline | doc | manual
	Phase  string `json:"phase,omitempty"`
}

type Appointment struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	At    string `json:"at"` // YYYY-MM-DDTHH:MM
	Place string `json:"place"`
	Notes string `json:"notes"`
}

type Doc struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	File       string `json:"file"` // path under data dir
	UploadedAt string `json:"uploaded_at"`
	Notes      string `json:"notes"`
	Kind       string `json:"kind,omitempty"`   // "" general | medical | dbq | resume | packet | les | form
	Filled     bool   `json:"filled,omitempty"` // for kind=form: filled out yet
}

type AnalyzedCondition struct {
	Name      string `json:"name"`
	Percent   int    `json:"percent"`
	Bilateral bool   `json:"bilateral"`
	Code      string `json:"code,omitempty"`
	Rationale string `json:"rationale"`
}

type ClaimAnalysis struct {
	At       string              `json:"at"`
	Combined int                 `json:"combined"`
	Color    string              `json:"color"` // css suffix: low|mid|high|top
	Summary  string              `json:"summary"`
	Items    []AnalyzedCondition `json:"items"`
	Provider string              `json:"provider"`
}

type AdvisorEntry struct {
	Q  string `json:"q"`
	A  string `json:"a"`
	At string `json:"at"`
}

type Resource struct {
	ID    int    `json:"id"`
	Name  string `json:"name"` // office or program
	Org   string `json:"org"`  // agency behind it
	Info  string `json:"info"` // phone / email / URL / building
	Notes string `json:"notes"`
}

type Link struct {
	ID       int    `json:"id"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	Category string `json:"category"`
	Notes    string `json:"notes"`
}

type Med struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Dose       string `json:"dose"`
	For        string `json:"for"`
	Prescriber string `json:"prescriber"`
	Notes      string `json:"notes"`
}

type Condition struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Documented bool   `json:"documented"` // in the medical record
	DBQ        bool   `json:"dbq"`        // DBQ studied
	Notes      string `json:"notes"`
}

type Symptom struct {
	ID        int    `json:"id"`
	Date      string `json:"date"`
	Condition string `json:"condition"`
	Note      string `json:"note"`
}

type Txn struct {
	ID       int    `json:"id"`
	Date     string `json:"date"`         // YYYY-MM-DD
	Amount   int64  `json:"amount_cents"` // negative = spend, positive = income
	Category string `json:"category"`
	Note     string `json:"note"`
}

type ResumeTarget struct {
	ID           int             `json:"id"`
	Position     string          `json:"position"`
	Company      string          `json:"company"`
	Requirements string          `json:"requirements"`
	Headline     string          `json:"headline"`
	Sections     []ResumeSection `json:"sections"`
	Design       string          `json:"design,omitempty"` // classic | modern | executive | minimal
	Dark         bool            `json:"dark,omitempty"`
	Font         string          `json:"font,omitempty"`
	Color        string          `json:"color,omitempty"`
}

type ResumeSection struct {
	ID      int      `json:"id"`
	Heading string   `json:"heading"`
	Bullets []string `json:"bullets"`
}

type Bill struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Amount int64  `json:"amount_cents"`
}

type Debt struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	APR      string `json:"apr"`           // display string; "TBD" until captured
	Balance  int64  `json:"balance_cents"` // 0 with APR TBD = not captured yet
	Min      int64  `json:"min_cents"`
	Priority int    `json:"priority"`
	PaidOff  bool   `json:"paid_off"`
}

type Prospect struct {
	ID     int    `json:"id"`
	Title  string `json:"title"`
	Org    string `json:"org"`
	URL    string `json:"url"`
	Status string `json:"status"` // found | applied | interview | offer
	Notes  string `json:"notes"`
	Added  string `json:"added"`
}

type Contact struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Org   string `json:"org"`
	Role  string `json:"role"`
	Info  string `json:"info"` // email / phone / LinkedIn
	Notes string `json:"notes"`
}

type JobSearch struct {
	ID       int    `json:"id"`
	Keyword  string `json:"keyword"`
	Location string `json:"location"` // free text, from older builds
	Zip      string `json:"zip,omitempty"`
	Radius   int    `json:"radius,omitempty"`
	Mode     string `json:"mode,omitempty"` // near | remote | both
}

type State struct {
	Settings      Settings          `json:"settings"`
	Todos         []Todo            `json:"todos"`
	Appointments  []Appointment     `json:"appointments"`
	Docs          []Doc             `json:"docs"`
	Txns          []Txn             `json:"txns"`
	Resume        []ResumeSection   `json:"resume"`
	ResumeTargets []ResumeTarget    `json:"resume_targets,omitempty"`
	ITP           map[string]string `json:"itp,omitempty"`
	JobSearches   []JobSearch       `json:"job_searches"`
	Debts         []Debt            `json:"debts"`
	Bills         []Bill            `json:"bills"`
	Conditions    []Condition       `json:"conditions"`
	Meds          []Med             `json:"meds,omitempty"`
	Prospects     []Prospect        `json:"prospects"`
	Contacts      []Contact         `json:"contacts"`
	Resources     []Resource        `json:"resources"`
	Links         []Link            `json:"links,omitempty"`
	Advisor       []AdvisorEntry    `json:"advisor,omitempty"`
	Analysis      *ClaimAnalysis    `json:"analysis,omitempty"`
	Symptoms      []Symptom         `json:"symptoms"`
	Housing       *HouseSearch      `json:"housing,omitempty"`
	PARDone       map[string]string `json:"par_done,omitempty"` // PAR card key -> "done|YYYY-MM-DD" or "na|..."
	Notes         []Note            `json:"notes,omitempty"`
	Savings       []SavingsEntry    `json:"savings,omitempty"`
	SkillBridge   *SBPlan           `json:"skillbridge,omitempty"`
	SBLeads       []SBLead          `json:"sb_leads,omitempty"`
	NextID        int               `json:"next_id"`
}

// ---------- Store ----------------------------------------------------------

type Store struct {
	mu   sync.Mutex
	path string
	st   State
}

func openStore(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o700); err != nil {
		return nil, err
	}
	tightenPerms(dir)
	s := &Store{path: filepath.Join(dir, "state.json")}
	b, err := os.ReadFile(s.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		s.st = State{NextID: 1}
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, &s.st); err != nil {
			return nil, fmt.Errorf("state.json is corrupt: %w", err)
		}
		migrateAI(&s.st)
	}
	return s, nil
}

// mutate runs fn on a copy under the lock and commits it only once the new
// state is safely on disk, so a failed save never shows an edit that a
// restart would lose. The write is atomic: temp file, fsync, rename.
func (s *Store) mutate(fn func(*State)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := json.Marshal(s.st)
	if err != nil {
		return err
	}
	var next State
	if err := json.Unmarshal(cur, &next); err != nil {
		return err
	}
	fn(&next)
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(s.path, b); err != nil {
		log.Printf("save failed, change not kept: %v", err)
		return err
	}
	s.st = next
	return nil
}

// writeFileAtomic replaces path with b, readable only by you.
func writeFileAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Store) snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal(s.st)
	var out State
	_ = json.Unmarshal(b, &out)
	return out
}

func (s *State) id() int { v := s.NextID; s.NextID++; return v }

// ---------- The transition timeline ----------------------------------------

// milestone offsets are days relative to the retirement date; negative
// means before. These are the standard federal/military windows; every
// generated to-do is editable, so treat them as the starting picture,
// not regulation. Verify service-specific dates with your transition
// counselor.
type milestone struct {
	Offset int
	Phase  string
	Title  string
	Notes  string
}

// The template is a general Army retirement plan, expressed as day offsets
// from the retirement date so the same plan scales if the date moves. Hard
// gates carry their own warnings. Some steps (the flight, the pack-out) only
// apply if you retire from an overseas assignment; delete what does not fit.
var transitionTemplate = []milestone{
	// Phase 1 · packet in motion + setup
	{-465, "Phase 1 · Packet and Setup", "Verify DD 4s and service dates in iPERMS", "Before the service calc goes in."},
	{-465, "Phase 1 · Packet and Setup", "Turn in the packet for the service computation", "Calc comes back in about two weeks."},
	{-451, "Phase 1 · Packet and Setup", "Submit the PAR in IPPS-A", "HARD GATE: submit the day the calc returns. Orders take 3 to 6 months and gate the flight, clearing, and the move."},
	{-451, "Phase 1 · Packet and Setup", "Start a personal symptom log", "Every condition, every date. The claim is built from this."},
	{-427, "Phase 1 · Packet and Setup", "Set up a professional email and LinkedIn profile", "Your resume, email, and profile should all point at each other."},
	{-397, "Phase 1 · Packet and Setup", "Check your leave against the use-or-lose cap", "Anything over 60 days on 30 September is forfeited. Plan leave now so none is lost."},
	{-366, "Phase 1 · Packet and Setup", "Enroll in SFL-TAP (DD Form 2648)", "No later than 365 days out."},
	{-336, "Phase 1 · Packet and Setup", "Resume drafted and networking started", "A first draft now; tailor it per job later."},

	// Phase 2 · applications while orders pend
	{-396, "Phase 2 · Applications", "Enter the new fiscal year at or under 60 days leave", "Then hold leave; the balance becomes terminal leave."},
	{-305, "Phase 2 · Applications", "Begin federal applications on USAJobs", "The pipeline runs 4 to 8 months; aim at a start date on terminal leave."},
	{-300, "Phase 2 · Applications", "Track the PAR in IPPS-A weekly", "Chase it if it stalls."},
	{-274, "Phase 2 · Applications", "Finish any certification your target jobs require", "Check real job postings for required certifications (for example, Security+ for many DoD IT roles) and schedule the exam early."},
	{-246, "Phase 2 · Applications", "Work samples ready to show", "A portfolio, writing samples, or a project that shows a civilian employer what you can do. Never include classified or controlled material."},

	// Phase 3 · orders in hand, overseas endgame
	{-246, "Phase 3 · On Orders", "Receive retirement orders", ""},
	{-215, "Phase 3 · On Orders", "Book the flight and the clearing appointments", "Flight through transportation/CTO. CIF, HHG, housing, finance: book immediately; backlogs kill schedules."},
	{-215, "Phase 3 · On Orders", "Pull the VA Certificate of Eligibility", "Minutes on VA.gov once orders exist."},
	{-215, "Phase 3 · On Orders", "Begin contractor and corporate applications", ""},
	{-185, "Phase 3 · On Orders", "Clean up credit and open no new debt", "The mortgage file starts now."},
	{-154, "Phase 3 · On Orders", "Request complete medical and dental record copies", "Full STR copies."},
	{-154, "Phase 3 · On Orders", "Find a VSO and share records for claim review", "Before filing."},
	{-139, "Phase 3 · On Orders", "HHG pack-out into temporary storage", ""},
	{-131, "Phase 3 · On Orders", "Pre-clear early unit items", ""},
	{-124, "Phase 3 · On Orders", "Army retirement physical and dental", "Every condition documented. Study the DBQs for each claimed condition."},
	{-124, "Phase 3 · On Orders", "Remote lender pre-approval and a buyer's agent lined up", "VA-experienced lenders at the destination; pension documented by the calc and orders."},
	{-123, "Phase 3 · On Orders", "SBP election (DD 2656) signed and NOTARIZED", "HARD GATE: spousal concurrence, notarized at the losing installation BEFORE departing. It must be done before terminal leave."},
	{-121, "Phase 3 · On Orders", "Final out: clear the installation", "About ten days of clearing before the flight."},
	{-121, "Phase 3 · On Orders", "Fly to the retirement destination", "If you are overseas: the PCS flight home."},

	// Phase 4 · PTDY at the destination
	{-120, "Phase 4 · PTDY", "PTDY begins (DA Form 31)", "Thirty days, job and house hunting justification."},
	{-116, "Phase 4 · PTDY", "File the BDD claim on arrival (VA 21-526EZ)", "HARD GATE: the window closes at 90 days out. File within days of landing so every C&P exam is local."},
	{-96, "Phase 4 · PTDY", "House hunt and make the offer", "Lenders will not count military pay ending within a year: qualifying income is the pension plus the civilian offer letter."},
	{-93, "Phase 4 · PTDY", "Sign the civilian job offer", "It is part of the mortgage file."},
	{-90, "Phase 4 · PTDY", "Terminal leave begins (DA Form 31)", "The civilian job may legally start the same day: both pays run to the end."},

	// Phase 5 · on terminal leave
	{-62, "Phase 5 · Terminal Leave", "VA appraisal, inspection, rate lock", "Two to three weeks for the appraisal."},
	{-32, "Phase 5 · Terminal Leave", "Attend every C&P exam", "HARD GATE: a missed exam stalls the whole rating."},
	{-32, "Phase 5 · Terminal Leave", "Close on the house", "Thirty to forty-five days from contract; budget 2 to 4 percent closing costs."},
	{-30, "Phase 5 · Terminal Leave", "Review the DD-214 draft against the DD 4s", "Fixing it now takes minutes."},
	{-30, "Phase 5 · Terminal Leave", "HHG delivery from storage, timed to closing", ""},
	{-24, "Phase 5 · Terminal Leave", "Apply for VA health care", "Thirty to sixty days before retirement, separate from the disability claim."},
	{-17, "Phase 5 · Terminal Leave", "Move in and settle", "Before retirement day."},
	{-15, "Phase 5 · Terminal Leave", "Confirm leftover leave sells on the final pay", ""},

	// Phase 6 · retired
	{0, "Phase 6 · Retired", "Retirement day", "Done. Take the day."},
	{1, "Phase 6 · Retired", "Upload the DD-214 to VA.gov", "The day it is issued; the BDD rating decision follows."},
	{14, "Phase 6 · Retired", "CAC in, retiree ID out, family DEERS updated", "One visit to the ID card office; book online, bring the DD-214, orders, and a second ID."},
	{14, "Phase 6 · Retired", "Enroll in the TRICARE retiree plan", "The window is 90 days from retirement."},
	{30, "Phase 6 · Retired", "Verify the first retired pay deposit and the SBP deduction", ""},
	{45, "Phase 6 · Retired", "Reconnect with the VSO on the rating decision", "VA compensation begins the month after separation."},
}

func seedTimeline(st *State, rday time.Time) {
	for _, m := range transitionTemplate {
		st.Todos = append(st.Todos, Todo{
			ID:     st.id(),
			Title:  m.Title,
			Notes:  m.Notes,
			Due:    rday.AddDate(0, 0, m.Offset).Format("2006-01-02"),
			Source: "timeline",
			Phase:  m.Phase,
		})
	}
	st.Settings.TimelineSeeded = true
}

// ---------- Server ----------------------------------------------------------

type Server struct {
	store *Store
	tmpl  *template.Template
	data  string
	homes *homeFeed
	quit  func() // set by main; nil in tests
}

func main() {
	port := flag.Int("port", 5252, "port to listen on")
	addrFlag := flag.String("addr", envOr("RW_ADDR", ""), "full listen address, e.g. 0.0.0.0:5252 in a container (default 127.0.0.1:<port>; env RW_ADDR)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	healthcheck := flag.Bool("healthcheck", false, "probe a running app's /healthz and exit 0 if healthy (for container health checks)")
	openBrowser := flag.Bool("open", envOr("RW_OPEN_BROWSER", "1") != "0", "open the app in your browser on start (env RW_OPEN_BROWSER=0 turns it off)")
	dataFlag := flag.String("data", "", "data folder (default: RW_DATA, then ./data if it exists, then your per-user app data folder)")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	if *healthcheck {
		probe := *addrFlag
		if probe == "" || strings.HasPrefix(probe, "0.0.0.0:") || strings.HasPrefix(probe, ":") {
			_, p, _ := strings.Cut(probe, ":")
			if p == "" {
				p = strconv.Itoa(*port)
			}
			probe = "127.0.0.1:" + p
		}
		resp, err := (&http.Client{Timeout: 3 * time.Second}).Get("http://" + probe + "/healthz")
		if err != nil || resp.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	addr := *addrFlag
	if addr == "" {
		addr = fmt.Sprintf("127.0.0.1:%d", *port)
	}
	// Claim the port before touching the data folder. If ARW already holds
	// it, open that copy instead of starting a second one.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if alreadyRunning(addr) {
			fmt.Printf("ARW is already running at http://%s/\n", addr)
			if *openBrowser && *addrFlag == "" {
				openInBrowser("http://" + addr + "/")
			}
			return
		}
		log.Fatalf("cannot listen on %s: %v (another program is using that port; try -port 5353)", addr, err)
	}

	dir, why := resolveDataDir(*dataFlag)
	log.Printf("data folder: %s (%s)", dir, why)
	s, handler, err := newApp(dir)
	if err != nil {
		log.Fatal(err)
	}
	go s.homes.run()

	if !strings.HasPrefix(addr, "127.0.0.1:") && !strings.HasPrefix(addr, "localhost:") {
		log.Printf("WARNING: listening on %s. This app has no login; reach it through a tunnel (kubectl port-forward, SSH) or an authenticating proxy, never an open port.", addr)
	}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No WriteTimeout: Advisor answers and packet merges can take minutes.
	}
	shutdown := func() {
		log.Printf("shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx) // lets in-flight saves finish; the store writes atomically
	}
	s.quit = func() { go func() { time.Sleep(300 * time.Millisecond); shutdown() }() }
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() { <-stop; shutdown() }()

	log.Printf("ARW (Army Retirement Workbench) %s listening on http://%s/", version, addr)
	if hosts := extraHosts(); len(hosts) > 0 {
		log.Printf("also answering for: %s", strings.Join(hosts, ", "))
	}
	if *addrFlag == "" {
		fmt.Printf("\n  ARW is running at http://%s/\n  Leave this running while you use it. To stop, click Quit ARW in Settings, or press Ctrl+C here.\n\n", addr)
		if *openBrowser {
			go openInBrowser("http://" + addr + "/")
		}
	}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	fmt.Println("ARW has stopped.")
}

// alreadyRunning reports whether the thing on addr is ARW.
func alreadyRunning(addr string) bool {
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Get("http://" + addr + "/healthz")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

// newApp opens the data folder and wires every route. It starts nothing in
// the background, so tests can build a full app against a temp folder.
func newApp(dataDir string) (*Server, http.Handler, error) {
	store, err := openStore(dataDir)
	if err != nil {
		return nil, nil, err
	}
	funcs := template.FuncMap{
		"navGroups":  func() []navGroup { return sideNav },
		"navIcon":    navIcon,
		"navIconSet": navIconsJSON,
		"pageIcon": func(page string) string {
			for _, g := range sideNav {
				for _, it := range g.Items {
					if it.Page == page {
						return it.Icon
					}
				}
			}
			return "settings"
		},
		"host": func(raw string) string {
			if u, err := url.Parse(raw); err == nil && u.Host != "" {
				return strings.TrimPrefix(u.Host, "www.")
			}
			return raw
		},
		"money": money,
		"nicedate": func(s string) string {
			t, err := parseDay(s)
			if err != nil {
				return s
			}
			return t.Format("Mon, Jan 2 2006")
		},
		"atdate": func(s string) string {
			for _, layout := range []string{"2006-01-02T15:04", time.RFC3339, "2006-01-02"} {
				if t, err := time.Parse(layout, s); err == nil {
					return t.Format("Jan 2, 2006")
				}
			}
			return s
		},
		"div100": func(a, b int) int { return a * 100 / b },
		"attime": func(s string) string {
			for _, layout := range []string{"2006-01-02T15:04", time.RFC3339} {
				if t, err := time.Parse(layout, s); err == nil {
					return t.Format("15:04")
				}
			}
			return ""
		},
	}
	tmpl, err := template.New("").Funcs(funcs).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, nil, err
	}
	s := &Server{store: store, tmpl: tmpl, data: dataDir, homes: newHomeFeed(dataDir, store)}

	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.FileServerFS(staticFS))
	mux.HandleFunc("GET /{$}", s.dashboard)
	mux.HandleFunc("GET /search", s.search)
	mux.HandleFunc("GET /weather.json", s.weatherJSON)
	mux.HandleFunc("GET /timeline", s.timeline)
	mux.HandleFunc("GET /todos", s.todos)
	mux.HandleFunc("POST /todos", s.todoAdd)
	mux.HandleFunc("POST /todos/{id}/toggle", s.todoToggle)
	mux.HandleFunc("POST /todos/{id}/update", s.todoUpdate)
	mux.HandleFunc("POST /todos/{id}/delete", s.todoDelete)
	mux.HandleFunc("GET /notes", s.notes)
	mux.HandleFunc("POST /notes", s.noteAdd)
	mux.HandleFunc("POST /notes/{id}/update", s.noteUpdate)
	mux.HandleFunc("POST /notes/{id}/delete", s.noteDelete)
	mux.HandleFunc("GET /appointments", s.appointments)
	mux.HandleFunc("POST /appointments", s.apptAdd)
	mux.HandleFunc("POST /appointments/{id}/delete", s.apptDelete)
	mux.HandleFunc("GET /docs", s.docs)
	mux.HandleFunc("POST /docs", s.docUpload)
	mux.HandleFunc("POST /docs/bulk", s.docBulkUpload)
	mux.HandleFunc("GET /docs/{id}/file", s.docFile)
	mux.HandleFunc("POST /docs/{id}/todo", s.docTodo)
	mux.HandleFunc("POST /docs/{id}/rename", s.docRename)
	mux.HandleFunc("POST /docs/{id}/delete", s.docDelete)
	mux.HandleFunc("POST /docs/{id}/form", s.docFlagForm)
	mux.HandleFunc("POST /docs/{id}/filled", s.docToggleFilled)
	mux.HandleFunc("GET /packet", s.packet)
	mux.HandleFunc("POST /packet/upload", s.packetUpload)
	mux.HandleFunc("GET /packet/supporting.pdf", s.packetSupporting)
	mux.HandleFunc("GET /packet/full.pdf", s.packetFull)
	mux.HandleFunc("POST /packet/{id}/delete", s.packetDelete)
	mux.HandleFunc("POST /packet/{id}/up", s.packetMove)
	mux.HandleFunc("POST /packet/{id}/down", s.packetMove)
	mux.HandleFunc("POST /packet-par/{key}", s.parMark)
	mux.HandleFunc("POST /advisor/ask", s.advisorAsk)
	mux.HandleFunc("GET /resources", s.resources)
	mux.HandleFunc("POST /resources/contacts", s.resourceAdd)
	mux.HandleFunc("POST /resources/contacts/{id}/delete", s.resourceDelete)
	mux.HandleFunc("POST /resources/links", s.linkAdd)
	mux.HandleFunc("POST /resources/links/{id}/delete", s.linkDelete)
	mux.HandleFunc("GET /budget", s.budget)
	mux.HandleFunc("POST /budget", s.txnAdd)
	mux.HandleFunc("POST /budget/{id}/delete", s.txnDelete)
	mux.HandleFunc("POST /bills", s.billAdd)
	mux.HandleFunc("POST /bills/{id}/update", s.billUpdate)
	mux.HandleFunc("POST /bills/{id}/delete", s.billDelete)
	mux.HandleFunc("POST /budget/income", s.incomeSave)
	mux.HandleFunc("POST /budget/goal", s.budgetGoalSave)
	mux.HandleFunc("POST /budget/retirepay", s.retirePaySave)
	mux.HandleFunc("POST /savings", s.savingsAdd)
	mux.HandleFunc("POST /savings/goal", s.savingsGoal)
	mux.HandleFunc("POST /savings/{id}/delete", s.savingsDelete)
	mux.HandleFunc("POST /debts", s.debtAdd)
	mux.HandleFunc("POST /debts/{id}/delete", s.debtDelete)
	mux.HandleFunc("POST /debts/{id}/update", s.debtUpdate)
	mux.HandleFunc("POST /debts/{id}/toggle", s.debtToggle)
	mux.HandleFunc("GET /itp", s.itp)
	mux.HandleFunc("POST /itp", s.itpSave)
	mux.HandleFunc("GET /resume", s.resume)
	mux.HandleFunc("GET /resume/print", s.resumePrint)
	mux.HandleFunc("GET /resume/pdf", s.resumePDF)
	mux.HandleFunc("POST /resume/header", s.resumeHeader)
	mux.HandleFunc("POST /resume/targets", s.resumeTargetAdd)
	mux.HandleFunc("POST /resume/targets/{id}/delete", s.resumeTargetDelete)
	mux.HandleFunc("POST /resume/targets/{id}/save", s.resumeTargetSave)
	mux.HandleFunc("POST /resume/targets/{id}/style", s.resumeStyle)
	mux.HandleFunc("POST /resume/targets/{id}/sections", s.resumeSectionAdd)
	mux.HandleFunc("POST /resume/targets/{id}/sections/{sid}/update", s.resumeSectionEdit)
	mux.HandleFunc("POST /resume/targets/{id}/sections/{sid}/up", s.resumeSectionMove)
	mux.HandleFunc("POST /resume/targets/{id}/sections/{sid}/down", s.resumeSectionMove)
	mux.HandleFunc("POST /resume/targets/{id}/sections/{sid}/delete", s.resumeSectionDelete)
	mux.HandleFunc("GET /jobs", s.jobs)
	mux.HandleFunc("GET /skillbridge", s.skillbridge)
	mux.HandleFunc("POST /skillbridge/plan", s.sbPlanSave)
	mux.HandleFunc("POST /skillbridge/leads", s.sbLeadAdd)
	mux.HandleFunc("POST /skillbridge/leads/{id}/advance", s.sbLeadAdvance)
	mux.HandleFunc("POST /skillbridge/leads/{id}/delete", s.sbLeadDelete)
	mux.HandleFunc("GET /skillbridge/memo", s.sbMemoPrint)
	mux.HandleFunc("GET /skillbridge/memo.pdf", s.sbMemoPDF)
	mux.HandleFunc("POST /jobs/prospects", s.prospectAdd)
	mux.HandleFunc("POST /jobs/prospects/{id}/advance", s.prospectAdvance)
	mux.HandleFunc("POST /jobs/prospects/{id}/delete", s.prospectDelete)
	mux.HandleFunc("POST /jobs/contacts", s.contactAdd)
	mux.HandleFunc("POST /jobs/contacts/{id}/delete", s.contactDelete)
	mux.HandleFunc("POST /jobs/searches", s.jobSearchAdd)
	mux.HandleFunc("POST /jobs/searches/{id}/delete", s.jobSearchDelete)
	mux.HandleFunc("GET /housing", s.housing)
	mux.HandleFunc("GET /housing/stamp", s.housingStamp)
	mux.HandleFunc("POST /housing/search", s.housingSearch)
	mux.HandleFunc("POST /housing/refresh", s.housingRefresh)
	mux.HandleFunc("GET /medical", s.medical)
	mux.HandleFunc("POST /medical/analyze", s.medicalAnalyze)
	mux.HandleFunc("POST /medical/meds", s.medAdd)
	mux.HandleFunc("POST /medical/meds/{id}/delete", s.medDelete)
	mux.HandleFunc("POST /medical/conditions", s.conditionAdd)
	mux.HandleFunc("POST /medical/conditions/{id}/toggle", s.conditionToggle)
	mux.HandleFunc("POST /medical/conditions/{id}/delete", s.conditionDelete)
	mux.HandleFunc("POST /medical/symptoms", s.symptomAdd)
	mux.HandleFunc("POST /medical/symptoms/{id}/delete", s.symptomDelete)
	mux.HandleFunc("POST /timeline/tasks", s.timelineTaskAdd)
	mux.HandleFunc("GET /settings", s.settings)
	mux.HandleFunc("POST /settings", s.settingsSave)
	mux.HandleFunc("POST /settings/seed", s.settingsSeed)
	mux.HandleFunc("POST /settings/demo", s.settingsDemo)
	mux.HandleFunc("POST /settings/erase", s.settingsErase)
	mux.HandleFunc("POST /settings/quit", s.quitApp)
	mux.HandleFunc("GET /settings/backup", s.backupDownload)
	mux.HandleFunc("POST /settings/restore", s.backupRestore)
	return s, guard(mux), nil
}

// version is stamped at release time: go build -ldflags "-X main.version=v1.0.0".
var version = "dev"

// page assembles the data every template gets, plus the page's own.
func money(cents int64) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	return fmt.Sprintf("%s%s.%02d", sign, dollars(int(cents/100)), cents%100)
}

func (s *Server) page(w http.ResponseWriter, name string, data map[string]any) {
	st := s.store.snapshot()
	if data == nil {
		data = map[string]any{}
	}
	data["Settings"] = st.Settings
	data["Page"] = name
	titles := map[string]string{
		"dashboard": "Dashboard", "timeline": "Timeline", "todos": "To-Dos", "medical": "Medical / VA",
		"appointments": "Appointments", "docs": "Documents", "budget": "Budget",
		"resume": "Resume", "jobs": "Jobs", "resources": "Resources", "itp": "ITP", "settings": "Settings",
		"packet": "Submit Packet", "housing": "Housing", "notes": "Notes", "skillbridge": "SkillBridge",
	}
	data["PageTitle"] = titles[name]
	// Recent Advisor exchanges feed the docked sidebar on every page.
	if n := len(st.Advisor); n > 0 {
		from := 0
		if n > 8 {
			from = n - 8
		}
		data["AdvisorRecent"] = st.Advisor[from:]
	}
	if st.Settings.RetirementDate != "" {
		if rd, err := parseDay(st.Settings.RetirementDate); err == nil {
			days := daysBetween(localDay(time.Now()), rd)
			data["DaysToR"] = days
			data["RDay"] = rd.Format("Mon, Jan 2 2006")
		}
	}
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name+".html", data); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

func pathID(r *http.Request) int {
	id, _ := strconv.Atoi(r.PathValue("id"))
	return id
}

func pathIDName(r *http.Request, name string) int {
	id, _ := strconv.Atoi(r.PathValue(name))
	return id
}

func (st *State) findTarget(id int) *ResumeTarget {
	for i := range st.ResumeTargets {
		if st.ResumeTargets[i].ID == id {
			return &st.ResumeTargets[i]
		}
	}
	return nil
}

// resolveTarget finds a resume target by numeric id or a position/company
// substring, so the Advisor can name it naturally ("the Acme resume").
func (st *State) resolveTarget(ref string) *ResumeTarget {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		if len(st.ResumeTargets) == 1 {
			return &st.ResumeTargets[0]
		}
		return nil
	}
	if id, err := strconv.Atoi(strings.TrimPrefix(ref, "#")); err == nil {
		if t := st.findTarget(id); t != nil {
			return t
		}
	}
	low := strings.ToLower(ref)
	for i := range st.ResumeTargets {
		t := &st.ResumeTargets[i]
		if strings.Contains(strings.ToLower(t.Position), low) || (t.Company != "" && strings.Contains(strings.ToLower(t.Company), low)) {
			return t
		}
	}
	return nil
}

// ---------- Dashboard -------------------------------------------------------

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	st := s.store.snapshot()
	var open []Todo
	doneCount := 0
	for _, t := range st.Todos {
		if t.Done {
			doneCount++
		} else {
			open = append(open, t)
		}
	}
	sort.Slice(open, func(i, j int) bool { return dueLess(open[i], open[j]) })
	if len(open) > 6 {
		open = open[:6]
	}
	colors := catColors(st.Todos)
	openCats := make([]catTodo, 0, len(open))
	for _, t := range open {
		openCats = append(openCats, categorize(t, colors))
	}
	now := time.Now().Format("2006-01-02T15:04")
	var appts []Appointment
	for _, a := range st.Appointments {
		if a.At >= now {
			appts = append(appts, a)
		}
	}
	sort.Slice(appts, func(i, j int) bool { return appts[i].At < appts[j].At })
	if len(appts) > 4 {
		appts = appts[:4]
	}
	in, out := monthTotals(st.Txns, time.Now())

	// By the numbers.
	total := len(st.Todos)
	tasksPct := 0
	if total > 0 {
		tasksPct = doneCount * 100 / total
	}
	var openDebt int64
	for _, d := range st.Debts {
		if !d.PaidOff {
			openDebt += d.Balance
		}
	}
	baseline := st.Settings.DebtBaseline
	if openDebt > baseline {
		baseline = openDebt
	}
	debtPct := 0
	if baseline > 0 {
		debtPct = int((baseline - openDebt) * 100 / baseline)
	}
	condReady := 0
	for _, c := range st.Conditions {
		if c.Documented && c.DBQ {
			condReady++
		}
	}
	vaPct := -1
	if len(st.Conditions) > 0 {
		vaPct = condReady * 100 / len(st.Conditions)
	}
	resumePct := len(st.Resume) * 100 / 6
	if resumePct > 100 {
		resumePct = 100
	}
	// Terminal leave: the plan item wins; fall back to R-day minus 90.
	leaveDate := ""
	for _, t := range st.Todos {
		if strings.HasPrefix(t.Title, "Terminal leave begins") {
			if t.Start != "" {
				leaveDate = t.Start
			} else {
				leaveDate = t.Due
			}
		}
	}
	if leaveDate == "" && st.Settings.RetirementDate != "" {
		if rd, err := parseDay(st.Settings.RetirementDate); err == nil {
			leaveDate = rd.AddDate(0, 0, -90).Format("2006-01-02")
		}
	}
	ptdyDate := ""
	for _, t := range st.Todos {
		if strings.HasPrefix(t.Title, "PTDY begins") {
			ptdyDate = firstNonEmpty(t.Start, t.Due)
		}
	}
	if ptdyDate == "" && st.Settings.RetirementDate != "" {
		if rd, err := parseDay(st.Settings.RetirementDate); err == nil {
			ptdyDate = rd.AddDate(0, 0, -120).Format("2006-01-02")
		}
	}
	daysToPTDY := -1
	if d, err := parseDay(ptdyDate); err == nil {
		daysToPTDY = daysBetween(localDay(time.Now()), d)
	}
	daysToLeave := -1
	if leaveDate != "" {
		if ld, err := parseDay(leaveDate); err == nil {
			daysToLeave = daysBetween(localDay(time.Now()), ld)
		}
	}

	// This week: the next seven days of timeline items and appointments,
	// budget campaign excluded.
	type weekItem struct {
		Day, Label, Title string
		Appt              bool
		Class             string
	}
	var week []weekItem
	today := time.Now().Format("2006-01-02")
	weekEnd := time.Now().AddDate(0, 0, 7).Format("2006-01-02")
	for _, t := range st.Todos {
		if t.Done || t.Due == "" || strings.HasPrefix(t.Phase, "Debt Avalanche") {
			continue
		}
		if t.Due >= today && t.Due < weekEnd {
			if d, err := parseDay(t.Due); err == nil {
				week = append(week, weekItem{Day: t.Due, Label: d.Format("Mon Jan 2"),
					Title: t.Title, Class: categorize(t, colors).Class})
			}
		}
	}
	for _, a := range st.Appointments {
		if len(a.At) >= 10 && a.At[:10] >= today && a.At[:10] < weekEnd {
			if d, err := parseDay(a.At[:10]); err == nil {
				lbl := d.Format("Mon Jan 2")
				if len(a.At) >= 16 {
					lbl += " " + a.At[11:16]
				}
				week = append(week, weekItem{Day: a.At[:10], Label: lbl, Title: a.Title, Appt: true})
			}
		}
	}
	sort.Slice(week, func(i, j int) bool { return week[i].Day < week[j].Day })

	s.page(w, "dashboard", map[string]any{
		"OpenTodos": openCats, "OpenCount": countOpen(st.Todos),
		"Appts": appts, "MonthIn": in, "MonthOut": out,
		"TasksPct": tasksPct, "TasksDone": doneCount, "TasksTotal": total,
		"DebtPct": debtPct, "OpenDebt": openDebt, "Sav": summarizeSavings(st),
		"VaPct": vaPct, "CondReady": condReady, "CondTotal": len(st.Conditions),
		"VaEst":     st.Settings.VaEstimate,
		"ResumePct": resumePct, "Prospects": len(st.Prospects),
		"DaysToLeave": daysToLeave, "Week": week, "DaysToPTDY": daysToPTDY, "SB": buildSBView(st), "Leave": buildLeaveView(st, leaveDate),
	})
}

// catColors assigns the same color index per phase that the chart uses:
// first-seen order over timeline items. Non-phase categories get fixed
// classes handled in CSS ("doc", "manual").
func catColors(todos []Todo) map[string]int {
	m := map[string]int{}
	for _, t := range todos {
		if t.Source == "timeline" && t.Phase != "" {
			if _, ok := m[t.Phase]; !ok {
				m[t.Phase] = len(m) % 7
			}
		}
	}
	return m
}

type catTodo struct {
	Todo
	Cat     string // display label
	Class   string // css suffix: 0..6 | doc | manual
	Urgency string // display label: Overdue | Upcoming | Prepare | Later
	UClass  string // css suffix: overdue | upcoming | prepare | later
}

// urgencyOf grades a to-do by how soon it is due, relative to today.
// Done items and items with no due date get no pill.
func urgencyOf(t Todo) (label, class string) {
	if t.Done || t.Due == "" {
		return "", ""
	}
	due, err := parseDay(t.Due)
	if err != nil {
		return "", ""
	}
	today := localDay(time.Now())
	days := daysBetween(today, due)
	switch {
	case days < 0:
		return "Overdue", "overdue"
	case days <= 14:
		return "Upcoming", "upcoming"
	case days <= 60:
		return "Prepare", "prepare"
	default:
		return "Later", "later"
	}
}

func categorize(t Todo, colors map[string]int) catTodo {
	c := catTodo{Todo: t}
	switch {
	case t.Phase != "":
		c.Cat = t.Phase
		if n, ok := colors[t.Phase]; ok {
			c.Class = fmt.Sprintf("%d", n)
		} else {
			c.Class = "manual"
		}
	case t.Source == "doc":
		c.Cat = "from a document"
		c.Class = "doc"
	default:
		c.Cat = "manual"
		c.Class = "manual"
	}
	c.Urgency, c.UClass = urgencyOf(t)
	return c
}

func countOpen(ts []Todo) int {
	n := 0
	for _, t := range ts {
		if !t.Done {
			n++
		}
	}
	return n
}

func dueLess(a, b Todo) bool {
	if (a.Due == "") != (b.Due == "") {
		return a.Due != "" // dated items first
	}
	if a.Due != b.Due {
		return a.Due < b.Due
	}
	return a.ID < b.ID
}

// ---------- Timeline --------------------------------------------------------

func (s *Server) timeline(w http.ResponseWriter, r *http.Request) {
	st := s.store.snapshot()
	var items []Todo
	for _, t := range st.Todos {
		if t.Source == "timeline" && !strings.HasPrefix(t.Phase, "Debt Avalanche") {
			items = append(items, t)
		}
	}
	// The Gantt: one row per dated item, a bar from start to due (or a
	// point when there is no range), on a shared time axis from the first
	// item to just past retirement. Phases get stable color indexes.
	phaseColor := map[string]int{}
	colorOf := func(phase string) int {
		if c, ok := phaseColor[phase]; ok {
			return c
		}
		c := len(phaseColor) % 7
		phaseColor[phase] = c
		return c
	}
	parse := func(d string) (time.Time, bool) {
		t, err := parseDay(d)
		return t, err == nil
	}
	type row struct {
		Title     string
		Kind      string // todo | appt
		Done      bool
		Overdue   bool
		Color     int
		Point     bool
		Left      float64 // percents
		Width     float64
		Label     string  // date range text (tooltip)
		Start     string  // formatted start (always shown)
		End       string  // formatted end (blank for single-date events)
		LabelLeft float64 // percent where the date text sits (right end of the bar)
		sortKey   string
	}
	todayFull := time.Now().Format("2006-01-02")
	var minD, maxD time.Time
	seenAny := false
	consider := func(d string) {
		if t, ok := parse(d); ok {
			if !seenAny || t.Before(minD) {
				minD = t
			}
			if !seenAny || t.After(maxD) {
				maxD = t
			}
			seenAny = true
		}
	}
	for _, t := range items {
		consider(t.Due)
		if t.Start != "" {
			consider(t.Start)
		}
	}
	for _, a := range st.Appointments {
		if len(a.At) >= 10 {
			consider(a.At[:10])
		}
	}
	consider(todayFull)
	if st.Settings.RetirementDate != "" {
		consider(st.Settings.RetirementDate)
	}
	var rows []row
	var monthTicks []map[string]any
	var todayPct, rdayPct float64 = -1, -1
	if seenAny {
		minD = minD.AddDate(0, 0, -7)
		maxD = maxD.AddDate(0, 0, 21)
		span := maxD.Sub(minD).Hours() / 24
		pct := func(t time.Time) float64 {
			return t.Sub(minD).Hours() / 24 / span * 100
		}
		for _, t := range items {
			due, ok := parse(t.Due)
			if !ok {
				continue
			}
			r := row{Title: t.Title, Kind: "todo", Done: t.Done,
				Overdue: !t.Done && t.Due < todayFull, Color: colorOf(t.Phase)}
			if start, ok2 := parse(t.Start); ok2 && start.Before(due) {
				r.Left = pct(start)
				r.Width = pct(due) - pct(start)
				r.Start = start.Format("Jan 2 2006")
				r.End = due.Format("Jan 2 2006")
				r.Label = r.Start + " to " + r.End
				r.LabelLeft = r.Left + r.Width
				r.sortKey = t.Start
			} else {
				r.Point = true
				r.Left = pct(due)
				r.Width = 0
				r.Start = due.Format("Jan 2 2006")
				r.Label = r.Start
				r.LabelLeft = r.Left
				r.sortKey = t.Due
			}
			rows = append(rows, r)
		}
		for _, a := range st.Appointments {
			if len(a.At) < 10 {
				continue
			}
			if d, ok := parse(a.At[:10]); ok {
				lbl := d.Format("Jan 2 2006")
				if len(a.At) >= 16 {
					lbl += " · " + a.At[11:16]
				}
				rows = append(rows, row{Title: a.Title, Kind: "appt", Point: true,
					Left: pct(d), LabelLeft: pct(d), Label: lbl, Start: lbl, sortKey: a.At[:10]})
			}
		}
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].sortKey < rows[j].sortKey })
		// Month gridlines on the first of each month inside the span.
		for m := time.Date(minD.Year(), minD.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0); m.Before(maxD); m = m.AddDate(0, 1, 0) {
			monthTicks = append(monthTicks, map[string]any{"Left": pct(m), "Label": m.Format("Jan '06")})
		}
		if t, ok := parse(todayFull); ok {
			todayPct = pct(t)
		}
		if st.Settings.RetirementDate != "" {
			if t, ok := parse(st.Settings.RetirementDate); ok {
				rdayPct = pct(t)
			}
		}
	}
	// Legend in first-seen phase order.
	type legendItem struct {
		Phase string
		Color int
	}
	var legend []legendItem
	seenPhase := map[string]bool{}
	for _, t := range items {
		if !seenPhase[t.Phase] {
			seenPhase[t.Phase] = true
			legend = append(legend, legendItem{t.Phase, colorOf(t.Phase)})
		}
	}

	s.page(w, "timeline", map[string]any{
		"HaveItems": len(items) > 0,
		"Rows":      rows, "Ticks": monthTicks, "Legend": legend,
		"TodayPct": todayPct, "RDayPct": rdayPct,
	})
}

// ---------- Todos -----------------------------------------------------------

func (s *Server) todos(w http.ResponseWriter, r *http.Request) {
	st := s.store.snapshot()
	items := append([]Todo(nil), st.Todos...)
	sort.Slice(items, func(i, j int) bool {
		if items[i].Done != items[j].Done {
			return !items[i].Done
		}
		return dueLess(items[i], items[j])
	})
	colors := catColors(st.Todos)
	cats := make([]catTodo, 0, len(items))
	for _, t := range items {
		cats = append(cats, categorize(t, colors))
	}
	var lanes []string
	seenLane := map[string]bool{}
	for _, t := range st.Todos {
		if t.Source == "timeline" && t.Phase != "" && !seenLane[t.Phase] {
			seenLane[t.Phase] = true
			lanes = append(lanes, t.Phase)
		}
	}
	s.page(w, "todos", map[string]any{"Items": cats, "Lanes": lanes})
}

func (s *Server) todoAdd(w http.ResponseWriter, r *http.Request) {
	title := strings.TrimSpace(r.FormValue("title"))
	if title != "" {
		lane := strings.TrimSpace(r.FormValue("phase"))
		if lane == "" {
			lane = "My Tasks"
		}
		_ = s.store.mutate(func(st *State) {
			st.Todos = append(st.Todos, Todo{
				ID: st.id(), Title: title,
				Notes:  strings.TrimSpace(r.FormValue("notes")),
				Due:    strings.TrimSpace(r.FormValue("due")),
				Start:  strings.TrimSpace(r.FormValue("start")),
				Phase:  lane,
				Source: "timeline", // lands on the chart alongside the plan
			})
		})
	}
	http.Redirect(w, r, refererOr(r, "/todos"), http.StatusSeeOther)
}

func (s *Server) todoUpdate(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	_ = s.store.mutate(func(st *State) {
		for i := range st.Todos {
			if st.Todos[i].ID == id {
				if v := strings.TrimSpace(r.FormValue("title")); v != "" {
					st.Todos[i].Title = v
				}
				st.Todos[i].Due = strings.TrimSpace(r.FormValue("due"))
				st.Todos[i].Start = strings.TrimSpace(r.FormValue("start"))
				st.Todos[i].Notes = strings.TrimSpace(r.FormValue("notes"))
				if v := strings.TrimSpace(r.FormValue("phase")); v != "" || st.Todos[i].Phase != "" {
					st.Todos[i].Phase = v
				}
			}
		}
	})
	http.Redirect(w, r, refererOr(r, "/todos"), http.StatusSeeOther)
}

func (s *Server) todoToggle(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	_ = s.store.mutate(func(st *State) {
		for i := range st.Todos {
			if st.Todos[i].ID == id {
				st.Todos[i].Done = !st.Todos[i].Done
			}
		}
	})
	http.Redirect(w, r, refererOr(r, "/todos"), http.StatusSeeOther)
}

func (s *Server) todoDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	_ = s.store.mutate(func(st *State) {
		st.Todos = deleteByID(st.Todos, id, func(t Todo) int { return t.ID })
	})
	http.Redirect(w, r, refererOr(r, "/todos"), http.StatusSeeOther)
}

func deleteByID[T any](in []T, id int, key func(T) int) []T {
	out := in[:0]
	for _, v := range in {
		if key(v) != id {
			out = append(out, v)
		}
	}
	return out
}

func refererOr(r *http.Request, fallback string) string {
	if ref := r.Header.Get("Referer"); ref != "" {
		if u, err := url.Parse(ref); err == nil && strings.HasPrefix(u.Path, "/") && !strings.HasPrefix(u.Path, "//") && !strings.Contains(u.Path, "\\") {
			return u.Path
		}
	}
	return fallback
}

// ---------- Appointments ----------------------------------------------------

func (s *Server) appointments(w http.ResponseWriter, r *http.Request) {
	st := s.store.snapshot()
	items := append([]Appointment(nil), st.Appointments...)
	sort.Slice(items, func(i, j int) bool { return items[i].At < items[j].At })
	s.page(w, "appointments", map[string]any{"Items": items})
}

func (s *Server) apptAdd(w http.ResponseWriter, r *http.Request) {
	title := strings.TrimSpace(r.FormValue("title"))
	at := strings.TrimSpace(r.FormValue("at"))
	if _, err := time.ParseInLocation("2006-01-02T15:04", at, time.Local); title != "" && err != nil {
		flash(w, "err", "Pick a date and time for the appointment.")
		http.Redirect(w, r, "/appointments", http.StatusSeeOther)
		return
	}
	if title != "" {
		flash(w, "ok", "Added "+title+".")
		_ = s.store.mutate(func(st *State) {
			st.Appointments = append(st.Appointments, Appointment{
				ID: st.id(), Title: title,
				At:    strings.TrimSpace(r.FormValue("at")),
				Place: strings.TrimSpace(r.FormValue("place")),
				Notes: strings.TrimSpace(r.FormValue("notes")),
			})
		})
	}
	http.Redirect(w, r, "/appointments", http.StatusSeeOther)
}

func (s *Server) apptDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	_ = s.store.mutate(func(st *State) {
		st.Appointments = deleteByID(st.Appointments, id, func(a Appointment) int { return a.ID })
	})
	http.Redirect(w, r, "/appointments", http.StatusSeeOther)
}

// ---------- Documents -------------------------------------------------------

func (s *Server) docs(w http.ResponseWriter, r *http.Request) {
	st := s.store.snapshot()
	items := append([]Doc(nil), st.Docs...)
	sort.Slice(items, func(i, j int) bool { return items[i].UploadedAt > items[j].UploadedAt })
	var forms []Doc
	for _, d := range items {
		if d.Kind == "form" {
			forms = append(forms, d)
		}
	}
	// Forms: unfilled first, then filled.
	sort.SliceStable(forms, func(i, j int) bool { return !forms[i].Filled && forms[j].Filled })
	s.page(w, "docs", map[string]any{"Items": items, "Forms": forms})
}

func (s *Server) docUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		http.Redirect(w, r, "/docs", http.StatusSeeOther)
		return
	}
	defer f.Close()
	name := filepath.Base(hdr.Filename)
	dst := filepath.Join(s.data, "docs", fmt.Sprintf("%d-%s", time.Now().UnixNano(), name))
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if _, err := io.Copy(out, f); err != nil {
		out.Close()
		http.Error(w, err.Error(), 500)
		return
	}
	out.Close()
	_ = s.store.mutate(func(st *State) {
		id := st.id()
		st.Docs = append(st.Docs, Doc{
			ID: id, Name: name, File: s.relDoc(dst),
			UploadedAt: time.Now().Format(time.RFC3339),
			Notes:      strings.TrimSpace(r.FormValue("notes")),
			Kind:       strings.TrimSpace(r.FormValue("kind")),
		})
		// Every document lands on the single to-do list for review.
		st.Todos = append(st.Todos, Todo{
			ID:     st.id(),
			Title:  "Review " + name + " and pull out its actions",
			Due:    time.Now().AddDate(0, 0, 7).Format("2006-01-02"),
			Notes:  "Uploaded to Documents. Add each action it demands as its own to-do.",
			Source: "doc",
		})
	})
	http.Redirect(w, r, "/docs", http.StatusSeeOther)
}

func (s *Server) docFile(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	st := s.store.snapshot()
	for _, d := range st.Docs {
		if d.ID == id {
			// An uploaded HTML or SVG file must never run as this app: only
			// PDFs and plain images display inline, and even those are sandboxed.
			switch strings.ToLower(filepath.Ext(d.File)) {
			case ".pdf", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".heic":
			default:
				w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
				w.Header().Set("Content-Disposition", `attachment; filename="`+safeFilename(d.Name)+`"`)
			}
			http.ServeFile(w, r, s.docPath(d.File))
			return
		}
	}
	http.NotFound(w, r)
}

// docBulkUpload saves many reference files at once, no per-file review to-dos.
func (s *Server) docBulkUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	kind := strings.TrimSpace(r.FormValue("kind"))
	files := r.MultipartForm.File["files"]
	saved := 0
	for _, hdr := range files {
		f, err := hdr.Open()
		if err != nil {
			continue
		}
		name := filepath.Base(hdr.Filename)
		dst := filepath.Join(s.data, "docs", fmt.Sprintf("%d-%s", time.Now().UnixNano(), name))
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			f.Close()
			continue
		}
		if _, err := io.Copy(out, f); err != nil {
			out.Close()
			f.Close()
			continue
		}
		out.Close()
		f.Close()
		_ = s.store.mutate(func(st *State) {
			st.Docs = append(st.Docs, Doc{
				ID: st.id(), Name: name, File: s.relDoc(dst),
				UploadedAt: time.Now().Format(time.RFC3339), Kind: kind,
				Notes: strings.TrimSpace(r.FormValue("notes")),
			})
		})
		saved++
	}
	http.Redirect(w, r, "/docs", http.StatusSeeOther)
}

// docRename updates a document's display name and notes.
func (s *Server) docRename(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	name := strings.TrimSpace(r.FormValue("name"))
	notes := strings.TrimSpace(r.FormValue("notes"))
	_ = s.store.mutate(func(st *State) {
		for i := range st.Docs {
			if st.Docs[i].ID == id {
				if name != "" {
					st.Docs[i].Name = name
				}
				st.Docs[i].Notes = notes
				return
			}
		}
	})
	http.Redirect(w, r, refererOr(r, "/docs"), http.StatusSeeOther)
}

// docFlagForm moves a document to the "Forms to Fill Out" shelf (or back).
func (s *Server) docFlagForm(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	_ = s.store.mutate(func(st *State) {
		for i := range st.Docs {
			if st.Docs[i].ID == id {
				if st.Docs[i].Kind == "form" {
					st.Docs[i].Kind = ""
				} else {
					st.Docs[i].Kind = "form"
				}
				return
			}
		}
	})
	http.Redirect(w, r, refererOr(r, "/docs"), http.StatusSeeOther)
}

func (s *Server) docToggleFilled(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	_ = s.store.mutate(func(st *State) {
		for i := range st.Docs {
			if st.Docs[i].ID == id {
				st.Docs[i].Filled = !st.Docs[i].Filled
				return
			}
		}
	})
	http.Redirect(w, r, refererOr(r, "/docs"), http.StatusSeeOther)
}

func (s *Server) docTodo(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		http.Redirect(w, r, "/docs", http.StatusSeeOther)
		return
	}
	st := s.store.snapshot()
	name := ""
	for _, d := range st.Docs {
		if d.ID == id {
			name = d.Name
		}
	}
	_ = s.store.mutate(func(st *State) {
		st.Todos = append(st.Todos, Todo{
			ID: st.id(), Title: title,
			Due:    strings.TrimSpace(r.FormValue("due")),
			Notes:  "From " + name,
			Source: "doc",
		})
	})
	http.Redirect(w, r, "/docs", http.StatusSeeOther)
}

var starterLinks = []Link{
	{Category: "Transition & TAP", Title: "Army TAP", URL: "https://www.armytap.army.mil"},
	{Category: "Transition & TAP", Title: "TAP Online Courses", URL: "https://tapevents.mil"},
	{Category: "Transition & TAP", Title: "DoD SkillBridge", URL: "https://dodskillbridge.com"},
	{Category: "Transition & TAP", Title: "Army Career Skills Program", URL: "https://home.army.mil/imcom/index.php/customers/career-skills-program"},
	{Category: "Transition & TAP", Title: "Soldier for Life", URL: "https://soldierforlife.army.mil"},
	{Category: "VA & Benefits", Title: "VA.gov", URL: "https://www.va.gov"},
	{Category: "VA & Benefits", Title: "eBenefits", URL: "https://www.ebenefits.va.gov"},
	{Category: "VA & Benefits", Title: "VA Disability Compensation", URL: "https://www.va.gov/disability"},
	{Category: "VA & Benefits", Title: "VA Disability Pay Rates", URL: "https://www.va.gov/disability/compensation-rates/veteran-rates"},
	{Category: "VA & Benefits", Title: "VA Home Loans", URL: "https://www.va.gov/housing-assistance/home-loans"},
	{Category: "VA & Benefits", Title: "MyArmyBenefits", URL: "https://myarmybenefits.us.army.mil"},
	{Category: "VA & Benefits", Title: "VA Solid Start", URL: "https://benefits.va.gov/transition/solid-start.asp"},
	{Category: "Health & TRICARE", Title: "TRICARE", URL: "https://www.tricare.mil"},
	{Category: "Health & TRICARE", Title: "TRICARE Overseas", URL: "https://www.tricare-overseas.com"},
	{Category: "Health & TRICARE", Title: "milConnect (DEERS)", URL: "https://milconnect.dmdc.osd.mil/milconnect"},
	{Category: "Health & TRICARE", Title: "My HealtheVet", URL: "https://www.myhealth.va.gov"},
	{Category: "Education & GI Bill", Title: "GI Bill / VA Education", URL: "https://www.va.gov/education"},
	{Category: "Education & GI Bill", Title: "Army COOL", URL: "https://www.cool.osd.mil/army"},
	{Category: "Education & GI Bill", Title: "DANTES", URL: "https://www.dantes.mil"},
	{Category: "Education & GI Bill", Title: "Student Veterans of America", URL: "https://studentveterans.org"},
	{Category: "Employment & Jobs", Title: "USAJOBS", URL: "https://www.usajobs.gov"},
	{Category: "Employment & Jobs", Title: "Feds Hire Vets", URL: "https://www.fedshirevets.gov"},
	{Category: "Employment & Jobs", Title: "CareerOneStop (DOL)", URL: "https://www.careeronestop.org"},
	{Category: "Employment & Jobs", Title: "Hiring Our Heroes", URL: "https://www.hiringourheroes.org"},
	{Category: "Employment & Jobs", Title: "RecruitMilitary", URL: "https://recruitmilitary.com"},
	{Category: "Employment & Jobs", Title: "LinkedIn", URL: "https://www.linkedin.com"},
	{Category: "Financial", Title: "DFAS", URL: "https://www.dfas.mil"},
	{Category: "Financial", Title: "Thrift Savings Plan (TSP)", URL: "https://www.tsp.gov"},
	{Category: "Financial", Title: "Military Pay Calculators", URL: "https://militarypay.defense.gov/Calculators"},
	{Category: "Financial", Title: "Social Security", URL: "https://www.ssa.gov"},
	{Category: "Records & Admin", Title: "IPPS-A", URL: "https://ipps-a.army.mil/"},
	{Category: "Records & Admin", Title: "milConnect", URL: "https://milconnect.dmdc.osd.mil/milconnect"},
}

// linkCategoryOrder is the display order for the Resources page.
var linkCategoryOrder = []string{"Transition & TAP", "VA & Benefits", "Health & TRICARE", "Education & GI Bill", "Employment & Jobs", "Financial", "Records & Admin"}

type linkGroup struct {
	Category string
	Links    []Link
}

func (s *Server) resources(w http.ResponseWriter, r *http.Request) {
	// Seed the starter retirement links once, on first visit.
	st := s.store.snapshot()
	if !st.Settings.LinksSeeded {
		_ = s.store.mutate(func(st *State) {
			if st.Settings.LinksSeeded {
				return
			}
			for _, l := range starterLinks {
				l.ID = st.id()
				st.Links = append(st.Links, l)
			}
			st.Settings.LinksSeeded = true
		})
		st = s.store.snapshot()
	}
	// Group links by category in the fixed order, with any extras last.
	seen := map[string]bool{}
	var groups []linkGroup
	add := func(cat string) {
		if seen[cat] {
			return
		}
		seen[cat] = true
		var ls []Link
		for _, l := range st.Links {
			if l.Category == cat || (cat == "Other" && !contains(linkCategoryOrder, l.Category)) {
				ls = append(ls, l)
			}
		}
		if len(ls) > 0 {
			groups = append(groups, linkGroup{Category: cat, Links: ls})
		}
	}
	for _, c := range linkCategoryOrder {
		add(c)
	}
	add("Other")
	s.page(w, "resources", map[string]any{"Groups": groups, "Contacts": st.Resources, "Categories": linkCategoryOrder})
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func (s *Server) linkAdd(w http.ResponseWriter, r *http.Request) {
	title := strings.TrimSpace(r.FormValue("title"))
	u := strings.TrimSpace(r.FormValue("url"))
	if title == "" || u == "" {
		http.Redirect(w, r, "/resources", http.StatusSeeOther)
		return
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		u = "https://" + u
	}
	cat := strings.TrimSpace(r.FormValue("category"))
	if cat == "" {
		cat = "Other"
	}
	_ = s.store.mutate(func(st *State) {
		st.Links = append(st.Links, Link{ID: st.id(), Title: title, URL: u, Category: cat, Notes: strings.TrimSpace(r.FormValue("notes"))})
	})
	http.Redirect(w, r, "/resources", http.StatusSeeOther)
}

func (s *Server) linkDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	_ = s.store.mutate(func(st *State) {
		for i, l := range st.Links {
			if l.ID == id {
				st.Links = append(st.Links[:i], st.Links[i+1:]...)
				return
			}
		}
	})
	http.Redirect(w, r, "/resources", http.StatusSeeOther)
}

func (s *Server) resourceAdd(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Redirect(w, r, "/resources", http.StatusSeeOther)
		return
	}
	_ = s.store.mutate(func(st *State) {
		st.Resources = append(st.Resources, Resource{
			ID: st.id(), Name: name,
			Org:   strings.TrimSpace(r.FormValue("org")),
			Info:  strings.TrimSpace(r.FormValue("info")),
			Notes: strings.TrimSpace(r.FormValue("notes")),
		})
	})
	http.Redirect(w, r, "/resources", http.StatusSeeOther)
}

func (s *Server) resourceDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	_ = s.store.mutate(func(st *State) {
		for i, x := range st.Resources {
			if x.ID == id {
				st.Resources = append(st.Resources[:i], st.Resources[i+1:]...)
				break
			}
		}
	})
	http.Redirect(w, r, "/resources", http.StatusSeeOther)
}

const ippsaURL = "https://ipps-a.army.mil/"

const packetGuidance = `RETIREMENT PACKET - HOW TO COMPLETE AND SUBMIT THE PAR

This cover sheet is generated by Army Retirement Workbench. The pages that follow are
the supporting documents you uploaded, combined into one file.

WHAT THE PAR IS
The Personnel Action Request (PAR) in IPPS-A is what formally starts your
voluntary retirement. Your unit S-1 initiates it from your signed DA Form 2339
(Application for Voluntary Retirement) and supporting documents; it routes through
your G-1 to HRC for approval.

TIMING
Submit the request no earlier than 24 months and no later than 12 months before
your requested retirement date (Army Directive 2026-08, 17 April 2026). Earlier
is better - orders can take months.

TWO CHECKLISTS, DO NOT CONFUSE THEM
1. The Transitions checklist (the Retirement Application Questionnaire and its
   required documents) is what you turn IN to the Transition Center to get your
   DA Form 2339 (Application for Voluntary Retirement) - the form the PAR needs.
2. The Enlisted Retirement Checklist is the PAR checklist itself - the documents
   the PAR requires once you initiate it in IPPS-A.

STEPS
1. Turn the Transitions checklist and its documents in to the Transition Center;
   get DA Form 2339 and your appointment to review and sign it.
2. Gather the PAR (Enlisted Retirement) checklist documents below and combine them here.
3. Take the signed DA Form 2339 plus the documents to your unit S-1.
4. S-1 initiates the retirement PAR in IPPS-A and routes it to G-1, then HRC.
5. Track the PAR weekly in IPPS-A until orders are published.

PAR CHECKLIST (Enlisted Retirement Checklist, as of March 2024 - verify with S-1)
- Personnel Action Request (PAR), submitted through IPPS-A
- If you are inside 12 months of the requested retirement date, ask your S-1 and
  Retirement Services Officer what a late request needs
- Waivers, if applicable (memorandum format)
- DEROS, if the requested retirement date is before your DEROS date
- ADSO (SFC and above is 3 years; there is no waiver for the 9/11 GI Bill)
- Exception to Policy request (in lieu of PCS if the retirement request is not
  submitted within 30 days of RFO / official assignment notification)
- DA Form 2339, Application for Voluntary Retirement (obtained from the Transition Center)
    - Soldier signs blocks 19 and 30 on the second page
    - Transition Center specialist verifies by signing block 31 on the second page
    - Transition Center specialist verification memorandum
- Current Soldier Talent Profile (STP), dated within 14 days
- Sexual Assault Statement (memorandum format; mandatory for all separation and
  retirement actions)
- Additional supporting documents (exception to policy, withdrawals, or a
  date-change retirement request)

* Consolidate all documents into a single PDF.
* Enter the requested retirement date as the Effective Date of the PAR.

This is planning guidance, not official policy. Confirm every step and deadline
with your Transition Center and unit S-1.`

func (s *Server) packet(w http.ResponseWriter, r *http.Request) {
	st := s.store.snapshot()
	var files []Doc
	for _, d := range st.Docs {
		if d.Kind == "packet" {
			files = append(files, d)
		}
	}
	s.page(w, "packet", map[string]any{
		"Files": files, "IppsaURL": ippsaURL, "PAR": buildPARView(st),
		"Err": r.URL.Query().Get("err"),
	})
}

func (s *Server) packetUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		http.Redirect(w, r, "/packet", http.StatusSeeOther)
		return
	}
	defer f.Close()
	name := filepath.Base(hdr.Filename)
	dst := filepath.Join(s.data, "docs", fmt.Sprintf("%d-%s", time.Now().UnixNano(), name))
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if _, err := io.Copy(out, f); err != nil {
		out.Close()
		http.Error(w, err.Error(), 500)
		return
	}
	out.Close()
	_ = s.store.mutate(func(st *State) {
		st.Docs = append(st.Docs, Doc{
			ID: st.id(), Name: name, File: s.relDoc(dst),
			UploadedAt: time.Now().Format(time.RFC3339),
			Notes:      strings.TrimSpace(r.FormValue("notes")), Kind: "packet",
		})
	})
	http.Redirect(w, r, "/packet", http.StatusSeeOther)
}

func (s *Server) packetFiles(st *State) []string {
	var paths []string
	for _, d := range st.Docs {
		if d.Kind == "packet" {
			paths = append(paths, s.docPath(d.File))
		}
	}
	return paths
}

//go:embed tools/pdfmerge.swift
var pdfmergeSwift []byte

// mergePDF combines the given files (and an optional cover text) into one PDF
// and returns the bytes. On macOS it runs the bundled Swift helper (PDFKit
// handles PDFs, images, and the cover page). Elsewhere it falls back to
// pdfunite from poppler-utils, which merges PDFs only.
func (s *Server) mergePDF(coverText string, inputs []string) ([]byte, error) {
	if len(inputs) == 0 {
		return nil, fmt.Errorf("upload at least one file first")
	}
	work, err := os.MkdirTemp("", "rw-packet-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	outPath := filepath.Join(work, "packet.pdf")

	var name string
	var args []string
	if swiftBin, err := exec.LookPath("swift"); err == nil {
		helper := filepath.Join(work, "pdfmerge.swift")
		if err := os.WriteFile(helper, pdfmergeSwift, 0o600); err != nil {
			return nil, err
		}
		name, args = swiftBin, []string{helper, outPath}
		if coverText != "" {
			cf := filepath.Join(work, "cover.txt")
			if os.WriteFile(cf, []byte(coverText), 0o600) == nil {
				args = append(args, "--cover", cf)
			}
		}
		args = append(args, inputs...)
	} else if unite, err := exec.LookPath("pdfunite"); err == nil {
		for _, in := range inputs {
			if !strings.EqualFold(filepath.Ext(in), ".pdf") {
				return nil, fmt.Errorf("without macOS, only PDF files can be combined; convert %s to PDF first", filepath.Base(in))
			}
		}
		name, args = unite, append(append([]string{}, inputs...), outPath)
	} else {
		return nil, fmt.Errorf("combining PDFs needs macOS (run xcode-select --install) or pdfunite (install poppler-utils)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	ownProcessGroup(cmd) // a timeout kills the compiled helper too, not just the swift driver
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("merge failed: %s", firstN(errb.String(), 200))
	}
	return os.ReadFile(outPath)
}

func (s *Server) servePacketPDF(w http.ResponseWriter, r *http.Request, cover, filename string) {
	st := s.store.snapshot()
	pdf, err := s.mergePDF(cover, s.packetFiles(&st))
	if err != nil {
		http.Redirect(w, r, "/packet?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	_, _ = w.Write(pdf)
}

func (s *Server) packetSupporting(w http.ResponseWriter, r *http.Request) {
	s.servePacketPDF(w, r, "", "supporting-document.pdf")
}

func (s *Server) packetFull(w http.ResponseWriter, r *http.Request) {
	s.servePacketPDF(w, r, packetGuidance, "retirement-packet.pdf")
}

func (s *Server) packetDelete(w http.ResponseWriter, r *http.Request) {
	s.removeDoc(pathID(r), "packet")
	http.Redirect(w, r, "/packet", http.StatusSeeOther)
}

// removeDoc deletes a document record and then its file. kind "" matches any.
func (s *Server) removeDoc(id int, kind string) {
	var file string
	err := s.store.mutate(func(st *State) {
		for i, d := range st.Docs {
			if d.ID == id && (kind == "" || d.Kind == kind) {
				file = d.File
				st.Docs = append(st.Docs[:i], st.Docs[i+1:]...)
				return
			}
		}
	})
	if err == nil && file != "" {
		_ = os.Remove(s.docPath(file))
	}
}

func (s *Server) docDelete(w http.ResponseWriter, r *http.Request) {
	s.removeDoc(pathID(r), "")
	http.Redirect(w, r, refererOr(r, "/docs"), http.StatusSeeOther)
}

// packetMove swaps a packet file with its neighbor packet file (order = merge order).
func (s *Server) packetMove(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	up := strings.HasSuffix(r.URL.Path, "/up")
	_ = s.store.mutate(func(st *State) {
		idx := []int{}
		for i, d := range st.Docs {
			if d.Kind == "packet" {
				idx = append(idx, i)
			}
		}
		for k, i := range idx {
			if st.Docs[i].ID == id {
				var j int
				if up {
					if k == 0 {
						return
					}
					j = idx[k-1]
				} else {
					if k == len(idx)-1 {
						return
					}
					j = idx[k+1]
				}
				st.Docs[i], st.Docs[j] = st.Docs[j], st.Docs[i]
				return
			}
		}
	})
	http.Redirect(w, r, "/packet", http.StatusSeeOther)
}

// ---------- Advisor ---------------------------------------------------------

// advisorDigest summarizes live app state so answers rest on real numbers.
// The #numbers are item IDs the tools accept.
func advisorDigest(st *State) string {
	var b strings.Builder
	now := time.Now()
	fmt.Fprintf(&b, "Today: %s\n", now.Format("2006-01-02"))
	fmt.Fprintf(&b, "Soldier: %s, %s, retires %s. Working VA estimate: %d%%\n",
		st.Settings.Name, st.Settings.Branch, st.Settings.RetirementDate, st.Settings.VaEstimate)
	var bills int64
	for _, x := range st.Bills {
		bills += x.Amount
	}
	fmt.Fprintf(&b, "Money: income %s/mo, bills %s/mo, debt-free goal %s\n", money(st.Settings.MonthlyIncome), money(bills), st.Settings.DebtFreeBy)
	b.WriteString("Bills:\n")
	for _, x := range st.Bills {
		fmt.Fprintf(&b, "- #%d %s %s\n", x.ID, x.Name, money(x.Amount))
	}
	b.WriteString("Debts (open):\n")
	for _, d := range st.Debts {
		if !d.PaidOff {
			fmt.Fprintf(&b, "- #%d %s apr %s balance %s min %s\n", d.ID, d.Name, d.APR, money(d.Balance), money(d.Min))
		}
	}
	b.WriteString(savingsDigest(*st))
	b.WriteString(leaveDigest(*st))
	if rp := estimateRetirePay(*st); rp.Set {
		fmt.Fprintf(&b, "Retired pay estimate (%s, %s years): gross %s, SBP %s, VA %s, total %s/mo\n", rp.System, rp.Years, money(rp.Gross), money(rp.SBP), money(rp.VA), money(rp.Total))
	}
	b.WriteString("\nVA claim conditions (doc = in the record, dbq = criteria studied):\n")
	for _, c := range st.Conditions {
		fmt.Fprintf(&b, "- #%d %s [doc:%t dbq:%t] %s\n", c.ID, c.Name, c.Documented, c.DBQ, c.Notes)
	}
	if len(st.Symptoms) > 0 {
		b.WriteString("\nRecent symptom log:\n")
		from := 0
		if len(st.Symptoms) > 5 {
			from = len(st.Symptoms) - 5
		}
		for _, x := range st.Symptoms[from:] {
			fmt.Fprintf(&b, "- #%d %s %s: %s\n", x.ID, x.Date, x.Condition, x.Note)
		}
	}
	b.WriteString("\nUpcoming appointments:\n")
	for _, a := range st.Appointments {
		if a.At >= now.Format("2006-01-02") {
			fmt.Fprintf(&b, "- #%d %s %s (%s) %s\n", a.ID, a.At, a.Title, a.Place, a.Notes)
		}
	}
	b.WriteString("\nOpen tasks due in the next 90 days (earliest 25):\n")
	horizon := now.AddDate(0, 0, 90).Format("2006-01-02")
	n := 0
	for _, t := range st.Todos {
		if !t.Done && t.Due != "" && t.Due <= horizon && n < 25 {
			fmt.Fprintf(&b, "- #%d %s [%s] %s\n", t.ID, t.Due, t.Phase, t.Title)
			n++
		}
	}
	if len(st.Prospects) > 0 {
		b.WriteString("\nJob prospects:\n")
		for _, p := range st.Prospects {
			fmt.Fprintf(&b, "- #%d %s (%s) status %s\n", p.ID, p.Title, p.Org, p.Status)
		}
	}
	fmt.Fprintf(&b, "\nDocuments on file: %d total. Key items: ", len(st.Docs))
	shown := 0
	for _, d := range st.Docs {
		name := strings.ToLower(d.Name)
		if strings.Contains(name, "strategy") || strings.Contains(name, "referral") ||
			strings.HasPrefix(name, "dbq") || strings.Contains(name, "mri") ||
			strings.Contains(name, "pdha") || strings.Contains(name, "cfr") {
			if shown > 0 {
				b.WriteString("; ")
			}
			b.WriteString(d.Name)
			shown++
		}
		if shown >= 22 {
			b.WriteString("; and more")
			break
		}
	}
	if len(st.ResumeTargets) > 0 {
		b.WriteString("\nResume targets (one tailored resume per position):\n")
		for _, t := range st.ResumeTargets {
			fmt.Fprintf(&b, "- #%d %s", t.ID, t.Position)
			if t.Company != "" {
				fmt.Fprintf(&b, " @ %s", t.Company)
			}
			fmt.Fprintf(&b, " (%d sections)", len(t.Sections))
			if t.Requirements != "" {
				fmt.Fprintf(&b, "; job requirements: %s", firstN(t.Requirements, 400))
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func toolStr(in map[string]any, k string) string {
	if v, ok := in[k].(string); ok {
		return strings.TrimSpace(v)
	}
	if v, ok := in[k].(float64); ok {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

func toolID(in map[string]any, k string) int {
	switch v := in[k].(type) {
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(v), "#"))
		return n
	}
	return 0
}

func obj(kv ...any) map[string]any {
	o := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		o[kv[i].(string)] = kv[i+1]
	}
	return o
}

func prop(t, desc string) map[string]any { return obj("type", t, "description", desc) }

func tool(name, desc string, req []string, props map[string]any) map[string]any {
	return obj("name", name, "description", desc,
		"input_schema", obj("type", "object", "properties", props, "required", req))
}

func firstN(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

func advisorTools() []map[string]any {
	date := prop("string", "YYYY-MM-DD")
	return append([]map[string]any{
		tool("add_task", "Add a task; it lands on the chart and the to-do list.", []string{"title", "due"},
			map[string]any{"title": prop("string", "task title"), "due": date, "start": date,
				"lane": prop("string", "chart lane, e.g. 'Medical / VA Claim'; empty = My Tasks"), "notes": prop("string", "")}),
		tool("update_task", "Update fields on a task by #id; empty fields keep their value.", []string{"id"},
			map[string]any{"id": prop("integer", "task id"), "title": prop("string", ""), "due": date, "start": date,
				"lane": prop("string", ""), "notes": prop("string", ""), "done": prop("boolean", "check or uncheck")}),
		tool("delete_task", "Delete a task by #id.", []string{"id"}, map[string]any{"id": prop("integer", "")}),
		tool("add_appointment", "Add an appointment.", []string{"title", "at"},
			map[string]any{"title": prop("string", ""), "at": prop("string", "YYYY-MM-DDTHH:MM"),
				"place": prop("string", ""), "notes": prop("string", "")}),
		tool("delete_appointment", "Delete an appointment by #id.", []string{"id"}, map[string]any{"id": prop("integer", "")}),
		tool("add_bill", "Add a monthly bill.", []string{"name", "amount"},
			map[string]any{"name": prop("string", ""), "amount": prop("string", "dollars, e.g. 84.99")}),
		tool("update_bill", "Set a bill's monthly amount by #id.", []string{"id", "amount"},
			map[string]any{"id": prop("integer", ""), "amount": prop("string", "dollars")}),
		tool("delete_bill", "Delete a bill by #id.", []string{"id"}, map[string]any{"id": prop("integer", "")}),
		tool("add_debt", "Add a debt account to the avalanche.", []string{"name"},
			map[string]any{"name": prop("string", ""), "apr": prop("string", "e.g. 19.99%"),
				"balance": prop("string", "dollars"), "min": prop("string", "dollars")}),
		tool("update_debt", "Update a debt's apr, balance, or minimum by #id.", []string{"id"},
			map[string]any{"id": prop("integer", ""), "apr": prop("string", ""),
				"balance": prop("string", "dollars"), "min": prop("string", "dollars")}),
		tool("mark_debt_paid", "Mark a debt paid off (or reopen it).", []string{"id"},
			map[string]any{"id": prop("integer", ""), "paid": prop("boolean", "default true")}),
		tool("add_condition", "Add a VA claim condition.", []string{"name"},
			map[string]any{"name": prop("string", ""), "notes": prop("string", "")}),
		tool("set_condition", "Set the documented / dbq flags on a condition by #id.", []string{"id"},
			map[string]any{"id": prop("integer", ""), "documented": prop("boolean", ""), "dbq": prop("boolean", "")}),
		tool("log_symptom", "Append to the symptom log.", []string{"date", "condition", "note"},
			map[string]any{"date": date, "condition": prop("string", ""), "note": prop("string", "")}),
		tool("add_prospect", "Track a job opening.", []string{"title"},
			map[string]any{"title": prop("string", ""), "org": prop("string", ""), "url": prop("string", ""), "notes": prop("string", "")}),
		tool("advance_prospect", "Advance a prospect found->applied->interview->offer.", []string{"id"},
			map[string]any{"id": prop("integer", "")}),
		tool("add_contact", "Add a job-search contact.", []string{"name"},
			map[string]any{"name": prop("string", ""), "org": prop("string", ""), "role": prop("string", ""),
				"info": prop("string", "phone/email"), "notes": prop("string", "")}),
		tool("add_resource", "Add a Who to Call resource.", []string{"name"},
			map[string]any{"name": prop("string", ""), "org": prop("string", ""), "info": prop("string", ""), "notes": prop("string", "")}),
		tool("create_resume_target", "Start a new tailored resume for a specific position. Capture the job requirements so later bullets can be aimed at them.", []string{"position"},
			map[string]any{"position": prop("string", "role title, e.g. 'Platform Engineer'"),
				"company":      prop("string", "employer, e.g. 'Acme Defense'"),
				"requirements": prop("string", "the job posting requirements or what the recruiter said")}),
		tool("set_resume_target", "Update a resume target's headline and captured job requirements.", []string{"target"},
			map[string]any{"target": prop("string", "target id or position/company name"),
				"headline":     prop("string", "tailored headline for this position"),
				"requirements": prop("string", "the job requirements to tailor toward")}),
		tool("add_resume_section", "Add a section to a specific resume target. Tailor the bullets to that target's captured job requirements.", []string{"target", "heading"},
			map[string]any{"target": prop("string", "target id or position/company name"),
				"heading": prop("string", "e.g. 'Senior Logistics NCO · 2003-2027'"),
				"bullets": prop("string", "achievement bullets, one per line")}),
		tool("update_resume_section", "Rewrite a section (by heading match or id) within a resume target.", []string{"target", "heading"},
			map[string]any{"target": prop("string", "target id or position/company name"),
				"heading":    prop("string", "the section heading to rewrite (or a new heading)"),
				"section_id": prop("integer", "section id if known"),
				"bullets":    prop("string", "the full replacement bullets, one per line")}),
		tool("set_resume_header", "Set the shared resume name and contact line (applies to every target).", []string{},
			map[string]any{"name": prop("string", ""), "contact": prop("string", "email · phone · city")}),
		tool("add_med", "Add a medication to the tracker.", []string{"name"},
			map[string]any{"name": prop("string", ""), "dose": prop("string", ""), "for": prop("string", "what it treats"),
				"prescriber": prop("string", ""), "notes": prop("string", "refill status, pharmacy, etc.")}),
		tool("set_va_estimate", "Set the Potential VA Rating box.", []string{"percent"},
			map[string]any{"percent": prop("integer", "0-100")}),
	}, append(savingsTools(), leaveTool())...)
}

func (s *Server) applyTool(name string, in map[string]any) string {
	res := ""
	err := s.store.mutate(func(st *State) {
		if out, ok := toolSavings(st, name, in); ok {
			res = out
			return
		}
		if name == "set_leave_balance" {
			res = toolLeave(st, in)
			return
		}
		switch name {
		case "add_task":
			t := Todo{ID: st.id(), Title: toolStr(in, "title"), Due: toolStr(in, "due"),
				Start: toolStr(in, "start"), Notes: toolStr(in, "notes"),
				Source: "timeline", Phase: toolStr(in, "lane")}
			if t.Title == "" || t.Due == "" {
				res = "error: title and due are required"
				return
			}
			if t.Phase == "" {
				t.Phase = "My Tasks"
			}
			st.Todos = append(st.Todos, t)
			res = fmt.Sprintf("added task #%d %q due %s in lane %q", t.ID, t.Title, t.Due, t.Phase)
		case "update_task":
			id := toolID(in, "id")
			for i := range st.Todos {
				if st.Todos[i].ID == id {
					t := &st.Todos[i]
					if v := toolStr(in, "title"); v != "" {
						t.Title = v
					}
					if v := toolStr(in, "due"); v != "" {
						t.Due = v
					}
					if v := toolStr(in, "start"); v != "" {
						t.Start = v
					}
					if v := toolStr(in, "lane"); v != "" {
						t.Phase = v
					}
					if v := toolStr(in, "notes"); v != "" {
						t.Notes = v
					}
					if v, ok := in["done"].(bool); ok {
						t.Done = v
					}
					res = fmt.Sprintf("updated task #%d %q (due %s, done %t)", t.ID, t.Title, t.Due, t.Done)
					return
				}
			}
			res = fmt.Sprintf("error: task #%d not found", id)
		case "delete_task":
			id := toolID(in, "id")
			for i, t := range st.Todos {
				if t.ID == id {
					st.Todos = append(st.Todos[:i], st.Todos[i+1:]...)
					res = fmt.Sprintf("deleted task #%d %q", id, t.Title)
					return
				}
			}
			res = fmt.Sprintf("error: task #%d not found", id)
		case "add_appointment":
			a := Appointment{ID: st.id(), Title: toolStr(in, "title"), At: toolStr(in, "at"),
				Place: toolStr(in, "place"), Notes: toolStr(in, "notes")}
			if a.Title == "" || a.At == "" {
				res = "error: title and at are required"
				return
			}
			st.Appointments = append(st.Appointments, a)
			sort.Slice(st.Appointments, func(i, j int) bool { return st.Appointments[i].At < st.Appointments[j].At })
			res = fmt.Sprintf("added appointment #%d %q at %s (%s)", a.ID, a.Title, a.At, a.Place)
		case "delete_appointment":
			id := toolID(in, "id")
			for i, a := range st.Appointments {
				if a.ID == id {
					st.Appointments = append(st.Appointments[:i], st.Appointments[i+1:]...)
					res = fmt.Sprintf("deleted appointment #%d %q", id, a.Title)
					return
				}
			}
			res = fmt.Sprintf("error: appointment #%d not found", id)
		case "add_bill":
			amt, e := parseMoney(toolStr(in, "amount"))
			if e != nil || toolStr(in, "name") == "" {
				res = "error: name and a dollar amount are required"
				return
			}
			b := Bill{ID: st.id(), Name: toolStr(in, "name"), Amount: amt}
			st.Bills = append(st.Bills, b)
			res = fmt.Sprintf("added bill #%d %s %s/mo", b.ID, b.Name, money(amt))
		case "update_bill":
			id := toolID(in, "id")
			amt, e := parseMoney(toolStr(in, "amount"))
			if e != nil {
				res = "error: bad amount"
				return
			}
			for i := range st.Bills {
				if st.Bills[i].ID == id {
					st.Bills[i].Amount = amt
					res = fmt.Sprintf("bill #%d %s set to %s/mo", id, st.Bills[i].Name, money(amt))
					return
				}
			}
			res = fmt.Sprintf("error: bill #%d not found", id)
		case "delete_bill":
			id := toolID(in, "id")
			for i, b := range st.Bills {
				if b.ID == id {
					st.Bills = append(st.Bills[:i], st.Bills[i+1:]...)
					res = fmt.Sprintf("deleted bill #%d %s", id, b.Name)
					return
				}
			}
			res = fmt.Sprintf("error: bill #%d not found", id)
		case "add_debt":
			if toolStr(in, "name") == "" {
				res = "error: name required"
				return
			}
			maxp := 0
			for _, d := range st.Debts {
				if d.Priority > maxp {
					maxp = d.Priority
				}
			}
			d := Debt{ID: st.id(), Name: toolStr(in, "name"), APR: toolStr(in, "apr"), Priority: maxp + 1}
			if v, e := parseMoney(toolStr(in, "balance")); e == nil {
				d.Balance = v
			}
			if v, e := parseMoney(toolStr(in, "min")); e == nil {
				d.Min = v
			}
			if d.APR == "" {
				d.APR = "TBD"
			}
			st.Debts = append(st.Debts, d)
			bumpBaseline(st)
			res = fmt.Sprintf("added debt #%d %s balance %s min %s", d.ID, d.Name, money(d.Balance), money(d.Min))
		case "update_debt":
			id := toolID(in, "id")
			for i := range st.Debts {
				if st.Debts[i].ID == id {
					d := &st.Debts[i]
					if v := toolStr(in, "apr"); v != "" {
						d.APR = v
					}
					if toolStr(in, "balance") != "" {
						if v, e := parseMoney(toolStr(in, "balance")); e == nil {
							d.Balance = v
						}
					}
					if toolStr(in, "min") != "" {
						if v, e := parseMoney(toolStr(in, "min")); e == nil {
							d.Min = v
						}
					}
					bumpBaseline(st)
					res = fmt.Sprintf("updated debt #%d %s: apr %s balance %s min %s", d.ID, d.Name, d.APR, money(d.Balance), money(d.Min))
					return
				}
			}
			res = fmt.Sprintf("error: debt #%d not found", id)
		case "mark_debt_paid":
			id := toolID(in, "id")
			paid := true
			if v, ok := in["paid"].(bool); ok {
				paid = v
			}
			for i := range st.Debts {
				if st.Debts[i].ID == id {
					st.Debts[i].PaidOff = paid
					res = fmt.Sprintf("debt #%d %s marked paid=%t", id, st.Debts[i].Name, paid)
					return
				}
			}
			res = fmt.Sprintf("error: debt #%d not found", id)
		case "add_condition":
			if toolStr(in, "name") == "" {
				res = "error: name required"
				return
			}
			c := Condition{ID: st.id(), Name: toolStr(in, "name"), Notes: toolStr(in, "notes")}
			st.Conditions = append(st.Conditions, c)
			res = fmt.Sprintf("added condition #%d %s", c.ID, c.Name)
		case "set_condition":
			id := toolID(in, "id")
			for i := range st.Conditions {
				if st.Conditions[i].ID == id {
					if v, ok := in["documented"].(bool); ok {
						st.Conditions[i].Documented = v
					}
					if v, ok := in["dbq"].(bool); ok {
						st.Conditions[i].DBQ = v
					}
					res = fmt.Sprintf("condition #%d %s: doc=%t dbq=%t", id, st.Conditions[i].Name,
						st.Conditions[i].Documented, st.Conditions[i].DBQ)
					return
				}
			}
			res = fmt.Sprintf("error: condition #%d not found", id)
		case "log_symptom":
			x := Symptom{ID: st.id(), Date: toolStr(in, "date"), Condition: toolStr(in, "condition"), Note: toolStr(in, "note")}
			if x.Date == "" || x.Condition == "" || x.Note == "" {
				res = "error: date, condition, and note are all required"
				return
			}
			st.Symptoms = append(st.Symptoms, x)
			res = fmt.Sprintf("logged symptom #%d %s %s", x.ID, x.Date, x.Condition)
		case "add_prospect":
			if toolStr(in, "title") == "" {
				res = "error: title required"
				return
			}
			p := Prospect{ID: st.id(), Title: toolStr(in, "title"), Org: toolStr(in, "org"),
				URL: toolStr(in, "url"), Notes: toolStr(in, "notes"), Status: "found",
				Added: time.Now().Format("2006-01-02")}
			st.Prospects = append(st.Prospects, p)
			res = fmt.Sprintf("tracking prospect #%d %s (%s)", p.ID, p.Title, p.Org)
		case "advance_prospect":
			id := toolID(in, "id")
			for i := range st.Prospects {
				if st.Prospects[i].ID == id {
					for k, s2 := range prospectFlow {
						if st.Prospects[i].Status == s2 && k < len(prospectFlow)-1 {
							st.Prospects[i].Status = prospectFlow[k+1]
							break
						}
					}
					res = fmt.Sprintf("prospect #%d now %s", id, st.Prospects[i].Status)
					return
				}
			}
			res = fmt.Sprintf("error: prospect #%d not found", id)
		case "add_contact":
			if toolStr(in, "name") == "" {
				res = "error: name required"
				return
			}
			c := Contact{ID: st.id(), Name: toolStr(in, "name"), Org: toolStr(in, "org"),
				Role: toolStr(in, "role"), Info: toolStr(in, "info"), Notes: toolStr(in, "notes")}
			st.Contacts = append(st.Contacts, c)
			res = fmt.Sprintf("added contact #%d %s", c.ID, c.Name)
		case "add_resource":
			if toolStr(in, "name") == "" {
				res = "error: name required"
				return
			}
			x := Resource{ID: st.id(), Name: toolStr(in, "name"), Org: toolStr(in, "org"),
				Info: toolStr(in, "info"), Notes: toolStr(in, "notes")}
			st.Resources = append(st.Resources, x)
			res = fmt.Sprintf("added resource #%d %s", x.ID, x.Name)
		case "create_resume_target":
			pos := toolStr(in, "position")
			if pos == "" {
				res = "error: position required"
				return
			}
			t := ResumeTarget{ID: st.id(), Position: pos, Company: toolStr(in, "company"), Requirements: toolStr(in, "requirements")}
			st.ResumeTargets = append(st.ResumeTargets, t)
			res = fmt.Sprintf("created resume target #%d for %s", t.ID, pos)
		case "set_resume_target":
			t := st.resolveTarget(toolStr(in, "target"))
			if t == nil {
				res = "error: no matching resume target"
				return
			}
			if v := toolStr(in, "headline"); v != "" {
				t.Headline = v
			}
			if v := toolStr(in, "requirements"); v != "" {
				t.Requirements = v
			}
			res = fmt.Sprintf("updated resume target %q", t.Position)
		case "add_resume_section":
			t := st.resolveTarget(toolStr(in, "target"))
			if t == nil {
				res = "error: name the resume target (position or company)"
				return
			}
			h := toolStr(in, "heading")
			if h == "" {
				res = "error: heading required"
				return
			}
			sec := ResumeSection{ID: st.id(), Heading: h, Bullets: parseBullets(toolStr(in, "bullets"))}
			t.Sections = append(t.Sections, sec)
			res = fmt.Sprintf("added section %q (%d bullets) to the %s resume", h, len(sec.Bullets), t.Position)
		case "update_resume_section":
			t := st.resolveTarget(toolStr(in, "target"))
			if t == nil {
				res = "error: name the resume target"
				return
			}
			sid := toolID(in, "section_id")
			h := toolStr(in, "heading")
			for i := range t.Sections {
				if (sid != 0 && t.Sections[i].ID == sid) || (sid == 0 && strings.EqualFold(t.Sections[i].Heading, h)) {
					if h != "" {
						t.Sections[i].Heading = h
					}
					if v := toolStr(in, "bullets"); v != "" {
						t.Sections[i].Bullets = parseBullets(v)
					}
					res = fmt.Sprintf("rewrote section %q in the %s resume", t.Sections[i].Heading, t.Position)
					return
				}
			}
			res = "error: section not found in that target"
		case "set_resume_header":
			if v := toolStr(in, "name"); v != "" {
				st.Settings.Name = v
			}
			st.Settings.ResumeContact = toolStr(in, "contact")
			res = "updated the resume header"
		case "add_med":
			if toolStr(in, "name") == "" {
				res = "error: name required"
				return
			}
			md := Med{ID: st.id(), Name: toolStr(in, "name"), Dose: toolStr(in, "dose"),
				For: toolStr(in, "for"), Prescriber: toolStr(in, "prescriber"), Notes: toolStr(in, "notes")}
			st.Meds = append(st.Meds, md)
			res = fmt.Sprintf("added medication #%d %s", md.ID, md.Name)
		case "set_va_estimate":
			v := toolID(in, "percent")
			if v < 0 || v > 100 {
				res = "error: percent must be 0-100"
				return
			}
			st.Settings.VaEstimate = v
			res = fmt.Sprintf("Potential VA Rating set to %d%%", v)
		default:
			res = "error: unknown tool " + name
		}
	})
	if err != nil {
		return "error: " + err.Error()
	}
	return res
}

func (s *Server) advisorAsk(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.FormValue("q"))
	st := s.store.snapshot()
	ready := st.aiReady()
	// The docked panel on every page posts panel=1 and wants JSON back;
	// the full Advisor page posts a plain form and wants a redirect.
	panel := r.FormValue("panel") == "1"
	fail := func(msg string) {
		if panel {
			w.Header().Set("content-type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
			return
		}
		http.Error(w, msg, http.StatusBadRequest)
	}
	done := func(answer string, changed bool) {
		if panel {
			w.Header().Set("content-type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"answer": answer, "changed": changed})
			return
		}
		http.Redirect(w, r, refererOr(r, "/"), http.StatusSeeOther)
	}
	if q == "" || !ready {
		fail("Ask a question, and choose Claude or ChatGPT for the Advisor in Settings.")
		return
	}
	sys := "You are the advisor inside ARW (Army Retirement Workbench), a private local app run by one soldier retiring from the U.S. Army. " +
		"The app state below is live and true; treat it as the record of what exists in the app, not of what exists in official systems. " +
		"Answer plainly in short paragraphs or simple numbered lists; no markdown symbols and no em dashes. Be concrete and use the soldier's real numbers and dates. " +
		"On VA disability topics, apply 38 CFR Part 4 concepts carefully, never invent record contents, label estimates as estimates, and recommend a VSO for filing decisions. " +
		"You can CHANGE the app with your tools when asked: add or edit tasks, appointments, bills, debts, conditions, symptoms, prospects, contacts, resources, resume targets (one tailored resume per position) and their sections, and the VA estimate. When asked to help with the resume, write strong, quantified, civilian-readable achievement bullets (no unexplained military jargon). Item IDs are the #numbers in the app state. Confirm each change in one short line. Never guess a required field like a date or dollar amount; if it is missing, ask instead of inventing it.\n\n=== APP STATE ===\n" + advisorDigest(&st)
	var p strings.Builder
	p.WriteString(sys)
	p.WriteString("\n\n=== TOOLS YOU CAN REQUEST ===\n" + toolCatalog())
	p.WriteString("\n=== CONVERSATION SO FAR ===\n")
	from := 0
	if len(st.Advisor) > 4 {
		from = len(st.Advisor) - 4
	}
	for _, e := range st.Advisor[from:] {
		fmt.Fprintf(&p, "soldier: %s\n\nadvisor: %s\n\n", e.Q, e.A)
	}
	p.WriteString("=== QUESTION ===\n" + q + "\n\n" + advisorReplyFormat)
	text, err := askAI(r.Context(), st.Settings.AdvisorProvider, p.String(), "")
	if err != nil {
		fail(err.Error())
		return
	}
	answer, acts := parseAdvisorReply(text)
	var actions []string
	askedToDelete := strings.Contains(strings.ToLower(q), "delete") || strings.Contains(strings.ToLower(q), "remove")
	for n, a := range acts {
		if n == 12 {
			break // one question never rewrites the whole app
		}
		// Deletes need your words, not the model's: text inside a document or
		// job posting must never be able to talk the Advisor into erasing data.
		if strings.HasPrefix(a.Tool, "delete_") && !askedToDelete {
			actions = append(actions, "skipped "+a.Tool+": ask me to delete it in your own words")
			continue
		}
		actions = append(actions, s.applyTool(a.Tool, a.Input))
	}
	if answer == "" {
		answer = "Done."
	}
	if len(actions) > 0 {
		answer += "\n\nChanges made:\n• " + strings.Join(actions, "\n• ")
	}
	_ = s.store.mutate(func(st *State) {
		st.Advisor = append(st.Advisor, AdvisorEntry{Q: q, A: answer, At: time.Now().Format("2006-01-02 15:04")})
		if len(st.Advisor) > 50 {
			st.Advisor = st.Advisor[len(st.Advisor)-50:]
		}
	})
	done(answer, len(actions) > 0)
}

const advisorReplyFormat = `Reply with ONLY one JSON object and nothing before or after it:
{"answer": "your reply to the soldier, plain text", "actions": [{"tool": "tool_name", "input": {"field": "value"}}]}
Use an empty actions list when nothing in the app should change. Request a change only when the soldier asked for it. In the answer, confirm each change in one short line.`

// ---------- Medical / VA ----------------------------------------------------

func (s *Server) medical(w http.ResponseWriter, r *http.Request) {
	st := s.store.snapshot()
	var records, dbqs []Doc
	for _, d := range st.Docs {
		switch d.Kind {
		case "medical":
			records = append(records, d)
		case "dbq":
			dbqs = append(dbqs, d)
		}
	}
	symptoms := append([]Symptom(nil), st.Symptoms...)
	sort.Slice(symptoms, func(i, j int) bool { return symptoms[i].Date > symptoms[j].Date })
	if len(symptoms) > 40 {
		symptoms = symptoms[:40]
	}
	s.page(w, "medical", map[string]any{
		"Conditions": st.Conditions, "Symptoms": symptoms, "Records": records, "DBQs": dbqs, "Meds": st.Meds,
		"Analysis": st.Analysis, "Err": r.URL.Query().Get("err"),
		"JustAnalyzed": r.URL.Query().Get("analyzed") == "1", // the count-up plays once, right after an analysis
		"AdvisorReady": st.aiReady(),
	})
}

// combineTwo applies the VA combined-ratings formula to two values.
func combineTwo(a, b float64) float64 { return a + b*(100-a)/100 }

// combinedRating estimates the VA combined disability rating from per-condition
// percentages, applying the bilateral factor and rounding to the nearest 10 -
// the same math a rater uses. It is an estimate, not a decision.
func combinedRating(items []AnalyzedCondition) int {
	var bil, non []float64
	for _, it := range items {
		if it.Percent <= 0 {
			continue
		}
		if it.Bilateral {
			bil = append(bil, float64(it.Percent))
		} else {
			non = append(non, float64(it.Percent))
		}
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(bil)))
	sort.Sort(sort.Reverse(sort.Float64Slice(non)))
	parts := append([]float64{}, non...)
	if len(bil) >= 2 {
		acc := bil[0]
		for _, v := range bil[1:] {
			acc = math.Round(combineTwo(acc, v))
		}
		parts = append(parts, acc*1.10) // bilateral factor: +10%
	} else {
		parts = append(parts, bil...)
	}
	if len(parts) == 0 {
		return 0
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(parts)))
	acc := parts[0]
	for _, v := range parts[1:] {
		acc = math.Round(combineTwo(acc, v))
	}
	return int(math.Round(acc/10) * 10)
}

func ratingColor(pct int) string {
	switch {
	case pct >= 70:
		return "top"
	case pct >= 50:
		return "high"
	case pct >= 30:
		return "mid"
	default:
		return "low"
	}
}

// medicalAnalyze plays C&P examiner: it reads the DBQs and records plus the
// structured condition notes and returns a per-condition rating estimate. The
// combined rating is computed in Go; only the clinical judgment is the model's.
func (s *Server) medicalAnalyze(w http.ResponseWriter, r *http.Request) {
	st := s.store.snapshot()
	if !st.aiReady() {
		http.Redirect(w, r, "/medical?err="+url.QueryEscape("Choose Claude or ChatGPT for the Advisor in Settings first. The analysis runs through it."), http.StatusSeeOther)
		return
	}
	if len(st.Conditions) == 0 {
		http.Redirect(w, r, "/medical?err="+url.QueryEscape("Add the conditions you intend to claim first; the analysis rates those against your evidence."), http.StatusSeeOther)
		return
	}

	var ev strings.Builder
	ev.WriteString("Rate each claimed condition below under 38 CFR Part 4. Evidence follows.\n\nCLAIMED CONDITIONS:\n")
	for _, c := range st.Conditions {
		fmt.Fprintf(&ev, "- %s [in record: %t, DBQ studied: %t] %s\n", c.Name, c.Documented, c.DBQ, c.Notes)
	}
	if len(st.Symptoms) > 0 {
		ev.WriteString("\nSYMPTOM LOG (recent):\n")
		from := 0
		if len(st.Symptoms) > 15 {
			from = len(st.Symptoms) - 15
		}
		for _, x := range st.Symptoms[from:] {
			fmt.Fprintf(&ev, "- %s %s: %s\n", x.Date, x.Condition, x.Note)
		}
	}
	// The model reads the PDFs itself, from the docs folder only, DBQs first.
	var dbqNames, recNames, files []string
	pdf := func(d Doc) {
		if strings.HasSuffix(strings.ToLower(d.Name), ".pdf") && len(files) < 20 {
			files = append(files, filepath.Base(s.docPath(d.File))+"  ("+d.Name+")")
		}
	}
	for _, d := range st.Docs {
		if d.Kind == "dbq" {
			dbqNames = append(dbqNames, d.Name)
			pdf(d)
		}
	}
	for _, d := range st.Docs {
		if d.Kind == "medical" {
			recNames = append(recNames, d.Name)
			pdf(d)
		}
	}
	if len(dbqNames) > 0 {
		fmt.Fprintf(&ev, "\nDBQs on file: %s\n", strings.Join(dbqNames, "; "))
	}
	if len(recNames) > 0 {
		fmt.Fprintf(&ev, "\nMEDICAL RECORDS on file: %s\n", strings.Join(recNames, "; "))
	}
	docsDir, _ := filepath.Abs(filepath.Join(s.data, "docs"))
	if len(files) > 0 {
		fmt.Fprintf(&ev, "\nRead these PDFs in %s before rating:\n- %s\n", docsDir, strings.Join(files, "\n- "))
	} else {
		docsDir = ""
	}
	ev.WriteString("\nRate ONLY the claimed conditions. Do not invent findings. Be realistic and conservative; label nothing as certain.")

	sys := "You are an experienced VA Compensation & Pension examiner. You rate claimed conditions under 38 CFR Part 4 by the most appropriate diagnostic code, using only the evidence provided. " +
		"Assign a realistic integer percent per condition. Mark bilateral true only for a paired left/right extremity condition. Give a one-sentence rationale citing the specific finding. " +
		"This is a planning estimate for the veteran, not an official decision, and you never overstate the evidence."

	prompt := sys + "\n\n" + ev.String() + "\n\nRespond with ONLY a JSON object: {\"conditions\":[{\"name\":str,\"percent\":int,\"bilateral\":bool,\"code\":str,\"rationale\":str}],\"summary\":str}. No prose, no code fence."
	out, err := askAI(r.Context(), st.Settings.AdvisorProvider, prompt, docsDir)
	if err != nil {
		http.Redirect(w, r, "/medical?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	var parsed struct {
		Conditions []AnalyzedCondition `json:"conditions"`
		Summary    string              `json:"summary"`
	}
	if json.Unmarshal([]byte(extractJSON(out)), &parsed) != nil || len(parsed.Conditions) == 0 {
		http.Redirect(w, r, "/medical?err="+url.QueryEscape("The analysis came back unreadable. Try again."), http.StatusSeeOther)
		return
	}
	items, summary := parsed.Conditions, parsed.Summary

	combined := combinedRating(items)
	an := &ClaimAnalysis{
		At: time.Now().Format("2006-01-02 15:04"), Combined: combined,
		Color: ratingColor(combined), Summary: summary, Items: items,
		Provider: providerLabel(st.Settings.AdvisorProvider),
	}
	_ = s.store.mutate(func(st *State) {
		st.Analysis = an
		st.Settings.VaEstimate = combined // the dashboard box mirrors the analysis
	})
	http.Redirect(w, r, "/medical?analyzed=1", http.StatusSeeOther)
}

// extractJSON pulls the first {...} object out of a text blob.
func extractJSON(s string) string {
	i := strings.Index(s, "{")
	j := strings.LastIndex(s, "}")
	if i >= 0 && j > i {
		return s[i : j+1]
	}
	return s
}

func (s *Server) medAdd(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name != "" {
		_ = s.store.mutate(func(st *State) {
			st.Meds = append(st.Meds, Med{
				ID: st.id(), Name: name,
				Dose:       strings.TrimSpace(r.FormValue("dose")),
				For:        strings.TrimSpace(r.FormValue("for")),
				Prescriber: strings.TrimSpace(r.FormValue("prescriber")),
				Notes:      strings.TrimSpace(r.FormValue("notes")),
			})
		})
	}
	http.Redirect(w, r, "/medical", http.StatusSeeOther)
}

func (s *Server) medDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	_ = s.store.mutate(func(st *State) {
		for i, x := range st.Meds {
			if x.ID == id {
				st.Meds = append(st.Meds[:i], st.Meds[i+1:]...)
				return
			}
		}
	})
	http.Redirect(w, r, "/medical", http.StatusSeeOther)
}

func (s *Server) conditionAdd(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name != "" {
		_ = s.store.mutate(func(st *State) {
			st.Conditions = append(st.Conditions, Condition{
				ID: st.id(), Name: name,
				Notes: strings.TrimSpace(r.FormValue("notes")),
			})
		})
	}
	http.Redirect(w, r, "/medical", http.StatusSeeOther)
}

func (s *Server) conditionToggle(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	field := r.FormValue("field")
	_ = s.store.mutate(func(st *State) {
		for i := range st.Conditions {
			if st.Conditions[i].ID == id {
				switch field {
				case "documented":
					st.Conditions[i].Documented = !st.Conditions[i].Documented
				case "dbq":
					st.Conditions[i].DBQ = !st.Conditions[i].DBQ
				}
			}
		}
	})
	http.Redirect(w, r, "/medical", http.StatusSeeOther)
}

func (s *Server) conditionDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	_ = s.store.mutate(func(st *State) {
		st.Conditions = deleteByID(st.Conditions, id, func(c Condition) int { return c.ID })
	})
	http.Redirect(w, r, "/medical", http.StatusSeeOther)
}

func (s *Server) symptomAdd(w http.ResponseWriter, r *http.Request) {
	note := strings.TrimSpace(r.FormValue("note"))
	if note != "" {
		_ = s.store.mutate(func(st *State) {
			st.Symptoms = append(st.Symptoms, Symptom{
				ID: st.id(), Note: note,
				Date:      orToday(r.FormValue("date")),
				Condition: strings.TrimSpace(r.FormValue("condition")),
			})
		})
	}
	http.Redirect(w, r, "/medical", http.StatusSeeOther)
}

func (s *Server) symptomDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	_ = s.store.mutate(func(st *State) {
		st.Symptoms = deleteByID(st.Symptoms, id, func(x Symptom) int { return x.ID })
	})
	http.Redirect(w, r, "/medical", http.StatusSeeOther)
}

// timelineTaskAdd puts a new dated task straight onto the chart.
func (s *Server) timelineTaskAdd(w http.ResponseWriter, r *http.Request) {
	title := strings.TrimSpace(r.FormValue("title"))
	due := strings.TrimSpace(r.FormValue("due"))
	if title != "" && due != "" {
		phase := strings.TrimSpace(r.FormValue("phase"))
		if phase == "" {
			phase = "My Tasks"
		}
		_ = s.store.mutate(func(st *State) {
			st.Todos = append(st.Todos, Todo{
				ID: st.id(), Title: title, Due: due,
				Start:  strings.TrimSpace(r.FormValue("start")),
				Notes:  strings.TrimSpace(r.FormValue("notes")),
				Source: "timeline", Phase: phase,
			})
		})
	}
	http.Redirect(w, r, "/timeline", http.StatusSeeOther)
}

// ---------- Budget ----------------------------------------------------------

func monthTotals(txns []Txn, when time.Time) (in, out int64) {
	prefix := when.Format("2006-01")
	for _, t := range txns {
		if strings.HasPrefix(t.Date, prefix) {
			if t.Amount >= 0 {
				in += t.Amount
			} else {
				out += -t.Amount
			}
		}
	}
	return in, out
}

// ---------- Avalanche payoff projection ------------------------------------

type payLine struct {
	Name string
	Pay  int64
	Half int64
}
type planRow struct {
	Name  string
	Cells []int64 // payment per month, aligned to the month columns; 0 = nothing
}
type monthPlan struct {
	Idx       int
	Date      string
	Total     int64
	Half      int64
	Target    string
	ToTarget  int64
	Remaining int64
}
type payoffLine struct {
	Name, APR, PaidBy string
	Months            int
	Interest          int64
}

// parseAPRnum pulls a yearly percent from a messy APR string ("~29.99%",
// "Installment", "0% promo"). Non-numeric or installment plans read as 0 -
// the avalanche then correctly attacks them last.
func parseAPRnum(s string) float64 {
	var b strings.Builder
	seenDigit := false
	for _, c := range s {
		if c >= '0' && c <= '9' {
			b.WriteRune(c)
			seenDigit = true
		} else if c == '.' && seenDigit {
			b.WriteRune(c)
		} else if seenDigit {
			break
		}
	}
	if !seenDigit {
		return 0
	}
	f, _ := strconv.ParseFloat(b.String(), 64)
	return f
}

// avalancheProject simulates the debt avalanche month by month: interest accrues,
// minimums are paid on every open debt, and the leftover budget (freed minimums +
// engine) rolls onto the highest-APR open debt until it dies, then the next.
func avalancheProject(debts []Debt, engine int64) (months []monthPlan, payoff []payoffLine, thisMonth []payLine, debtFree string, totalInterest int64, tbd []string, planMonths []string, planRows []planRow, ok bool) {
	type d struct {
		name          string
		aprStr        string
		bal, min, apr float64
		interest      float64
		paid          bool
		paidMonth     int
		hist          []int64
	}
	var ds []*d
	for _, x := range debts {
		if x.PaidOff {
			continue
		}
		if x.Balance == 0 {
			tbd = append(tbd, x.Name)
			continue
		}
		ds = append(ds, &d{name: x.Name, aprStr: x.APR, bal: float64(x.Balance) / 100, min: float64(x.Min) / 100, apr: parseAPRnum(x.APR)})
	}
	if len(ds) == 0 {
		return
	}
	var mins float64
	for _, x := range ds {
		mins += x.min
	}
	budget := mins + float64(engine)/100
	if budget < mins {
		budget = mins // never pay less than the minimums
	}
	now := time.Now()
	var totInterest float64
	for mo := 0; mo < 360; mo++ {
		anyOpen := false
		for _, x := range ds {
			if !x.paid {
				anyOpen = true
			}
		}
		if !anyOpen {
			break
		}
		// interest
		for _, x := range ds {
			if !x.paid && x.apr > 0 {
				it := x.bal * (x.apr / 100 / 12)
				x.bal += it
				x.interest += it
				totInterest += it
			}
		}
		pays := map[*d]float64{} // keyed by debt, not name: two cards can share a name
		remaining := budget
		// minimums first
		for _, x := range ds {
			if x.paid {
				continue
			}
			p := x.min
			if p > x.bal {
				p = x.bal
			}
			if p > remaining {
				p = remaining
			}
			x.bal -= p
			remaining -= p
			pays[x] += p
		}
		// target for this month = highest-APR open debt (before extra applied)
		var targetName string
		{
			var tgt *d
			for _, x := range ds {
				if x.paid || x.bal <= 0.005 {
					continue
				}
				if tgt == nil || x.apr > tgt.apr {
					tgt = x
				}
			}
			if tgt != nil {
				targetName = tgt.name
			}
		}
		toTarget := 0.0
		// roll the leftover onto the highest-APR open debt, cascading
		for remaining > 0.005 {
			var tgt *d
			for _, x := range ds {
				if x.paid || x.bal <= 0.005 {
					continue
				}
				if tgt == nil || x.apr > tgt.apr {
					tgt = x
				}
			}
			if tgt == nil {
				break
			}
			p := remaining
			if p > tgt.bal {
				p = tgt.bal
			}
			tgt.bal -= p
			remaining -= p
			pays[tgt] += p
			if tgt.name == targetName {
				toTarget += p
			}
			if tgt.bal <= 0.005 {
				tgt.paid = true
				tgt.paidMonth = mo + 1
				tgt.bal = 0
			}
		}
		for _, x := range ds {
			if !x.paid && x.bal <= 0.005 {
				x.paid = true
				x.paidMonth = mo + 1
				x.bal = 0
			}
		}
		var total, rem float64
		for _, p := range pays {
			total += p
		}
		for _, x := range ds {
			rem += x.bal
		}
		mdate := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, mo, 0)
		months = append(months, monthPlan{
			Idx: mo + 1, Date: mdate.Format("Jan 2006"),
			Total: int64(total*100 + 0.5), Half: int64(total*50 + 0.5),
			Target: targetName, ToTarget: int64(toTarget*100 + 0.5), Remaining: int64(rem*100 + 0.5),
		})
		if mo == 0 {
			for _, x := range ds {
				p := pays[x]
				thisMonth = append(thisMonth, payLine{Name: x.name, Pay: int64(p*100 + 0.5), Half: int64(p*50 + 0.5)})
			}
		}
		planMonths = append(planMonths, mdate.Format("Jan '06"))
		for _, x := range ds {
			x.hist = append(x.hist, int64(pays[x]*100+0.5))
		}
	}
	for _, x := range ds {
		planRows = append(planRows, planRow{Name: x.name, Cells: x.hist})
	}
	for _, x := range ds {
		pl := payoffLine{Name: x.name, APR: x.aprStr, Interest: int64(x.interest*100 + 0.5), Months: x.paidMonth}
		if x.paidMonth > 0 {
			pl.PaidBy = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, x.paidMonth-1, 0).Format("Jan 2006")
		} else {
			pl.PaidBy = "beyond 30 yrs"
		}
		payoff = append(payoff, pl)
	}
	allPaid := true
	for _, x := range ds {
		allPaid = allPaid && x.paid
	}
	if n := len(months); n > 0 && allPaid {
		debtFree = months[n-1].Date
	} else if n > 0 {
		debtFree = "not within 30 years at these payments"
	}
	totalInterest = int64(totInterest*100 + 0.5)
	ok = true
	return
}

func (s *Server) budget(w http.ResponseWriter, r *http.Request) {
	st := s.store.snapshot()
	items := append([]Txn(nil), st.Txns...)
	sort.Slice(items, func(i, j int) bool { return items[i].Date > items[j].Date })
	if len(items) > 60 {
		items = items[:60]
	}
	in, out := monthTotals(st.Txns, time.Now())
	debts := append([]Debt(nil), st.Debts...)
	sort.Slice(debts, func(i, j int) bool { return debts[i].Priority < debts[j].Priority })

	// The worksheet: income minus bills minus open-debt minimums equals the
	// unassigned cash flow. When income is set, that COMPUTED number is the
	// avalanche engine; the settings value is the fallback before then.
	var billsTotal, debtMins int64
	for _, b := range st.Bills {
		billsTotal += b.Amount
	}
	for _, d := range debts {
		if !d.PaidOff {
			debtMins += d.Min
		}
	}
	engine := st.Settings.PowerEngine
	computed := false
	if st.Settings.MonthlyIncome > 0 {
		engine = st.Settings.MonthlyIncome - billsTotal - debtMins
		computed = true
	}
	// The target is the avalanche target: the open debt with the highest APR
	// (ties go to your priority order), the same rule the payoff plan uses.
	var target string
	power := engine
	var tgt *Debt
	for i := range debts {
		d := &debts[i]
		if d.PaidOff || d.Balance == 0 {
			continue
		}
		if tgt == nil || parseAPRnum(d.APR) > parseAPRnum(tgt.APR) {
			tgt = d
		}
	}
	if tgt != nil {
		target = tgt.Name
		power += tgt.Min
	}
	// The gist: total open debt, the clock to the debt-free date, and the
	// required monthly pace versus the money actually available for debt
	// (income minus living expenses). TBD balances make the total a FLOOR,
	// so the card says how many are still unknown.
	var totalDebt int64
	tbd := 0
	for _, d := range debts {
		if d.PaidOff {
			continue
		}
		if d.Balance == 0 {
			tbd++
		}
		totalDebt += d.Balance
	}
	// Every key the template reads exists from the start, so a fresh install
	// with no goal date renders zeros instead of failing.
	gist := map[string]any{
		"TotalDebt": totalDebt, "TBD": tbd,
		"Available": st.Settings.MonthlyIncome - billsTotal,
		"Goal":      "", "Months": 0, "Need": int64(0), "Margin": st.Settings.MonthlyIncome - billsTotal,
	}
	if st.Settings.DebtFreeBy != "" {
		if goal, err := parseDay(st.Settings.DebtFreeBy); err == nil {
			now := time.Now()
			months := (goal.Year()-now.Year())*12 + int(goal.Month()) - int(now.Month())
			if months < 1 {
				months = 1
			}
			need := totalDebt / int64(months)
			avail := st.Settings.MonthlyIncome - billsTotal
			gist["Goal"] = goal.Format("Jan 2 2006")
			gist["Months"] = months
			gist["Need"] = need
			gist["Margin"] = avail - need
		}
	}
	// Recommended payment per card: minimums everywhere, the whole engine on
	// the avalanche target - the avalanche in numbers.
	type debtView struct {
		Debt
		Pay int64
	}
	views := make([]debtView, 0, len(debts))
	for _, d := range debts {
		v := debtView{Debt: d}
		if !d.PaidOff {
			v.Pay = d.Min
			if tgt != nil && d.ID == tgt.ID { // the avalanche target gets the extra
				v.Pay = d.Min + engine
				if engine < 0 {
					v.Pay = d.Min
				}
				if d.Balance > 0 && v.Pay > d.Balance {
					v.Pay = d.Balance
				}
			}
		}
		views = append(views, v)
	}
	_, _, _, debtFree, totalInterest, planTBD, planMonths, planRows, planOK := avalancheProject(debts, engine)

	// Live countdown and dynamic daily pace calculation based on today
	now := time.Now()
	todayFormatted := now.Format("Monday, Jan 2, 2006")
	todayShort := now.Format("Jan 2, 2006")

	goalStr := st.Settings.DebtFreeBy
	var goalDate time.Time
	hasGoal := false
	if goalStr != "" {
		if t, err := parseDay(goalStr); err == nil {
			goalDate = t
			hasGoal = true
		}
	}
	if !hasGoal && debtFree != "" {
		if t, err := time.Parse("Jan 2006", debtFree); err == nil {
			goalDate = t
			goalStr = t.Format("2006-01-02")
			hasGoal = true
		}
	}

	daysLeft := 0
	paychecksLeft := 0
	var needPerDay int64
	var needPerWeek int64
	var needPerPaycheck int64
	goalFormatted := ""
	isoTarget := ""

	if hasGoal {
		goalFormatted = goalDate.Format("Jan 2, 2006")
		isoTarget = goalDate.Format("2006-01-02T00:00:00")
		diff := goalDate.Sub(localDay(now))
		daysLeft = int(diff.Hours() / 24)
		if daysLeft < 0 {
			daysLeft = 0
		}
		if daysLeft > 0 && totalDebt > 0 {
			needPerDay = totalDebt / int64(daysLeft)
			needPerWeek = needPerDay * 7
		}

		// Calculate remaining DFAS paychecks (1st and 15th of each month)
		cur := localDay(now).AddDate(0, 0, 1)
		for !cur.After(goalDate) {
			if cur.Day() == 1 || cur.Day() == 15 {
				paychecksLeft++
			}
			cur = cur.AddDate(0, 0, 1)
		}
		if paychecksLeft < 1 && daysLeft > 0 {
			paychecksLeft = 1
		}
		if paychecksLeft > 0 && totalDebt > 0 {
			needPerPaycheck = totalDebt / int64(paychecksLeft)
		}
	}

	// Next military payday (1st or 15th)
	var nextPayday time.Time
	if now.Day() < 15 {
		nextPayday = time.Date(now.Year(), now.Month(), 15, 0, 0, 0, 0, now.Location())
	} else {
		nextPayday = time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, now.Location())
	}
	daysToNextPay := daysBetween(localDay(now), nextPayday)
	nextPaydayFormatted := nextPayday.Format("Mon, Jan 2")

	// Current month run-rate and day progression
	daysInMonth := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, now.Location()).Day()
	dayOfMonth := now.Day()
	daysLeftInMonth := daysInMonth - dayOfMonth
	monthPct := (dayOfMonth * 100) / daysInMonth

	// Starting baseline progress
	baseline := st.Settings.DebtBaseline
	if totalDebt > baseline {
		baseline = totalDebt
	}
	paidOffSoFar := int64(0)
	overallDebtPct := 0
	if baseline > 0 {
		paidOffSoFar = baseline - totalDebt
		if paidOffSoFar < 0 {
			paidOffSoFar = 0
		}
		overallDebtPct = int((paidOffSoFar * 100) / baseline)
	}

	countdown := map[string]any{
		"HasGoal":         hasGoal,
		"GoalDate":        goalStr,
		"GoalFormatted":   goalFormatted,
		"ISOTarget":       isoTarget,
		"TodayFormatted":  todayFormatted,
		"TodayShort":      todayShort,
		"DaysLeft":        daysLeft,
		"PaychecksLeft":   paychecksLeft,
		"NeedPerDay":      needPerDay,
		"NeedPerWeek":     needPerWeek,
		"NeedPerPaycheck": needPerPaycheck,
		"NextPayday":      nextPaydayFormatted,
		"DaysToNextPay":   daysToNextPay,
		"DayOfMonth":      dayOfMonth,
		"DaysInMonth":     daysInMonth,
		"DaysLeftInMonth": daysLeftInMonth,
		"MonthPct":        monthPct,
		"Baseline":        baseline,
		"PaidOffSoFar":    paidOffSoFar,
		"OverallDebtPct":  overallDebtPct,
		"TotalDebt":       totalDebt,
	}

	s.page(w, "budget", map[string]any{
		"Items": items, "MonthIn": in, "MonthOut": out, "Net": in - out,
		"Debts": views, "Target": target, "Power": power,
		"PlanMonths": planMonths, "PlanRows": planRows, "DebtFree": debtFree,
		"PlanInterest": totalInterest, "PlanTBD": planTBD, "HasPlan": planOK && engine > 0,
		"Bills": st.Bills, "BillsTotal": billsTotal, "DebtMins": debtMins,
		"Engine": engine, "EngineComputed": computed, "Gist": gist,
		"Countdown": countdown, "Clock": moneyClock(st, totalDebt), "Sav": summarizeSavings(st), "Ret": estimateRetirePay(st), "Today": time.Now().Format("2006-01-02"),
	})
}

func (s *Server) txnAdd(w http.ResponseWriter, r *http.Request) {
	amt, err := parseMoney(r.FormValue("amount"))
	if err != nil {
		flash(w, "err", "That amount is not a dollar amount. Try something like 84.99.")
	}
	if err == nil {
		if strings.TrimSpace(r.FormValue("kind")) == "spend" && amt > 0 {
			amt = -amt
		}
		_ = s.store.mutate(func(st *State) {
			st.Txns = append(st.Txns, Txn{
				ID: st.id(), Amount: amt,
				Date:     orToday(r.FormValue("date")),
				Category: strings.TrimSpace(r.FormValue("category")),
				Note:     strings.TrimSpace(r.FormValue("note")),
			})
		})
	}
	http.Redirect(w, r, "/budget", http.StatusSeeOther)
}

func (s *Server) txnDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	_ = s.store.mutate(func(st *State) {
		st.Txns = deleteByID(st.Txns, id, func(t Txn) int { return t.ID })
	})
	http.Redirect(w, r, "/budget", http.StatusSeeOther)
}

// parseMoney reads a dollar amount into cents: "1,250", "$84.99", "-300",
// "-$12.5", ".50", "5.". Extra decimals round half up. Anything else, a
// second sign, or more than a trillion dollars is an error, never a guess.
func parseMoney(s string) (int64, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	neg := false
	if strings.HasPrefix(s, "-") {
		neg, s = true, s[1:]
	}
	s = strings.TrimPrefix(strings.TrimSpace(s), "$")
	if s == "" || s == "." || strings.Trim(s, "0123456789.") != "" || strings.Count(s, ".") > 1 {
		return 0, errors.New("not a dollar amount")
	}
	whole, frac, _ := strings.Cut(s, ".")
	if len(whole) > 12 {
		return 0, errors.New("amount too large")
	}
	var dollars int64
	if whole != "" {
		dollars, _ = strconv.ParseInt(whole, 10, 64)
	}
	var cents int64
	if frac != "" {
		f := (frac + "00")[:2]
		cents, _ = strconv.ParseInt(f, 10, 64)
		if len(frac) > 2 && frac[2] >= '5' {
			cents++
		}
	}
	v := dollars*100 + cents
	if neg {
		v = -v
	}
	return v, nil
}

func orToday(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Now().Format("2006-01-02")
	}
	return s
}

func (s *Server) billAdd(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	amt, ok := optionalMoney(r.FormValue("amount"))
	switch {
	case name == "":
		flash(w, "err", "Give the bill a name.")
	case !ok:
		flash(w, "err", "That amount is not a dollar amount. Try something like 84.99.")
	default:
		_ = s.store.mutate(func(st *State) {
			st.Bills = append(st.Bills, Bill{ID: st.id(), Name: name, Amount: amt})
		})
		flash(w, "ok", "Added "+name+".")
	}
	http.Redirect(w, r, "/budget", http.StatusSeeOther)
}

func (s *Server) billUpdate(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	amt, err := parseMoney(r.FormValue("amount"))
	if err != nil {
		flash(w, "err", "That amount is not a dollar amount. Try something like 84.99.")
	}
	if err == nil {
		_ = s.store.mutate(func(st *State) {
			for i := range st.Bills {
				if st.Bills[i].ID == id {
					st.Bills[i].Amount = amt
				}
			}
		})
	}
	http.Redirect(w, r, "/budget", http.StatusSeeOther)
}

func (s *Server) billDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	_ = s.store.mutate(func(st *State) {
		st.Bills = deleteByID(st.Bills, id, func(b Bill) int { return b.ID })
	})
	http.Redirect(w, r, "/budget", http.StatusSeeOther)
}

func (s *Server) incomeSave(w http.ResponseWriter, r *http.Request) {
	_ = s.store.mutate(func(st *State) {
		mid, midOK := parseMoney(r.FormValue("pay_mid"))
		end, endOK := parseMoney(r.FormValue("pay_end"))
		if midOK == nil {
			st.Settings.PayMid = mid
		}
		if endOK == nil {
			st.Settings.PayEnd = end
		}
		// Monthly take-home is the two pay periods combined. Only overwrite when
		// there is something to combine, so an empty save never zeroes income.
		if sum := st.Settings.PayMid + st.Settings.PayEnd; sum > 0 {
			st.Settings.MonthlyIncome = sum
		}
	})
	http.Redirect(w, r, "/budget", http.StatusSeeOther)
}

func (s *Server) budgetGoalSave(w http.ResponseWriter, r *http.Request) {
	_ = s.store.mutate(func(st *State) {
		goal := strings.TrimSpace(r.FormValue("debt_free_by"))
		if goal != "" {
			if _, err := parseDay(goal); err == nil {
				st.Settings.DebtFreeBy = goal
			}
		}
		if baseStr := strings.TrimSpace(r.FormValue("debt_baseline")); baseStr != "" {
			if v, err := parseMoney(baseStr); err == nil && v > 0 {
				st.Settings.DebtBaseline = v
			}
		}
	})
	http.Redirect(w, r, "/budget", http.StatusSeeOther)
}

func bumpBaseline(st *State) {
	var open int64
	for _, d := range st.Debts {
		if !d.PaidOff {
			open += d.Balance
		}
	}
	if open > st.Settings.DebtBaseline {
		st.Settings.DebtBaseline = open
	}
}

func (s *Server) debtAdd(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	bal, balOK := optionalMoney(r.FormValue("balance")) // blank = not captured yet
	min, minOK := optionalMoney(r.FormValue("min"))
	switch {
	case name == "":
		flash(w, "err", "Give the debt a name.")
	case !balOK || !minOK:
		flash(w, "err", "The balance and minimum must be dollar amounts, like 1,250.00. Leave them blank if you do not know yet.")
	default:
		flash(w, "ok", "Added "+name+" to the avalanche.")
		_ = s.store.mutate(func(st *State) {
			maxP := 0
			for _, d := range st.Debts {
				if d.Priority > maxP {
					maxP = d.Priority
				}
			}
			st.Debts = append(st.Debts, Debt{
				ID: st.id(), Name: name, APR: strings.TrimSpace(r.FormValue("apr")),
				Balance: bal, Min: min, Priority: maxP + 1,
			})
			bumpBaseline(st)
		})
	}
	http.Redirect(w, r, "/budget", http.StatusSeeOther)
}

func (s *Server) debtDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	_ = s.store.mutate(func(st *State) {
		st.Debts = deleteByID(st.Debts, id, func(d Debt) int { return d.ID })
	})
	http.Redirect(w, r, "/budget", http.StatusSeeOther)
}

func (s *Server) debtUpdate(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	bal, balErr := parseMoney(r.FormValue("balance"))
	min, minErr := parseMoney(r.FormValue("min"))
	apr := strings.TrimSpace(r.FormValue("apr"))
	_ = s.store.mutate(func(st *State) {
		for i := range st.Debts {
			if st.Debts[i].ID == id {
				if balErr == nil {
					st.Debts[i].Balance = bal
				}
				if minErr == nil {
					st.Debts[i].Min = min
				}
				if apr != "" {
					st.Debts[i].APR = apr
				}
			}
		}
		bumpBaseline(st)
	})
	http.Redirect(w, r, "/budget", http.StatusSeeOther)
}

func (s *Server) debtToggle(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	_ = s.store.mutate(func(st *State) {
		for i := range st.Debts {
			if st.Debts[i].ID == id {
				st.Debts[i].PaidOff = !st.Debts[i].PaidOff
			}
		}
	})
	http.Redirect(w, r, "/budget", http.StatusSeeOther)
}

// ---------- Resume ----------------------------------------------------------

func (s *Server) itp(w http.ResponseWriter, r *http.Request) {
	st := s.store.snapshot()
	ans := st.ITP
	if ans == nil {
		ans = map[string]string{}
	}
	wbID := 0
	for _, d := range st.Docs {
		// Any uploaded PDF with "ITP" in its name is treated as your workbook scan.
		n := strings.ToLower(d.Name)
		if strings.Contains(n, "itp") && strings.HasSuffix(n, ".pdf") {
			wbID = d.ID
		}
	}
	s.page(w, "itp", map[string]any{"A": ans, "Saved": r.URL.Query().Get("saved") != "", "WorkbookID": wbID})
}

// itpSave rebuilds the whole ITP from the submitted form so unchecked boxes clear.
func (s *Server) itpSave(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	next := map[string]string{}
	for k, v := range r.PostForm {
		if len(v) > 0 && strings.TrimSpace(v[0]) != "" {
			next[k] = strings.TrimSpace(v[0])
		}
	}
	_ = s.store.mutate(func(st *State) { st.ITP = next })
	http.Redirect(w, r, "/itp?saved=1", http.StatusSeeOther)
}

func (s *Server) resume(w http.ResponseWriter, r *http.Request) {
	st := s.store.snapshot()
	// One-time migration: fold any legacy single resume into a "General" target.
	if len(st.ResumeTargets) == 0 && len(st.Resume) > 0 {
		_ = s.store.mutate(func(st *State) {
			if len(st.ResumeTargets) == 0 && len(st.Resume) > 0 {
				st.ResumeTargets = append(st.ResumeTargets, ResumeTarget{
					ID: st.id(), Position: "General", Headline: st.Settings.ResumeHeadline,
					Sections: st.Resume,
				})
				st.Resume = nil
			}
		})
		st = s.store.snapshot()
	}
	var docs []Doc
	for _, d := range st.Docs {
		if d.Kind == "resume" {
			docs = append(docs, d)
		}
	}
	var active *ResumeTarget
	if t := r.URL.Query().Get("t"); t != "" {
		id, _ := strconv.Atoi(t)
		active = st.findTarget(id)
	}
	if active == nil && len(st.ResumeTargets) > 0 {
		active = &st.ResumeTargets[0]
	}
	s.page(w, "resume", map[string]any{
		"Targets": st.ResumeTargets, "Active": active, "Docs": docs, "ResumeDesigns": resumeDesigns, "ResumeFonts": resumeFonts, "ResumeColors": resumeColors,
	})
}

func parseBullets(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-")); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func (s *Server) resumeHeader(w http.ResponseWriter, r *http.Request) {
	_ = s.store.mutate(func(st *State) {
		if v := strings.TrimSpace(r.FormValue("name")); v != "" {
			st.Settings.Name = v
		}
		st.Settings.ResumeContact = strings.TrimSpace(r.FormValue("contact"))
	})
	http.Redirect(w, r, resumeBack(r), http.StatusSeeOther)
}

// resumeBack returns to the active target if one is given via ?t or form.
func resumeBack(r *http.Request) string {
	t := r.FormValue("t")
	if t == "" {
		t = r.URL.Query().Get("t")
	}
	if t != "" {
		return "/resume?t=" + url.QueryEscape(t)
	}
	return "/resume"
}

func (s *Server) resumeTargetAdd(w http.ResponseWriter, r *http.Request) {
	pos := strings.TrimSpace(r.FormValue("position"))
	if pos == "" {
		http.Redirect(w, r, "/resume", http.StatusSeeOther)
		return
	}
	var newID int
	_ = s.store.mutate(func(st *State) {
		newID = st.id()
		st.ResumeTargets = append(st.ResumeTargets, ResumeTarget{
			ID: newID, Position: pos, Company: strings.TrimSpace(r.FormValue("company")),
			Requirements: strings.TrimSpace(r.FormValue("requirements")),
		})
	})
	http.Redirect(w, r, fmt.Sprintf("/resume?t=%d", newID), http.StatusSeeOther)
}

func (s *Server) resumeTargetDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	_ = s.store.mutate(func(st *State) {
		for i := range st.ResumeTargets {
			if st.ResumeTargets[i].ID == id {
				st.ResumeTargets = append(st.ResumeTargets[:i], st.ResumeTargets[i+1:]...)
				return
			}
		}
	})
	http.Redirect(w, r, "/resume", http.StatusSeeOther)
}

func (s *Server) resumeTargetSave(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	_ = s.store.mutate(func(st *State) {
		if t := st.findTarget(id); t != nil {
			if v := strings.TrimSpace(r.FormValue("position")); v != "" {
				t.Position = v
			}
			t.Company = strings.TrimSpace(r.FormValue("company"))
			t.Headline = strings.TrimSpace(r.FormValue("headline"))
			t.Requirements = strings.TrimSpace(r.FormValue("requirements"))
		}
	})
	http.Redirect(w, r, fmt.Sprintf("/resume?t=%d", id), http.StatusSeeOther)
}

func (s *Server) resumeSectionAdd(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	heading := strings.TrimSpace(r.FormValue("heading"))
	if heading != "" {
		_ = s.store.mutate(func(st *State) {
			if t := st.findTarget(id); t != nil {
				t.Sections = append(t.Sections, ResumeSection{ID: st.id(), Heading: heading, Bullets: parseBullets(r.FormValue("bullets"))})
			}
		})
	}
	http.Redirect(w, r, fmt.Sprintf("/resume?t=%d", id), http.StatusSeeOther)
}

func (s *Server) resumeSectionEdit(w http.ResponseWriter, r *http.Request) {
	id, sid := pathID(r), pathIDName(r, "sid")
	_ = s.store.mutate(func(st *State) {
		if t := st.findTarget(id); t != nil {
			for i := range t.Sections {
				if t.Sections[i].ID == sid {
					if v := strings.TrimSpace(r.FormValue("heading")); v != "" {
						t.Sections[i].Heading = v
					}
					t.Sections[i].Bullets = parseBullets(r.FormValue("bullets"))
				}
			}
		}
	})
	http.Redirect(w, r, fmt.Sprintf("/resume?t=%d", id), http.StatusSeeOther)
}

func (s *Server) resumeSectionMove(w http.ResponseWriter, r *http.Request) {
	id, sid := pathID(r), pathIDName(r, "sid")
	up := strings.HasSuffix(r.URL.Path, "/up")
	_ = s.store.mutate(func(st *State) {
		if t := st.findTarget(id); t != nil {
			for i := range t.Sections {
				if t.Sections[i].ID == sid {
					j := i - 1
					if !up {
						j = i + 1
					}
					if j >= 0 && j < len(t.Sections) {
						t.Sections[i], t.Sections[j] = t.Sections[j], t.Sections[i]
					}
					return
				}
			}
		}
	})
	http.Redirect(w, r, fmt.Sprintf("/resume?t=%d", id), http.StatusSeeOther)
}

func (s *Server) resumeSectionDelete(w http.ResponseWriter, r *http.Request) {
	id, sid := pathID(r), pathIDName(r, "sid")
	_ = s.store.mutate(func(st *State) {
		if t := st.findTarget(id); t != nil {
			t.Sections = deleteByID(t.Sections, sid, func(x ResumeSection) int { return x.ID })
		}
	})
	http.Redirect(w, r, fmt.Sprintf("/resume?t=%d", id), http.StatusSeeOther)
}

// ---------- Jobs (USAJOBS) --------------------------------------------------

type jobHit struct {
	Title, Org, Location, URL, Closes string
	Source                            string // USAJOBS | Adzuna
	Posted                            string // YYYY-MM-DD
	Pay                               string
	Remote                            bool
}

func (s *Server) jobs(w http.ResponseWriter, r *http.Request) {
	st := s.store.snapshot()
	qv := r.URL.Query()
	jq := parseJobQuery(qv.Get("q"), qv.Get("zip"), qv.Get("radius"), qv.Get("mode"), qv.Get("where"))
	var res jobResults
	if jq.Q != "" {
		res = searchJobs(st.Settings, &jq)
	}
	s.page(w, "jobs", map[string]any{
		"Searches": st.JobSearches, "Hits": res.Hits, "Errs": res.Errs, "JQ": jq,
		"LinkRows": jobLinkRows(jq), "Radii": []int{10, 25, 50, 100},
		"HaveUSAJobs": st.Settings.USAJobsKey != "",
		"HaveAdzuna":  st.Settings.AdzunaID != "" && st.Settings.AdzunaKey != "",
		"Prospects":   st.Prospects, "Contacts": st.Contacts,
	})
}

// usajobsSearch queries the official USAJOBS API. extra carries the
// location, radius, and remote filters (LocationName, Radius, RemoteIndicator).
func usajobsSearch(cfg Settings, keyword string, extra url.Values) ([]jobHit, error) {
	q := url.Values{"Keyword": {keyword}, "ResultsPerPage": {"25"}}
	for k, v := range extra {
		q[k] = v
	}
	req, err := http.NewRequest("GET", "https://data.usajobs.gov/api/search?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", cfg.USAJobsEmail)
	req.Header.Set("Authorization-Key", cfg.USAJobsKey)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("USAJOBS returned %s · check the API key and email in Settings", resp.Status)
	}
	var body struct {
		SearchResult struct {
			SearchResultItems []struct {
				MatchedObjectDescriptor struct {
					PositionTitle    string `json:"PositionTitle"`
					OrganizationName string `json:"OrganizationName"`
					PositionURI      string `json:"PositionURI"`
					PositionLocation []struct {
						LocationName string `json:"LocationName"`
					} `json:"PositionLocation"`
					ApplicationCloseDate string `json:"ApplicationCloseDate"`
					PublicationStartDate string `json:"PublicationStartDate"`
					PositionRemuneration []struct {
						Min      string `json:"MinimumRange"`
						Max      string `json:"MaximumRange"`
						Interval string `json:"Description"`
					} `json:"PositionRemuneration"`
				} `json:"MatchedObjectDescriptor"`
			} `json:"SearchResultItems"`
		} `json:"SearchResult"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	var hits []jobHit
	for _, it := range body.SearchResult.SearchResultItems {
		d := it.MatchedObjectDescriptor
		loc := ""
		if len(d.PositionLocation) > 0 {
			loc = d.PositionLocation[0].LocationName
		}
		closes := d.ApplicationCloseDate
		if len(closes) >= 10 {
			closes = closes[:10]
		}
		h := jobHit{Title: d.PositionTitle, Org: d.OrganizationName, Location: loc, URL: d.PositionURI, Closes: closes, Source: "USAJOBS"}
		h.Remote = strings.Contains(strings.ToLower(loc), "remote")
		if len(d.PublicationStartDate) >= 10 {
			h.Posted = d.PublicationStartDate[:10]
		}
		if len(d.PositionRemuneration) > 0 {
			pr := d.PositionRemuneration[0]
			lo, _ := strconv.ParseFloat(pr.Min, 64)
			hi, _ := strconv.ParseFloat(pr.Max, 64)
			if lo > 0 {
				h.Pay = dollars(int(lo))
				if hi > lo {
					h.Pay += " to " + dollars(int(hi))
				}
				if pr.Interval != "" && pr.Interval != "Per Year" {
					h.Pay += " " + strings.ToLower(pr.Interval)
				}
			}
		}
		hits = append(hits, h)
	}
	return hits, nil
}

var prospectFlow = []string{"found", "applied", "interview", "offer"}

func (s *Server) prospectAdd(w http.ResponseWriter, r *http.Request) {
	title := strings.TrimSpace(r.FormValue("title"))
	if title != "" {
		_ = s.store.mutate(func(st *State) {
			st.Prospects = append(st.Prospects, Prospect{
				ID: st.id(), Title: title,
				Org:    strings.TrimSpace(r.FormValue("org")),
				URL:    strings.TrimSpace(r.FormValue("url")),
				Notes:  strings.TrimSpace(r.FormValue("notes")),
				Status: "found",
				Added:  time.Now().Format("2006-01-02"),
			})
		})
	}
	http.Redirect(w, r, "/jobs", http.StatusSeeOther)
}

func (s *Server) prospectAdvance(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	_ = s.store.mutate(func(st *State) {
		for i := range st.Prospects {
			if st.Prospects[i].ID == id {
				for n, v := range prospectFlow {
					if v == st.Prospects[i].Status && n < len(prospectFlow)-1 {
						st.Prospects[i].Status = prospectFlow[n+1]
						break
					}
				}
			}
		}
	})
	http.Redirect(w, r, "/jobs", http.StatusSeeOther)
}

func (s *Server) prospectDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	_ = s.store.mutate(func(st *State) {
		st.Prospects = deleteByID(st.Prospects, id, func(p Prospect) int { return p.ID })
	})
	http.Redirect(w, r, "/jobs", http.StatusSeeOther)
}

func (s *Server) contactAdd(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name != "" {
		_ = s.store.mutate(func(st *State) {
			st.Contacts = append(st.Contacts, Contact{
				ID: st.id(), Name: name,
				Org:   strings.TrimSpace(r.FormValue("org")),
				Role:  strings.TrimSpace(r.FormValue("role")),
				Info:  strings.TrimSpace(r.FormValue("info")),
				Notes: strings.TrimSpace(r.FormValue("notes")),
			})
		})
	}
	http.Redirect(w, r, "/jobs", http.StatusSeeOther)
}

func (s *Server) contactDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	_ = s.store.mutate(func(st *State) {
		st.Contacts = deleteByID(st.Contacts, id, func(c Contact) int { return c.ID })
	})
	http.Redirect(w, r, "/jobs", http.StatusSeeOther)
}

func (s *Server) jobSearchAdd(w http.ResponseWriter, r *http.Request) {
	kw := strings.TrimSpace(r.FormValue("keyword"))
	if kw != "" {
		_ = s.store.mutate(func(st *State) {
			jq := parseJobQuery(r.FormValue("keyword"), r.FormValue("zip"), r.FormValue("radius"), r.FormValue("mode"), r.FormValue("location"))
			st.JobSearches = append(st.JobSearches, JobSearch{
				ID: st.id(), Keyword: kw, Location: jq.Where, Zip: jq.Zip, Radius: jq.Radius, Mode: jq.Mode,
			})
		})
	}
	http.Redirect(w, r, "/jobs", http.StatusSeeOther)
}

func (s *Server) jobSearchDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	_ = s.store.mutate(func(st *State) {
		st.JobSearches = deleteByID(st.JobSearches, id, func(j JobSearch) int { return j.ID })
	})
	http.Redirect(w, r, "/jobs", http.StatusSeeOther)
}

// ---------- Settings --------------------------------------------------------

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	type provView struct {
		aiProvider
		Status string
	}
	var provs []provView
	for _, p := range aiProviders {
		provs = append(provs, provView{p, aiStatus(p.Key)})
	}
	s.page(w, "settings", map[string]any{"Err": r.URL.Query().Get("err"), "AIProviders": provs, "DataDir": absPath(s.data)})
}

func (s *Server) settingsSave(w http.ResponseWriter, r *http.Request) {
	_ = s.store.mutate(func(st *State) {
		st.Settings.Name = strings.TrimSpace(r.FormValue("name"))
		st.Settings.Branch = strings.TrimSpace(r.FormValue("branch"))
		st.Settings.RetirementDate = strings.TrimSpace(r.FormValue("retirement_date"))
		st.Settings.USAJobsEmail = strings.TrimSpace(r.FormValue("usajobs_email"))
		if v := strings.TrimSpace(r.FormValue("usajobs_key")); v != "" {
			st.Settings.USAJobsKey = v // blank keeps the saved key; the page never shows it
		}
		if r.FormValue("usajobs_key_clear") == "1" {
			st.Settings.USAJobsKey = ""
		}
		if r.Form.Has("weather_here") {
			st.Settings.WeatherHere = strings.TrimSpace(r.FormValue("weather_here"))
			st.Settings.WeatherRetire = strings.TrimSpace(r.FormValue("weather_retire"))
		}
		if r.Form.Has("adzuna_id") {
			st.Settings.AdzunaID = strings.TrimSpace(r.FormValue("adzuna_id"))
		}
		if v := strings.TrimSpace(r.FormValue("adzuna_key")); v != "" {
			st.Settings.AdzunaKey = v
		}
		if r.FormValue("adzuna_key_clear") == "1" {
			st.Settings.AdzunaID, st.Settings.AdzunaKey = "", ""
		}
		if v, err := strconv.Atoi(strings.TrimSpace(r.FormValue("va_estimate"))); err == nil && v >= 0 && v <= 100 {
			st.Settings.VaEstimate = v
		}
		if _, ok := findProvider(r.FormValue("advisor_provider")); ok || r.FormValue("advisor_provider") == "" {
			st.Settings.AdvisorProvider = r.FormValue("advisor_provider")
		}
		if v, err := parseMoney(r.FormValue("monthly_budget")); err == nil {
			st.Settings.MonthlyBudget = v
		}
		if r.Form.Has("leave_days") {
			if v, ok, err := parseLeaveDays(r.FormValue("leave_days")); err == nil {
				prev := st.Settings.LeaveDays
				st.Settings.LeaveDays, st.Settings.LeaveSet = v, ok
				asOf := strings.TrimSpace(r.FormValue("leave_as_of"))
				if _, err := parseDay(asOf); err != nil || (asOf == st.Settings.LeaveAsOf && v != prev) {
					asOf = time.Now().Format("2006-01-02") // a new number means a fresh reading
				}
				if !ok {
					asOf = ""
				}
				st.Settings.LeaveAsOf = asOf
			}
		}
	})
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// seedDemo fills a FRESH install with a fictional soldier so a new user sees a
// working app. Everything here is invented - no real person, no real records.
func seedDemo(st *State) {
	rday := time.Now().AddDate(0, 0, 430)
	st.Settings = Settings{
		Name: "SFC John Doe", Branch: "Army",
		RetirementDate: rday.Format("2006-01-02"),
		MonthlyIncome:  650000, DebtFreeBy: rday.AddDate(0, -4, 0).Format("2006-01-02"),
	}
	seedTimeline(st, rday)
	st.Settings.TimelineSeeded = true
	st.Settings.VaEstimate = 70
	st.Bills = []Bill{
		{ID: st.id(), Name: "Rent", Amount: 180000},
		{ID: st.id(), Name: "Car Payment", Amount: 45000},
		{ID: st.id(), Name: "Phone", Amount: 8500},
		{ID: st.id(), Name: "Groceries", Amount: 60000},
	}
	st.Debts = []Debt{
		{ID: st.id(), Name: "Visa", APR: "24.99%", Balance: 480000, Min: 12000, Priority: 1},
		{ID: st.id(), Name: "Car Loan", APR: "6.5%", Balance: 1450000, Min: 45000, Priority: 2},
		{ID: st.id(), Name: "Store Card", APR: "29.99%", Balance: 90000, Min: 3500, Priority: 3},
	}
	bumpBaseline(st)
	st.Conditions = []Condition{
		{ID: st.id(), Name: "Lower back pain", Documented: true, DBQ: false, Notes: "Chronic since a 2018 field rotation. Get an MRI on the record."},
		{ID: st.id(), Name: "Tinnitus", Documented: true, DBQ: true, Notes: "Ranges and the motor pool. Common, well understood by the VA."},
		{ID: st.id(), Name: "Sleep apnea (suspected)", Documented: false, DBQ: false, Notes: "Snoring, daytime fatigue. Ask for a sleep study."},
	}
	st.Appointments = []Appointment{
		{ID: st.id(), Title: "Pre-Separation Briefing", At: time.Now().AddDate(0, 1, 0).Format("2006-01-02") + "T09:00", Place: "Transition Center", Notes: "Bring your questions."},
	}
	st.Prospects = []Prospect{
		{ID: st.id(), Title: "Logistics Manager", Org: "Example Logistics Co.", Status: "found", Added: time.Now().Format("2006-01-02"), Notes: "Saw it on LinkedIn."},
	}
	st.Resources = []Resource{
		{ID: st.id(), Name: "Your SFL-TAP Center", Org: "Army", Info: "Look up your installation's number", Notes: "Start here."},
	}
	today := time.Now()
	st.Settings.SavingsGoal, st.Settings.SavingsGoalName = 1500000, "Move fund"
	st.Savings = []SavingsEntry{
		{ID: st.id(), Date: today.AddDate(0, -2, 0).Format("2006-01-02"), Amount: 150000, Note: "Payday transfer"},
		{ID: st.id(), Date: today.AddDate(0, -1, 0).Format("2006-01-02"), Amount: 150000, Note: "Payday transfer"},
		{ID: st.id(), Date: today.Format("2006-01-02"), Amount: 90000, Note: "Sold the old truck"},
	}
	st.Notes = []Note{{ID: st.id(), Date: today.Format("2006-01-02"), Topic: "Pre-separation briefing takeaways",
		Body: "Book the retirement physical early; slots fill months out.\nStart the symptom log now, not later."}}
	st.PARDone = map[string]string{"step-transitions": "done|" + today.Format("2006-01-02")}
	st.ResumeTargets = []ResumeTarget{{ID: st.id(), Position: "Logistics Manager", Company: "Example Logistics Co.",
		Headline: "Logistics leader, 20 years moving people and equipment worldwide",
		Sections: []ResumeSection{
			{ID: st.id(), Heading: "Experience", Bullets: []string{
				"Ran supply operations for a 700-person organization with zero audit findings over three years",
				"Cut equipment turnaround time 30 percent by redesigning the maintenance request process"}},
			{ID: st.id(), Heading: "Education", Bullets: []string{"B.S. Business Administration, in progress"}},
		}}}
}

func (s *Server) settingsDemo(w http.ResponseWriter, r *http.Request) {
	full := false
	_ = s.store.mutate(func(st *State) {
		// Guard: never overwrite a real install. Demo only loads into an empty app.
		if len(st.Todos) > 0 || st.Settings.Name != "" || len(st.Docs) > 0 {
			full = true
			return
		}
		next := st.NextID
		if next < 1 {
			next = 1
		}
		*st = State{NextID: next}
		seedDemo(st)
	})
	if full {
		http.Redirect(w, r, "/settings?err="+url.QueryEscape("Example data only loads into an empty app. Erase everything first if you want the demo."), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) settingsErase(w http.ResponseWriter, r *http.Request) {
	if err := s.store.mutate(func(st *State) { *st = State{NextID: 1} }); err == nil {
		// Erase means erase: uploaded records and the housing cache go too.
		docs := filepath.Join(s.data, "docs")
		_ = os.RemoveAll(docs)
		_ = os.MkdirAll(docs, 0o700)
		s.homes.reset()
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (s *Server) settingsSeed(w http.ResponseWriter, r *http.Request) {
	st := s.store.snapshot()
	if st.Settings.RetirementDate == "" || st.Settings.TimelineSeeded {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	rd, err := parseDay(st.Settings.RetirementDate)
	if err != nil {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	_ = s.store.mutate(func(st *State) {
		if !st.Settings.TimelineSeeded { // a double-click must not seed twice
			seedTimeline(st, rd)
		}
	})
	http.Redirect(w, r, "/timeline", http.StatusSeeOther)
}
