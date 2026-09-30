package main

// Retired pay estimate: what lands each month after you retire.
//
//   - Pension: years of service x 2.5% (High-3) or 2.0% (Blended Retirement
//     System), times the average of your highest 36 months of base pay.
//   - SBP: full spouse coverage costs 6.5% of the covered amount.
//   - VA compensation is tax-free, and comes from VA's rate table for your
//     estimated rating and dependents (varates.go). At a combined rating of 50% or more,
//     Concurrent Retirement and Disability Pay (CRDP) pays both in full.
//     Below 50%, VA pay replaces an equal amount of retired pay (the VA
//     waiver), so the total is the larger of the two, not the sum. Combat-
//     Related Special Compensation can change that; ask your finance office.
//
// This is a planning estimate before taxes, not a DFAS statement.

import (
	"net/http"
	"strconv"
	"strings"
)

type retirePay struct {
	Set        bool
	System     string // "High-3" or "BRS"
	Years      string
	Multiplier string // "50.0%"
	Gross      int64  // monthly retired pay before SBP
	SBP        int64
	Net        int64 // retired pay after SBP, before tax
	VA         int64
	Rating     int
	RatesAsOf  string // VA rate table effective date
	High3      high3Calc
	CRDP       bool  // rating >= 50: both paid in full
	Offset     int64 // retired pay given up to the VA waiver
	Total      int64 // what arrives each month
	Change     int64 // after-tax total minus current take-home
	Tax        retireTax
	HaveIncome bool
}

// raise returns the assumed yearly raise for unpublished pay years.
func (s Settings) raise() float64 {
	if s.PayRaise > 0 {
		return s.PayRaise
	}
	return defaultRaise
}

// high3 prefers the calculated High-3 from grade and years of service, and
// falls back to the amount typed in by hand.
func (st State) high3() (int64, high3Calc) {
	s := st.Settings
	rd, _ := parseDay(s.RetirementDate)
	h := calcHigh3(s.PayGrade, s.RetYears, rd, s.raise())
	if h.OK {
		return h.Amount, h
	}
	return s.RetHigh3, h
}

func estimateRetirePay(st State) retirePay {
	s := st.Settings
	high3, calc := st.high3()
	if s.RetYears <= 0 || high3 <= 0 {
		// No pension inputs yet, but VA pay can still be shown from the rating.
		return retirePay{Rating: roundRating(s.VaEstimate), RatesAsOf: vaRatesEffective, High3: calc,
			VA: vaMonthly(s.VaEstimate, vaDependents{Spouse: s.RetSpouse, Children: s.RetKids, SchoolKids: s.RetSchoolKids, Parents: s.RetParents})}
	}
	rate := 0.025
	sys := "High-3"
	if s.RetSystem == "brs" {
		rate, sys = 0.020, "BRS"
	}
	mult := rate * s.RetYears
	if mult > 1 {
		mult = 1 // the multiplier caps at 100% of High-3
	}
	p := retirePay{Set: true, System: sys, Years: fmtDays(s.RetYears),
		Multiplier: strconv.FormatFloat(mult*100, 'f', 1, 64) + "%",
		Gross:      int64(float64(high3)*mult + 0.5), Rating: roundRating(s.VaEstimate), High3: calc,
		VA: vaMonthly(s.VaEstimate, vaDependents{Spouse: s.RetSpouse, Children: s.RetKids, SchoolKids: s.RetSchoolKids, Parents: s.RetParents})}
	if s.RetSBP {
		p.SBP = int64(float64(p.Gross)*0.065 + 0.5)
	}
	p.RatesAsOf = vaRatesEffective
	p.Net = p.Gross - p.SBP
	p.CRDP = p.Rating >= 50
	if p.CRDP {
		p.Total = p.Net + p.VA
	} else {
		p.Offset = min(p.VA, p.Net)
		p.Total = p.Net - p.Offset + p.VA
	}
	p.Tax = estimateRetireTax(st, p)
	if s.MonthlyIncome > 0 {
		// Take-home is after tax, so compare it with retired pay after tax.
		p.HaveIncome = true
		p.Change = p.Tax.AfterTax - s.MonthlyIncome
	}
	return p
}

func (s *Server) retirePaySave(w http.ResponseWriter, r *http.Request) {
	years, yErr := strconv.ParseFloat(strings.TrimSpace(r.FormValue("years")), 64)
	high3, hOK := optionalMoney(r.FormValue("high3")) // blank is fine when ARW calculates it
	count := func(k string, hi int) int {
		n, _ := strconv.Atoi(strings.TrimSpace(r.FormValue(k)))
		return min(max(n, 0), hi)
	}
	switch {
	case yErr != nil || years < 0 || years > 45:
		flash(w, "err", "Years of service must be a number like 20 or 22.5.")
	case !hOK:
		flash(w, "err", "High-3 must be a dollar amount, like 6,450.00.")
	case !validTaxState(r.FormValue("tax_state")):
		flash(w, "err", "Pick a state from the list.")
	case !validBirthYear(r.FormValue("birth_year")):
		flash(w, "err", "Birth year must be four digits, like 1985.")
	default:
		_ = s.store.mutate(func(st *State) {
			st.Settings.RetYears, st.Settings.RetHigh3 = years, high3
			st.Settings.RetSpouse = r.FormValue("spouse") == "1"
			st.Settings.RetKids, st.Settings.RetSchoolKids, st.Settings.RetParents = count("kids", 20), count("school_kids", 20), count("parents", 2)
			st.Settings.RetSystem = map[bool]string{true: "brs", false: "high3"}[r.FormValue("system") == "brs"]
			st.Settings.RetSBP = r.FormValue("sbp") == "1"
			st.Settings.TaxState = strings.TrimSpace(r.FormValue("tax_state"))
			st.Settings.BirthYear, _ = strconv.Atoi(strings.TrimSpace(r.FormValue("birth_year")))
		})
		flash(w, "ok", "Retired pay estimate updated.")
	}
	http.Redirect(w, r, "/retired-pay", http.StatusSeeOther)
}

func validTaxState(s string) bool {
	_, ok := stateNames[strings.TrimSpace(s)]
	return ok || strings.TrimSpace(s) == ""
}

func validBirthYear(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}
	n, err := strconv.Atoi(s)
	return err == nil && n >= 1930 && n <= 2010
}
