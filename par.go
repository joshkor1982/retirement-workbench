package main

// The PAR walkthrough on the Submit Packet page: the steps and the Enlisted
// Retirement Checklist as cards you mark complete. Progress lives in
// State.PARDone, keyed by the stable Key below, so rewording a card never
// loses a check mark. The PDF cover sheet still prints packetGuidance.

import (
	"net/http"
	"strings"
	"time"
)

type parItem struct {
	Key      string
	Title    string
	Detail   string
	Sub      []string
	Optional bool // conditional items can be marked Not Needed
}

var parSteps = []parItem{
	{Key: "step-transitions", Title: "Turn In the Transitions Checklist",
		Detail: "Take the Retirement Application Questionnaire and its required documents to the Transition Center. You get DA Form 2339 back, plus an appointment to review and sign it."},
	{Key: "step-gather", Title: "Gather the PAR Checklist Documents",
		Detail: "Collect every item in the Enlisted Retirement Checklist below, upload them in Packet Files, and combine them into one PDF."},
	{Key: "step-s1", Title: "Take the Packet to Your Unit S-1",
		Detail: "Bring the signed DA Form 2339 and the combined PDF."},
	{Key: "step-initiate", Title: "S-1 Initiates the PAR in IPPS-A",
		Detail: "S-1 starts the retirement PAR with your requested retirement date as the Effective Date and routes it to G-1, then HRC."},
	{Key: "step-track", Title: "Track the PAR Until Orders Publish",
		Detail: "Check IPPS-A weekly. Chase it with S-1 if it stalls."},
}

// parTransitions is what the Transition Center asks for to issue the
// DA Form 2339, the application the PAR is built on.
var parTransitions = []parItem{
	{Key: "tr-questionnaire", Title: "Retirement Application Questionnaire",
		Detail: "Filled out and signed. This starts your application at the Transition Center."},
	{Key: "tr-dd214", Title: "DD 214s and Prior-Service Documents",
		Detail: "Every DD 214 and any prior-service record, so your service dates add up."},
	{Key: "tr-dd93", Title: "DD 93, Record of Emergency Data",
		Detail: "Current and correct."},
	{Key: "tr-sgli", Title: "SGLI Election (SOES)",
		Detail: "Your current SGLI election and beneficiaries from SOES."},
	{Key: "tr-stp", Title: "Current Soldier Talent Profile (STP)",
		Detail: "A fresh printout."},
	{Key: "tr-2648", Title: "DD Form 2648, Pre-Separation Counseling",
		Detail: "Signed. Required before your retirement is processed."},
	{Key: "tr-orders", Title: "Current Orders",
		Detail: "Your most recent orders, if the Transition Center asks for them.", Optional: true},
}

var parChecklist = []parItem{
	{Key: "doc-par", Title: "Personnel Action Request (PAR)",
		Detail: "Submitted through IPPS-A. Effective Date = requested retirement date."},
	{Key: "doc-2339", Title: "DA Form 2339, Application for Voluntary Retirement",
		Detail: "Obtained from the Transition Center.",
		Sub: []string{
			"You sign blocks 19 and 30 on page 2",
			"Transition Center specialist signs block 31 on page 2",
			"Transition Center specialist verification memorandum",
		}},
	{Key: "doc-stp", Title: "Current Soldier Talent Profile (STP)",
		Detail: "Dated within 14 days of submission. Pull it last."},
	{Key: "doc-sa", Title: "Sexual Assault Statement",
		Detail: "Memorandum format. Mandatory for every separation and retirement action."},
	{Key: "doc-adso", Title: "ADSO",
		Detail: "SFC and above is 3 years. No waiver for the 9/11 GI Bill."},
	{Key: "doc-lateness", Title: "Late Request Documents", Optional: true,
		Detail: "Army Directive 2026-08 (17 April 2026) set the request window at 24 to 12 months before your retirement date. If you are inside 12 months, ask your S-1 and Retirement Services Officer what your request needs."},
	{Key: "doc-waivers", Title: "Waivers", Optional: true,
		Detail: "If applicable. Memorandum format."},
	{Key: "doc-deros", Title: "DEROS", Optional: true,
		Detail: "Only if the requested retirement date is before your DEROS date."},
	{Key: "doc-etp", Title: "Exception to Policy Request", Optional: true,
		Detail: "In lieu of PCS, if the request goes in more than 30 days after RFO or official assignment notification."},
	{Key: "doc-other", Title: "Additional Supporting Documents", Optional: true,
		Detail: "Exceptions to policy, withdrawals, or a date-change retirement request."},
}

type parCard struct {
	parItem
	N      int
	Status string // "" | done | na
	On     string // date it was marked
}

type parView struct {
	Steps, Transitions, Checklist []parCard
	Done, Total                   int
	WindowOpen                    string // 24 months before the retirement date
	WindowClose                   string // 12 months before (Army Directive 2026-08)
	WindowState                   string // before | open | late
}

func parCards(items []parItem, done map[string]string, v *parView) []parCard {
	out := make([]parCard, 0, len(items))
	for i, it := range items {
		c := parCard{parItem: it, N: i + 1}
		if val, ok := done[it.Key]; ok && len(val) > 11 {
			c.Status, c.On = val[:len(val)-11], val[len(val)-10:]
		}
		if c.Status != "" {
			v.Done++
		}
		v.Total++
		out = append(out, c)
	}
	return out
}

func buildPARView(st State) parView {
	var v parView
	v.Steps = parCards(parSteps, st.PARDone, &v)
	v.Transitions = parCards(parTransitions, st.PARDone, &v)
	v.Checklist = parCards(parChecklist, st.PARDone, &v)
	if rd, err := time.Parse("2006-01-02", st.Settings.RetirementDate); err == nil {
		open, close := rd.AddDate(-2, 0, 0), rd.AddDate(-1, 0, 0) // Army Directive 2026-08: 24 to 12 months out
		v.WindowOpen, v.WindowClose = open.Format("Jan 2, 2006"), close.Format("Jan 2, 2006")
		switch now := time.Now(); {
		case now.Before(open):
			v.WindowState = "before"
		case now.After(close):
			v.WindowState = "late"
		default:
			v.WindowState = "open"
		}
	}
	return v
}

func parKnown(key string) bool {
	for _, list := range [][]parItem{parSteps, parTransitions, parChecklist, sbChecklist} {
		for _, it := range list {
			if it.Key == key {
				return true
			}
		}
	}
	return false
}

// parMark sets a card to done or na, or clears it. Values are stored as
// "<status>|<YYYY-MM-DD>".
func (s *Server) parMark(w http.ResponseWriter, r *http.Request) {
	key, status := r.PathValue("key"), r.FormValue("status")
	if parKnown(key) {
		_ = s.store.mutate(func(st *State) {
			if st.PARDone == nil {
				st.PARDone = map[string]string{}
			}
			if status == "done" || status == "na" {
				st.PARDone[key] = status + "|" + time.Now().Format("2006-01-02")
			} else {
				delete(st.PARDone, key)
			}
		})
	}
	back := "/packet"
	if strings.HasPrefix(key, "sb-") {
		back = "/skillbridge"
	}
	http.Redirect(w, r, back+"#"+key, http.StatusSeeOther)
}
