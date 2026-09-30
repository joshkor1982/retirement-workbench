package main

// SkillBridge: the DoD program that lets you train or intern with a civilian
// employer during your last 180 days of service. This tab finds programs,
// tracks leads, walks the approval packet, and drafts the request
// memorandum as a PDF.
//
// The official catalog (skillbridge.mil) sits behind a bot check, so the app
// opens it for you rather than reading it. Requirements differ by
// installation; every checklist item says to confirm with your Career Skills
// Program (CSP) office.

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type SBLead struct {
	ID       int    `json:"id"`
	Company  string `json:"company"`
	Program  string `json:"program"`
	Location string `json:"location"`
	URL      string `json:"url"`
	Status   string `json:"status"` // found | contacted | applied | accepted | approved
	Notes    string `json:"notes"`
	Added    string `json:"added"`
}

// SBPlan is the program you are applying for; it fills the memo.
type SBPlan struct {
	Company   string `json:"company"`
	Program   string `json:"program"`
	Location  string `json:"location"`
	Start     string `json:"start"` // YYYY-MM-DD
	End       string `json:"end"`
	Hours     string `json:"hours"`
	Duties    string `json:"duties"`
	POC       string `json:"poc"` // partner point of contact: name, email, phone
	Unit      string `json:"unit"`
	Address   string `json:"address"`
	Office    string `json:"office"` // office symbol
	Commander string `json:"commander"`
	Rank      string `json:"rank"` // your rank, for the signature block
	Contact   string `json:"contact"`
}

var sbFlow = []string{"found", "contacted", "applied", "accepted", "approved"}

var sbChecklist = []parItem{
	{Key: "sb-eligible", Title: "Confirm You Are Eligible",
		Detail: "SkillBridge runs in your last 180 days of service. Ask your installation CSP office about local rules, flags, and deadlines before you commit to a partner."},
	{Key: "sb-partner", Title: "Partner Is an Authorized SkillBridge Organization",
		Detail: "Find the company on skillbridge.mil. Programs outside the official list cannot be approved."},
	{Key: "sb-2648", Title: "DD Form 2648 (Pre-Separation Counseling) Complete",
		Detail: "Most offices will not start a SkillBridge packet until counseling is on file."},
	{Key: "sb-accept", Title: "Letter of Acceptance From the Partner",
		Detail: "On company letterhead, naming you, the program, the dates, and the location."},
	{Key: "sb-plan", Title: "Training Plan",
		Detail: "What you will learn, the schedule and hours, and who supervises you. The partner usually provides it."},
	{Key: "sb-memo", Title: "Request Memorandum",
		Detail: "Fill in Your Program below, then download the memo, sign it, and add it to the packet."},
	{Key: "sb-leave", Title: "Leave, PTDY, and Clearing Planned Around the Dates",
		Detail: "Your SkillBridge dates, PTDY, terminal leave, and clearing must not overlap in ways your unit cannot support."},
	{Key: "sb-command", Title: "Commander Approval",
		Detail: "Usually your unit commander, and often the first O-5 in your chain. Your CSP office knows the local approval authority.", Optional: false},
	{Key: "sb-agreement", Title: "Participation Agreement Signed",
		Detail: "Any agreement or acknowledgment your installation requires, signed by you and, where required, the partner.", Optional: true},
	{Key: "sb-submit", Title: "Packet Submitted and Approval Tracked",
		Detail: "Turn in the packet as your CSP office directs, and follow it weekly until you have signed approval."},
}

type sbView struct {
	Plan       SBPlan
	Days       int // to start; -1 unknown
	Length     int // program days
	WindowOpen string
	Warnings   []string
}

func buildSBView(st State) sbView {
	v := sbView{Days: -1}
	if st.SkillBridge != nil {
		v.Plan = *st.SkillBridge
	}
	start, sErr := parseDay(v.Plan.Start)
	end, eErr := parseDay(v.Plan.End)
	if sErr == nil {
		v.Days = daysBetween(localDay(time.Now()), start)
	}
	if sErr == nil && eErr == nil {
		v.Length = daysBetween(start, end) + 1
		if v.Length > 180 {
			v.Warnings = append(v.Warnings, fmt.Sprintf("The program runs %d days. SkillBridge allows up to 180.", v.Length))
		}
		if !end.After(start) {
			v.Warnings = append(v.Warnings, "The end date is not after the start date.")
		}
	}
	if rd, err := parseDay(st.Settings.RetirementDate); err == nil {
		open := rd.AddDate(0, 0, -180)
		v.WindowOpen = open.Format("Jan 2, 2006")
		if sErr == nil && start.Before(open) {
			v.Warnings = append(v.Warnings, "The start date is more than 180 days before your retirement date ("+v.WindowOpen+" is the earliest).")
		}
		if eErr == nil && end.After(rd) {
			v.Warnings = append(v.Warnings, "The program ends after your retirement date.")
		}
	}
	return v
}

