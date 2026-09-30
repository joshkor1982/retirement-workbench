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
	Health     healthCost
	TakeHome   int64 // after tax and health coverage, cents a month
	Home       homePlan
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
		// No pension inputs yet, but VA pay and health costs can still be
		// shown from the rating and family.
		p := retirePay{Rating: roundRating(s.VaEstimate), RatesAsOf: vaRatesEffective, High3: calc,
			VA: vaMonthly(s.VaEstimate, vaDependents{Spouse: s.RetSpouse, Children: s.RetKids, SchoolKids: s.RetSchoolKids, Parents: s.RetParents})}
		p.Total = p.VA
		p.Tax = estimateRetireTax(st, p)
		p.Health = estimateHealth(s)
		p.TakeHome = p.Tax.AfterTax - p.Health.Total
		return p
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
	p.Health = estimateHealth(s)
	p.TakeHome = p.Tax.AfterTax - p.Health.Total
	p.Home = estimateHomePlan(st, p.TakeHome)
	if s.MonthlyIncome > 0 {
		// Today's take-home is after tax, and TRICARE is free on active
		// duty, so compare it with take-home after tax and health coverage.
		p.HaveIncome = true
		p.Change = p.TakeHome - s.MonthlyIncome
	}
	return p
}

func (s *Server) retirePaySave(w http.ResponseWriter, r *http.Request) {
	years, yErr := strconv.ParseFloat(strings.TrimSpace(r.FormValue("years")), 64)
	high3, hOK := optionalMoney(r.FormValue("high3")) // blank is fine when ARW calculates it
	salary, sOK := optionalMoney(r.FormValue("civ_salary"))
	other, oOK := optionalMoney(r.FormValue("health_other"))
	price, prOK := optionalMoney(r.FormValue("home_price"))
	down, dnOK := optionalMoney(r.FormValue("home_down"))
	homeIns, hiOK := optionalMoney(r.FormValue("home_ins"))
	hoa, hoOK := optionalMoney(r.FormValue("hoa"))
	propTax, ptOK := optionalMoney(r.FormValue("prop_tax"))
	rvLoan, rlOK := optionalMoney(r.FormValue("rv_loan"))
	rvSales, rsOK := optionalMoney(r.FormValue("rv_sales_tax"))
	charity, chOK := optionalMoney(r.FormValue("charity"))
	homeRate, hrOK := optionalRate(r.FormValue("home_rate"))
	rvRate, rrOK := optionalRate(r.FormValue("rv_rate"))
	rvYears, ryErr := strconv.Atoi(cmpOr(field(r, "rv_years"), "15"))
	count := func(k string, hi int) int {
		n, _ := strconv.Atoi(strings.TrimSpace(r.FormValue(k)))
		return min(max(n, 0), hi)
	}
	switch {
	case yErr != nil || years < 0 || years > 45:
		flash(w, "err", "Years of service must be a number like 20 or 22.5.")
	case !hOK:
		flash(w, "err", "High-3 must be a dollar amount, like 6,450.00.")
	case !sOK || salary < 0:
		flash(w, "err", "Civilian salary must be a dollar amount a year, like 85,000.")
	case !prOK || !dnOK || !hiOK || !hoOK || !ptOK || !rlOK || !rsOK || !chOK || price < 0 || down < 0 || homeIns < 0 || hoa < 0 || propTax < 0 || rvLoan < 0 || rvSales < 0 || charity < 0:
		flash(w, "err", "Loan, tax, and gift amounts must be dollar amounts, like 350,000.")
	case price > 0 && down >= price:
		flash(w, "err", "The down payment has to be less than the home price.")
	case !hrOK || !rrOK:
		flash(w, "err", "Interest rates must be percentages, like 6.25.")
	case ryErr != nil || rvYears < 1 || rvYears > 30:
		flash(w, "err", "The RV loan term must be 1 to 30 years.")
	case !oOK || other < 0:
		flash(w, "err", "Other health cost must be a dollar amount a month, like 150.")
	case !validHealthPlan(r.FormValue("health_plan")):
		flash(w, "err", "Pick a health plan from the list.")
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
			st.Settings.CivSalary = salary
			st.Settings.HealthPlan, st.Settings.HealthOther = r.FormValue("health_plan"), other
			st.Settings.NoDental = r.FormValue("dental") != "1"
			st.Settings.OwnHome, st.Settings.HomeRate, st.Settings.PropTax = r.FormValue("own_home") == "1", homeRate, propTax
			st.Settings.HomePrice, st.Settings.HomeDown, st.Settings.HomeIns, st.Settings.HOA = price, down, homeIns, hoa
			st.Settings.OwnRV, st.Settings.RVIsHome = r.FormValue("own_rv") == "1", r.FormValue("rv_is_home") == "1"
			st.Settings.RVLoan, st.Settings.RVRate, st.Settings.RVYears, st.Settings.RVSalesTax = rvLoan, rvRate, rvYears, rvSales
			st.Settings.Charity = charity
			st.Settings.NoVision = r.FormValue("vision") != "1"
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

// optionalRate reads a percentage like "6.25" or "6.25%"; blank is 0.
func optionalRate(v string) (float64, bool) {
	v = strings.TrimSuffix(strings.TrimSpace(v), "%")
	if v == "" {
		return 0, true
	}
	f, err := strconv.ParseFloat(v, 64)
	return f, err == nil && f >= 0 && f <= 30
}
