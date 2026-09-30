package main

// Itemized deductions for owning a home, and optionally an RV, checked
// against the standard deduction. A purchase is not a write-off; what can be
// deducted is the interest on the loans and some taxes, and only when their
// total beats the standard deduction.
//
//   - Mortgage interest on up to $750,000 of loans that bought a main home
//     and one second home (IRS Publication 936; the limit is permanent under
//     the 2025 tax law). An RV counts as a second home when it has sleeping,
//     cooking, and toilet facilities and secures its loan.
//   - State and local taxes: property tax plus the larger of state income tax
//     or sales tax (the sales tax on an RV counts), capped at $40,400 for
//     2026, and cut by 30% of income over $505,000, never below $10,000.
//   - Charitable gifts, as entered.
//
// Interest is the first year's, from a standard amortizing loan; it shrinks
// every year after, so itemizing tends to help most the year you buy.

import (
	"math"
	"strconv"
	"strings"
)

const (
	mortgageLimit  = 750000
	saltCap2026    = 40400
	saltPhaseStart = 505000
	saltFloor      = 10000
)

type itemization struct {
	Home, RV       bool
	HomeInterest   int64 // dollars, first year
	RVInterest     int64
	InterestCapped bool  // loans over $750,000: interest prorated
	SALT           int64 // after the cap
	SALTUsesSales  bool  // sales tax beat state income tax
	SALTCapped     bool
	PropTax        int64
	Charity        int64
	Total          int64 // itemized, dollars
	Standard       int64
	Better         bool  // itemizing beats the standard deduction
	Saves          int64 // federal tax saved a year, dollars
	RVNotHome      bool  // RV entered without sleep, cook, and toilet: interest not counted
}

// firstYearInterest is the interest paid in the first 12 payments of a
// fixed-rate amortizing loan.
func firstYearInterest(loan, ratePct float64, years int) float64 {
	if loan <= 0 || ratePct <= 0 || years <= 0 {
		return 0
	}
	r := ratePct / 100 / 12
	n := float64(years * 12)
	pay := loan * r / (1 - math.Pow(1+r, -n))
	bal, total := loan, 0.0
	for i := 0; i < 12 && bal > 0; i++ {
		in := bal * r
		total += in
		bal -= pay - in
	}
	return total
}

// saltCap is the 2026 state and local tax cap after the high-income cut.
func saltCap(income float64) float64 {
	c := math.Round(saltCap2026 - 0.3*max(income-saltPhaseStart, 0))
	return max(c, saltFloor)
}

func itemize(s Settings, homeRate, income, stateIncomeTax, standard float64) itemization {
	it := itemization{Home: s.OwnHome, RV: s.OwnRV, Standard: int64(standard)}
	var home, rv, loans float64
	if s.OwnHome {
		l, _ := s.homeLoan()
		loan := float64(l) / 100
		home = firstYearInterest(loan, homeRate, 30)
		loans += loan
		it.PropTax = s.PropTax / 100
	}
	if s.OwnRV {
		if s.RVIsHome {
			loan := float64(s.RVLoan) / 100
			rv = firstYearInterest(loan, s.RVRate, max(s.RVYears, 1))
			loans += loan
		} else {
			it.RVNotHome = true
		}
	}
	if loans > mortgageLimit {
		f := mortgageLimit / loans
		home, rv = home*f, rv*f
		it.InterestCapped = true
	}
	it.HomeInterest, it.RVInterest = int64(home+0.5), int64(rv+0.5)

	sales := 0.0
	if s.OwnRV {
		sales = float64(s.RVSalesTax) / 100
	}
	general := stateIncomeTax
	if sales > stateIncomeTax {
		general, it.SALTUsesSales = sales, true
	}
	salt := general + float64(it.PropTax)
	if c := saltCap(income); salt > c {
		salt, it.SALTCapped = c, true
	}
	it.SALT = int64(salt + 0.5)
	it.Charity = s.Charity / 100
	it.Total = it.HomeInterest + it.RVInterest + it.SALT + it.Charity
	it.Better = (s.OwnHome || s.OwnRV || s.Charity > 0) && float64(it.Total) > standard
	return it
}

