package main

// Agents & Lenders: the people who get you into the next house. No official
// list says which agents or lenders are "VA qualified": any VA-approved
// lender can make a VA loan, and the closest thing to a credential for an
// agent is NAR's Military Relocation Professional (MRP) certification. So
// this page links out to the searches that exist, keyed to your Housing ZIP
// code, and tracks the agents and lenders you actually talk to, side by side,
// with a VA funding fee estimate from VA's published table.

import (
	"cmp"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

type Agent struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Brokerage string `json:"brokerage"`
	Contact   string `json:"contact"` // phone, email
	URL       string `json:"url"`
	MRP       bool   `json:"mrp"`      // NAR Military Relocation Professional
	VADeals   string `json:"va_deals"` // what they said about VA buyers they have closed
	Status    string `json:"status"`   // found | contacted | interviewed | chosen
	Notes     string `json:"notes"`
}

type Lender struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"` // bank | credit union | mortgage company
	NMLS     string `json:"nmls"` // license ID, checkable on NMLS Consumer Access
	Contact  string `json:"contact"`
	Rate     string `json:"rate"` // from the Loan Estimate, e.g. "6.125"
	APR      string `json:"apr"`
	Costs    int64  `json:"costs_cents"` // Loan Estimate section A plus B, cents
	QuotedOn string `json:"quoted_on"`
	Status   string `json:"status"` // quoted | applied | chosen
	Notes    string `json:"notes"`
}

var (
	agentFlow  = []string{"found", "contacted", "interviewed", "chosen"}
	lenderFlow = []string{"quoted", "applied", "chosen"}
)

// fundingFeeRate is VA's funding fee for a purchase loan, in percent, from
// va.gov (checked 2026-09-30): first use 2.15/1.5/1.25, after first use
// 3.3/1.5/1.25, by down payment under 5%, 5% or more, 10% or more.
func fundingFeeRate(downPct float64, firstUse bool) float64 {
	switch {
	case downPct >= 10:
		return 1.25
	case downPct >= 5:
		return 1.5
	case firstUse:
		return 2.15
	}
	return 3.3
}

type feeEstimate struct {
	Price, Down, Loan, Fee int64 // cents
	Rate                   string
	DownPct                string
	FirstUse, Exempt, Set  bool
}

func estimateFee(s Settings) feeEstimate {
	f := feeEstimate{Price: s.HomePrice, Down: s.HomeDown, FirstUse: !s.VALoanUsed, Exempt: s.VaEstimate > 0}
	if f.Price <= 0 || f.Down < 0 || f.Down >= f.Price {
		return f
	}
	f.Set = true
	f.Loan = f.Price - f.Down
	pct := float64(f.Down) * 100 / float64(f.Price)
	r := fundingFeeRate(pct, f.FirstUse)
	f.Rate = strconv.FormatFloat(r, 'f', -1, 64) + "%"
	f.DownPct = strconv.FormatFloat(pct, 'f', 1, 64) + "%"
	f.Fee = int64(float64(f.Loan)*r/100 + 0.5)
	return f
}

type searchLink struct{ Label, Note, URL string }

// homeZip is the ZIP code to search around: Housing first, then the
// retirement location when it is a ZIP code.
func homeZip(st State) string {
	if st.Housing != nil && len(st.Housing.Zip) == 5 {
		return st.Housing.Zip
	}
	if m := zipRE.FindStringSubmatch(st.Settings.WeatherRetire); m != nil {
		return m[1]
	}
	return ""
}

func homeTeamLinks(zip string) (agents, lenders []searchLink) {
	agentSearch := "https://www.realtor.com/realestateagents/"
	if zip != "" {
		agentSearch += zip
	}
	agents = []searchLink{
		{"Realtor.com agent search", cmpOr(map[bool]string{true: "Agents near " + zip}[zip != ""], "By city or ZIP code"), agentSearch},
		{"Zillow agent finder", "Reviews and recent sales by agent", "https://www.zillow.com/professionals/real-estate-agent-reviews/"},
	}
	lenders = []searchLink{
		{"VA home loans", "Eligibility, how the loan works, and what VA guarantees", "https://www.va.gov/housing-assistance/home-loans/"},
		{"Request your COE", "The Certificate of Eligibility lenders ask for first", "https://www.va.gov/housing-assistance/home-loans/how-to-request-coe/"},
		{"VA funding fee and closing costs", "The fee table and who is exempt", "https://www.va.gov/housing-assistance/home-loans/funding-fee-and-closing-costs/"},
		{"VA lender statistics", "Which lenders make the most VA loans, by volume", "https://www.benefits.va.gov/HOMELOANS/lender_stats.asp"},
		{"NMLS Consumer Access", "Check a lender's or loan officer's license by NMLS ID", "https://www.nmlsconsumeraccess.org/"},
		{"CFPB: read a Loan Estimate", "Compare quotes line by line", "https://www.consumerfinance.gov/owning-a-home/loan-estimate/"},
	}
	return
}