func sbLinkRows(jq jobQuery) []jobLinkRow {
	e := url.QueryEscape
	q := strings.TrimSpace("SkillBridge " + jq.Q)
	official := []jobLink{
		{"SkillBridge Opportunities", "https://skillbridge.mil/opportunities.htm", "official catalog"},
		{"SkillBridge Locations Map", "https://skillbridge.mil/locations.htm", "official"},
		{"Hiring Our Heroes Fellowships", "https://www.hiringourheroes.org/career-services/fellowships/", "12-week corporate fellowship"},
		{"Army Career Skills Program", "https://home.army.mil/imcom/index.php/customers/career-skills-program", "Army's SkillBridge"},
	}
	rows := []jobLinkRow{{"Official programs", official}}
	if jq.Q == "" {
		return rows
	}
	loc := firstNonEmpty(jq.Zip, jq.Where)
	var boards []jobLink
	if jq.Mode == "remote" {
		boards = []jobLink{
			{"LinkedIn", "https://www.linkedin.com/jobs/search/?keywords=" + e(q) + "&location=United%20States&f_WT=2", ""},
			{"Indeed", "https://www.indeed.com/jobs?q=" + e(q) + "&l=Remote", ""},
			{"Google Jobs", "https://www.google.com/search?ibp=htl;jobs&q=" + e("remote "+q), ""},
		}
	} else if loc == "" {
		boards = []jobLink{
			{"LinkedIn", "https://www.linkedin.com/jobs/search/?keywords=" + e(q) + "&location=United%20States", ""},
			{"Indeed", "https://www.indeed.com/jobs?q=" + e(q) + "&l=United+States", ""},
			{"Google Jobs", "https://www.google.com/search?ibp=htl;jobs&q=" + e(q), ""},
		}
	} else {
		r := fmt.Sprint(jq.Radius)
		boards = []jobLink{
			{"LinkedIn", "https://www.linkedin.com/jobs/search/?keywords=" + e(q) + "&location=" + e(loc) + "&distance=" + r, ""},
			{"Indeed", "https://www.indeed.com/jobs?q=" + e(q) + "&l=" + e(loc) + "&radius=" + r, ""},
			{"Google Jobs", "https://www.google.com/search?ibp=htl;jobs&q=" + e(q+" near "+loc), ""},
		}
	}
	return append(rows, jobLinkRow{`Job boards: "` + q + `"`, boards})
}

func (s *Server) skillbridge(w http.ResponseWriter, r *http.Request) {
	st := s.store.snapshot()
	qv := r.URL.Query()
	jq := parseJobQuery(qv.Get("q"), qv.Get("zip"), qv.Get("radius"), qv.Get("mode"), "")
	var res jobResults
	if jq.Q != "" {
		sq := jq
		sq.Q = "SkillBridge " + jq.Q
		res = searchJobs(st.Settings, &sq)
	}
	s.page(w, "skillbridge", map[string]any{
		"SB": buildSBView(st), "JQ": jq, "Radii": []int{10, 25, 50, 100},
		"LinkRows": sbLinkRows(jq), "Hits": res.Hits, "Errs": res.Errs,
		"Leads": st.SBLeads, "Checklist": parCards(sbChecklist, st.PARDone, &parView{}),
		"HaveSearch": st.Settings.USAJobsKey != "" || (st.Settings.AdzunaID != "" && st.Settings.AdzunaKey != ""),
	})
}

func (s *Server) sbPlanSave(w http.ResponseWriter, r *http.Request) {
	f := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	p := SBPlan{Company: f("company"), Program: f("program"), Location: f("location"), Start: f("start"), End: f("end"),
		Hours: f("hours"), Duties: f("duties"), POC: f("poc"), Unit: f("unit"), Address: f("address"),
		Office: f("office"), Commander: f("commander"), Rank: f("rank"), Contact: f("contact")}
	for _, d := range []string{p.Start, p.End} {
		if _, err := parseDay(d); d != "" && err != nil {
			flash(w, "err", "Use the date pickers for the start and end dates.")
			http.Redirect(w, r, "/skillbridge#program", http.StatusSeeOther)
			return
		}
	}
	_ = s.store.mutate(func(st *State) { st.SkillBridge = &p })
	flash(w, "ok", "Program saved.")
	http.Redirect(w, r, "/skillbridge#program", http.StatusSeeOther)
}