// ---------- The cost of owning ----------------------------------------------

// homePlan is what the house (and RV) cost each month, what is left of
// take-home after them, and what is left in savings after closing. It draws
// on numbers entered elsewhere: price and down payment from the funding fee
// estimate (Agents & Lenders), the lowest quoted rate and closing costs from
// the Lenders tracker, and the Savings balance.
type homePlan struct {
	Set                   bool
	Price, Down, Loan     int64 // cents
	Fee                   int64 // funding fee financed into the loan, cents
	Rate                  float64
	RateFrom              string // "yours" | the lender's name
	PI, Tax, Ins, HOA     int64  // cents a month
	RVPay                 int64
	Total                 int64 // housing a month
	Left                  int64 // take-home after housing, a month
	Closing               int64 // closing costs, cents
	ClosingFrom           string
	Cash                  int64 // down payment plus closing costs
	Savings, SavingsAfter int64
}

// payment is the monthly principal and interest on a fixed-rate loan.
func payment(loanCents int64, ratePct float64, years int) int64 {
	if loanCents <= 0 || years <= 0 {
		return 0
	}
	n := float64(years * 12)
	if ratePct <= 0 {
		return int64(float64(loanCents)/n + 0.5)
	}
	r := ratePct / 100 / 12
	return int64(float64(loanCents)*r/(1-math.Pow(1+r, -n)) + 0.5)
}

func lenderRate(l Lender) float64 {
	f, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(l.Rate), "%"), 64)
	if err != nil {
		return 0
	}
	return f
}

// homeRate is the rate you entered, else the lowest rate a lender quoted.
func (st State) homeRate() (float64, string) {
	if st.Settings.HomeRate > 0 {
		return st.Settings.HomeRate, "yours"
	}
	best, from := 0.0, ""
	for _, l := range st.Lenders {
		if r := lenderRate(l); r > 0 && (best == 0 || r < best) {
			best, from = r, l.Name
		}
	}
	return best, from
}

// homeLoan is the price minus the down payment, plus the VA funding fee when
// it applies (most borrowers finance it).
func (s Settings) homeLoan() (loan, fee int64) {
	if s.HomePrice <= 0 || s.HomeDown >= s.HomePrice {
		return 0, 0
	}
	f := estimateFee(s)
	if !f.Exempt {
		fee = f.Fee
	}
	return s.HomePrice - s.HomeDown + fee, fee
}

func estimateHomePlan(st State, takeHome int64) homePlan {
	s := st.Settings
	h := homePlan{Price: s.HomePrice, Down: s.HomeDown}
	if !s.OwnHome && !s.OwnRV {
		return h
	}
	if s.OwnHome {
		h.Loan, h.Fee = s.homeLoan()
		h.Rate, h.RateFrom = st.homeRate()
		h.PI = payment(h.Loan, h.Rate, 30)
		h.Tax, h.Ins, h.HOA = (s.PropTax+6)/12, (s.HomeIns+6)/12, s.HOA
		// Closing costs: the lowest quote's sections A plus B.
		for _, l := range st.Lenders {
			if l.Costs > 0 && (h.Closing == 0 || l.Costs < h.Closing) {
				h.Closing, h.ClosingFrom = l.Costs, l.Name
			}
		}
		h.Cash = h.Down + h.Closing
	}
	if s.OwnRV {
		h.RVPay = payment(s.RVLoan, s.RVRate, max(s.RVYears, 1))
	}
	h.Total = h.PI + h.Tax + h.Ins + h.HOA + h.RVPay
	h.Left = takeHome - h.Total
	h.Savings = summarizeSavings(st).Saved
	h.SavingsAfter = h.Savings - h.Cash
	h.Set = h.Total > 0
	return h
}