func (s *Server) homeTeam(w http.ResponseWriter, r *http.Request) {
	st := s.store.snapshot()
	zip := homeZip(st)
	al, ll := homeTeamLinks(zip)
	lenders := append([]Lender(nil), st.Lenders...)
	// Lowest APR first; quotes without an APR go last.
	apr := func(l Lender) float64 {
		v, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(l.APR), "%"), 64)
		if err != nil {
			return 1e9
		}
		return v
	}
	slices.SortStableFunc(lenders, func(a, b Lender) int { return cmp.Compare(apr(a), apr(b)) })
	best := 0
	if len(lenders) > 0 && apr(lenders[0]) < 1e9 {
		best = lenders[0].ID
	}
	s.page(w, "hometeam", map[string]any{
		"Zip": zip, "AgentLinks": al, "LenderLinks": ll, "Agents": st.Agents, "Lenders": lenders, "BestAPR": best,
		"Fee": estimateFee(st.Settings), "AgentFlow": agentFlow, "LenderFlow": lenderFlow,
	})
}

func (s *Server) routeHomeTeam(mux *http.ServeMux) {
	mux.HandleFunc("GET /home-team", s.homeTeam)
	mux.HandleFunc("POST /home-team/agents", s.agentSave)
	mux.HandleFunc("POST /home-team/agents/{id}/update", s.agentSave)
	mux.HandleFunc("POST /home-team/agents/{id}/delete", s.agentDelete)
	mux.HandleFunc("POST /home-team/lenders", s.lenderSave)
	mux.HandleFunc("POST /home-team/lenders/{id}/update", s.lenderSave)
	mux.HandleFunc("POST /home-team/lenders/{id}/delete", s.lenderDelete)
	mux.HandleFunc("POST /home-team/fee", s.feeSave)
}

func toTeam(w http.ResponseWriter, r *http.Request, anchor string) {
	http.Redirect(w, r, "/home-team"+anchor, http.StatusSeeOther)
}

// agentSave adds an agent, or updates one when the path has an ID.
func (s *Server) agentSave(w http.ResponseWriter, r *http.Request) {
	name := field(r, "name")
	if name == "" && r.PathValue("id") == "" {
		flash(w, "err", "Give the agent a name.")
		toTeam(w, r, "#agents")
		return
	}
	fill := func(a *Agent) {
		keep(&a.Name, name)
		a.Brokerage, a.Contact, a.URL = field(r, "brokerage"), field(r, "contact"), webURL(field(r, "url"))
		a.MRP, a.VADeals, a.Notes = r.FormValue("mrp") == "1", field(r, "va_deals"), field(r, "notes")
		if v := field(r, "status"); slices.Contains(agentFlow, v) {
			a.Status = v
		}
	}
	_ = s.store.mutate(func(st *State) {
		if r.PathValue("id") == "" {
			a := Agent{ID: st.id(), Status: "found"}
			fill(&a)
			st.Agents = append(st.Agents, a)
			return
		}
		editByID(st.Agents, pathID(r), func(a Agent) int { return a.ID }, fill)
	})
	toTeam(w, r, "#agents")
}

func (s *Server) agentDelete(w http.ResponseWriter, r *http.Request) {
	_ = s.store.mutate(func(st *State) {
		st.Agents = deleteByID(st.Agents, pathID(r), func(a Agent) int { return a.ID })
	})
	toTeam(w, r, "#agents")
}

func (s *Server) lenderSave(w http.ResponseWriter, r *http.Request) {
	name := field(r, "name")
	costs, ok := optionalMoney(r.FormValue("costs"))
	switch {
	case name == "" && r.PathValue("id") == "":
		flash(w, "err", "Give the lender a name.")
		toTeam(w, r, "#lenders")
		return
	case !ok || costs < 0:
		flash(w, "err", "Closing costs must be a dollar amount, like 4,250.")
		toTeam(w, r, "#lenders")
		return
	}
	fill := func(l *Lender) {
		keep(&l.Name, name)
		l.Kind, l.NMLS, l.Contact = field(r, "kind"), field(r, "nmls"), field(r, "contact")
		l.Rate = strings.TrimSuffix(field(r, "rate"), "%")
		l.APR = strings.TrimSuffix(field(r, "apr"), "%")
		l.Costs, l.QuotedOn, l.Notes = costs, field(r, "quoted_on"), field(r, "notes")
		if v := field(r, "status"); slices.Contains(lenderFlow, v) {
			l.Status = v
		}
	}
	_ = s.store.mutate(func(st *State) {
		if r.PathValue("id") == "" {
			l := Lender{ID: st.id(), Status: "quoted"}
			fill(&l)
			st.Lenders = append(st.Lenders, l)
			return
		}
		editByID(st.Lenders, pathID(r), func(l Lender) int { return l.ID }, fill)
	})
	toTeam(w, r, "#lenders")
}

func (s *Server) lenderDelete(w http.ResponseWriter, r *http.Request) {
	_ = s.store.mutate(func(st *State) {
		st.Lenders = deleteByID(st.Lenders, pathID(r), func(l Lender) int { return l.ID })
	})
	toTeam(w, r, "#lenders")
}

func (s *Server) feeSave(w http.ResponseWriter, r *http.Request) {
	price, pOK := optionalMoney(r.FormValue("price"))
	down, dOK := optionalMoney(r.FormValue("down"))
	if !pOK || !dOK || price < 0 || down < 0 || (price > 0 && down >= price) {
		flash(w, "err", "Enter a price and a down payment smaller than the price, like 350,000 and 0.")
	} else {
		_ = s.store.mutate(func(st *State) {
			st.Settings.HomePrice, st.Settings.HomeDown = price, down
			st.Settings.VALoanUsed = r.FormValue("used") == "1"
		})
	}
	toTeam(w, r, "#fee")
}
