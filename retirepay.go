package main

// Retired pay estimate: what lands each month after you retire.
//
//   - Pension: years of service x 2.5% (High-3) or 2.0% (Blended Retirement
//     System), times the average of your highest 36 months of base pay.
//   - SBP: full spouse coverage costs 6.5% of the covered amount.
//   - VA compensation is tax-free. At a combined rating of 50% or more,
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
	CRDP       bool  // rating >= 50: both paid in full
	Offset     int64 // retired pay given up to the VA waiver
	Total      int64 // what arrives each month
	Change     int64 // Total minus current take-home
	HaveIncome bool
}

func estimateRetirePay(st State) retirePay {
	s := st.Settings
	if s.RetYears <= 0 || s.RetHigh3 <= 0 {
		return retirePay{}
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
		Gross:      int64(float64(s.RetHigh3)*mult + 0.5), VA: s.RetVAComp, Rating: s.VaEstimate}
	if s.RetSBP {
		p.SBP = int64(float64(p.Gross)*0.065 + 0.5)
	}
	p.Net = p.Gross - p.SBP
	p.CRDP = p.Rating >= 50
	if p.CRDP {
		p.Total = p.Net + p.VA
	} else {
		p.Offset = min(p.VA, p.Net)
		p.Total = p.Net - p.Offset + p.VA
	}
	if s.MonthlyIncome > 0 {
		p.HaveIncome = true
		p.Change = p.Total - s.MonthlyIncome
	}
	return p
}

func (s *Server) retirePaySave(w http.ResponseWriter, r *http.Request) {
	years, yErr := strconv.ParseFloat(strings.TrimSpace(r.FormValue("years")), 64)
	high3, hOK := optionalMoney(r.FormValue("high3"))
	va, vOK := optionalMoney(r.FormValue("va_comp"))
	switch {
	case yErr != nil || years < 0 || years > 45:
		flash(w, "err", "Years of service must be a number like 20 or 22.5.")
	case !hOK || !vOK:
		flash(w, "err", "High-3 and VA pay must be dollar amounts, like 6,450.00.")
	default:
		_ = s.store.mutate(func(st *State) {
			st.Settings.RetYears, st.Settings.RetHigh3, st.Settings.RetVAComp = years, high3, va
			st.Settings.RetSystem = map[bool]string{true: "brs", false: "high3"}[r.FormValue("system") == "brs"]
			st.Settings.RetSBP = r.FormValue("sbp") == "1"
		})
		flash(w, "ok", "Retired pay estimate updated.")
	}
	http.Redirect(w, r, "/budget#retired-pay", http.StatusSeeOther)
}