func (s *Server) sbLeadAdd(w http.ResponseWriter, r *http.Request) {
	company := strings.TrimSpace(r.FormValue("company"))
	if company == "" {
		flash(w, "err", "Give the lead a company name.")
	} else {
		_ = s.store.mutate(func(st *State) {
			st.SBLeads = append(st.SBLeads, SBLead{ID: st.id(), Company: company,
				Program: strings.TrimSpace(r.FormValue("program")), Location: strings.TrimSpace(r.FormValue("location")),
				URL: strings.TrimSpace(r.FormValue("url")), Notes: strings.TrimSpace(r.FormValue("notes")),
				Status: "found", Added: time.Now().Format("2006-01-02")})
		})
		flash(w, "ok", "Tracking "+company+".")
	}
	http.Redirect(w, r, "/skillbridge#leads", http.StatusSeeOther)
}

func (s *Server) sbLeadAdvance(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	_ = s.store.mutate(func(st *State) {
		for i := range st.SBLeads {
			if st.SBLeads[i].ID != id {
				continue
			}
			for n, v := range sbFlow {
				if v == st.SBLeads[i].Status && n < len(sbFlow)-1 {
					st.SBLeads[i].Status = sbFlow[n+1]
					break
				}
			}
		}
	})
	http.Redirect(w, r, "/skillbridge#leads", http.StatusSeeOther)
}

func (s *Server) sbLeadDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	_ = s.store.mutate(func(st *State) {
		st.SBLeads = deleteByID(st.SBLeads, id, func(l SBLead) int { return l.ID })
	})
	http.Redirect(w, r, "/skillbridge#leads", http.StatusSeeOther)
}

// sbMemoData fills the memo template.
func (s *Server) sbMemoData() map[string]any {
	st := s.store.snapshot()
	v := buildSBView(st)
	day := func(d string) string {
		if t, err := parseDay(d); err == nil {
			return t.Format("2 January 2006")
		}
		return "____________"
	}
	blank := func(x, alt string) string {
		if strings.TrimSpace(x) == "" {
			return alt
		}
		return x
	}
	p := v.Plan
	return map[string]any{
		"P": p, "Name": blank(st.Settings.Name, "YOUR NAME"),
		"NameUpper": strings.ToUpper(stripRank(blank(st.Settings.Name, "Your Name"), p.Rank)),
		"Branch":    blank(st.Settings.Branch, "Army"),
		"Today":     time.Now().Format("2 January 2006"),
		"Start":     day(p.Start), "End": day(p.End), "Length": v.Length,
		"Retire":  day(st.Settings.RetirementDate),
		"Company": blank(p.Company, "____________"), "Program": blank(p.Program, "____________"),
		"Location": blank(p.Location, "____________"), "Unit": blank(p.Unit, "____________"),
		"Commander": blank(p.Commander, "Commander, "+blank(p.Unit, "____________")),
		"Office":    blank(p.Office, "OFFICE SYMBOL"),
	}
}

func (s *Server) sbMemoPrint(w http.ResponseWriter, r *http.Request) {
	data := s.sbMemoData()
	data["Manual"] = r.URL.Query().Get("manual") == "1"
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, "sb_memo.html", data); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

func (s *Server) sbMemoPDF(w http.ResponseWriter, r *http.Request) {
	pdf, err := printToPDF(r.Context(), "http://"+r.Host+"/skillbridge/memo")
	if err != nil {
		http.Redirect(w, r, "/skillbridge/memo?manual=1", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="SkillBridge_Request_Memo.pdf"`)
	_, _ = w.Write(pdf)
}

// stripRank drops a leading rank from a name ("SFC John Doe" with rank SFC),
// so the signature block does not print the rank twice.
func stripRank(name, rank string) string {
	if rank != "" && len(name) > len(rank) && strings.EqualFold(name[:len(rank)], rank) && name[len(rank)] == ' ' {
		return strings.TrimSpace(name[len(rank):])
	}
	return name
}
