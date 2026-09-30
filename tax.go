package main

// Income tax on retired pay, by where you retire.
//
// Federal: the 2026 brackets and standard deduction from IRS Rev. Proc.
// 2025-32 (IR-2025-103). Retired pay is taxable; the SBP premium comes out
// before tax, and so does any retired pay given up to the VA waiver. VA
// compensation is never taxed (38 U.S.C. 5301), federal or state.
//
// State: each state's 2026 brackets, standard deduction, and personal
// exemption come from Tax Foundation's table (statetax_data.go,
// scripts/statetax). How each state treats military retired pay comes from
// the Army's Soldier for Life newsletters for tax year 2025 and 2026 (Change
// of Mission, January 2026; Army Echoes, February 2026). States change these
// rules often, so each one names its rule on the page.
//
// This taxes retired pay as your only income, which is what DFAS withholds
// from. A civilian job adds to it and raises the rate; ARW does not model
// that yet. It also leaves out local income taxes and credits other than the
// personal exemption.

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type bracket struct {
	Over int64   // dollars a year; the rate applies above this
	Rate float64 // 0.05 = 5%
}

type stateTax struct {
	NoTax         bool
	Single, Joint []bracket
	StdS, StdJ    int64 // standard deduction, dollars
	ExS, ExJ      int64 // personal exemption taken as a deduction
	CrS, CrJ      int64 // personal exemption or deduction given as a credit
}

const fedTaxYear = 2026

var (
	fedSingle = []bracket{{0, 0.10}, {12400, 0.12}, {50400, 0.22}, {105700, 0.24}, {201775, 0.32}, {256225, 0.35}, {640600, 0.37}}
	fedJoint  = []bracket{{0, 0.10}, {24800, 0.12}, {100800, 0.22}, {211400, 0.24}, {403550, 0.32}, {512450, 0.35}, {768700, 0.37}}
)

const fedStdSingle, fedStdJoint = 16100, 32200

// bracketTax is the tax on a year's taxable income, in dollars.
func bracketTax(taxable float64, bs []bracket) float64 {
	var tax float64
	for i, b := range bs {
		if taxable <= float64(b.Over) {
			break
		}
		top := taxable
		if i+1 < len(bs) && float64(bs[i+1].Over) < top {
			top = float64(bs[i+1].Over)
		}
		tax += (top - float64(b.Over)) * b.Rate
	}
	return tax
}

// milRule is how a state treats military retired pay.
type milRule struct {
	Kind string // none | exempt | partial | taxed
	Note string
	// exclude returns the dollars of a year's retired pay the state leaves
	// untaxed, for partial states. agi is retired pay plus job income.
	exclude func(pay, agi float64, age int, joint bool) float64
}

func flatExclusion(n float64) func(float64, float64, int, bool) float64 {
	return func(pay, _ float64, _ int, _ bool) float64 { return min(n, pay) }
}

var milRules = func() map[string]milRule {
	m := map[string]milRule{}
	for _, s := range strings.Fields("AK FL NV NH SD TN TX WA WY") {
		m[s] = milRule{Kind: "none", Note: "No state income tax on wages or retired pay."}
	}
	for _, s := range strings.Fields("AL AZ AR CT HI IL IN IA KS LA ME MA MI MN MS MO NE NJ NY NC ND OH OK PA RI SC WV WI") {
		m[s] = milRule{Kind: "exempt", Note: "Military retired pay is fully exempt from state income tax."}
	}
	m["CA"] = milRule{Kind: "partial", Note: "California subtracts up to $20,000 when your income is $125,000 or less ($250,000 married), tax years 2025 to 2029.",
		exclude: func(pay, agi float64, _ int, joint bool) float64 {
			if limit := map[bool]float64{false: 125000, true: 250000}[joint]; agi > limit {
				return 0
			}
			return min(20000, pay)
		}}
	m["CO"] = milRule{Kind: "partial", Note: "Colorado subtracts up to $15,000 under age 55, $20,000 at 55 to 64, and $24,000 at 65 or older.",
		exclude: func(pay, _ float64, age int, _ bool) float64 {
			switch {
			case age >= 65:
				return min(24000, pay)
			case age >= 55:
				return min(20000, pay)
			}
			return min(15000, pay)
		}}
	m["DE"] = milRule{Kind: "partial", Note: "Delaware excludes up to $12,500.", exclude: flatExclusion(12500)}
	m["GA"] = milRule{Kind: "partial", Note: "Georgia excludes up to $65,000 at any age, starting with tax year 2026.", exclude: flatExclusion(65000)}
	m["ID"] = milRule{Kind: "partial", Note: "Idaho's retirement deduction (up to $40,140, or $60,210 married) starts at age 65, or 62 if you are disabled. Younger retirees pay the full rate.",
		exclude: func(pay, _ float64, age int, joint bool) float64 {
			if age < 65 {
				return 0
			}
			return min(map[bool]float64{false: 40140, true: 60210}[joint], pay)
		}}
	m["KY"] = milRule{Kind: "partial", Note: "Kentucky excludes up to $31,110. Pay for service before 1998 is fully exempt, which this does not count.", exclude: flatExclusion(31110)}
	m["MD"] = milRule{Kind: "partial", Note: "Maryland excludes up to $12,500, or $20,000 at 55 or older. County income tax (about 2.25% to 3.3%) is extra and not included.",
		exclude: func(pay, _ float64, age int, _ bool) float64 {
			if age >= 55 {
				return min(20000, pay)
			}
			return min(12500, pay)
		}}
	m["MT"] = milRule{Kind: "partial", Note: "Montana excludes half of military retired pay for your first 5 years as a resident. After that it is fully taxed.",
		exclude: func(pay, _ float64, _ int, _ bool) float64 { return pay / 2 }}
	m["NM"] = milRule{Kind: "partial", Note: "New Mexico excludes up to $30,000.", exclude: flatExclusion(30000)}
	m["OR"] = milRule{Kind: "partial", Note: "Oregon exempts only the share earned for service before October 1, 1991. This taxes all of it.",
		exclude: func(float64, float64, int, bool) float64 { return 0 }}
	m["UT"] = milRule{Kind: "exempt", Note: "Utah's military retirement credit cancels the state tax on retired pay."}
	m["VT"] = milRule{Kind: "partial", Note: "Vermont exempts all of it when your income is $125,000 or less, part of it up to $175,000, and none above.",
		exclude: func(pay, agi float64, _ int, _ bool) float64 {
			switch {
			case agi <= 125000:
				return pay
			case agi >= 175000:
				return 0
			}
			return pay * (175000 - agi) / 50000
		}}
	m["VA"] = milRule{Kind: "partial", Note: "Virginia subtracts up to $40,000 at any age.", exclude: flatExclusion(40000)}
	m["DC"] = milRule{Kind: "taxed", Note: "The District of Columbia taxes military retired pay in full."}
	return m
}()

var stateNames = map[string]string{"AL": "Alabama", "AK": "Alaska", "AZ": "Arizona", "AR": "Arkansas", "CA": "California",
	"CO": "Colorado", "CT": "Connecticut", "DE": "Delaware", "DC": "District of Columbia", "FL": "Florida", "GA": "Georgia",
	"HI": "Hawaii", "ID": "Idaho", "IL": "Illinois", "IN": "Indiana", "IA": "Iowa", "KS": "Kansas", "KY": "Kentucky",
	"LA": "Louisiana", "ME": "Maine", "MD": "Maryland", "MA": "Massachusetts", "MI": "Michigan", "MN": "Minnesota",
	"MS": "Mississippi", "MO": "Missouri", "MT": "Montana", "NE": "Nebraska", "NV": "Nevada", "NH": "New Hampshire",
	"NJ": "New Jersey", "NM": "New Mexico", "NY": "New York", "NC": "North Carolina", "ND": "North Dakota", "OH": "Ohio",
	"OK": "Oklahoma", "OR": "Oregon", "PA": "Pennsylvania", "RI": "Rhode Island", "SC": "South Carolina",
	"SD": "South Dakota", "TN": "Tennessee", "TX": "Texas", "UT": "Utah", "VT": "Vermont", "VA": "Virginia",
	"WA": "Washington", "WV": "West Virginia", "WI": "Wisconsin", "WY": "Wyoming"}

type stateOption struct{ Code, Name string }

// stateOptions lists every state by name, for the picker.
func stateOptions() []stateOption {
	out := make([]stateOption, 0, len(stateNames))
	for c, n := range stateNames {
		out = append(out, stateOption{c, n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// zip3 maps the first three digits of a ZIP code to its state. Ranges are
// inclusive and checked in order, so the few exceptions come first.
var zip3 = []struct {
	lo, hi int
	st     string
}{
	{201, 201, "VA"}, {200, 205, "DC"}, {569, 569, "DC"}, {885, 885, "TX"},
	{5, 5, "NY"}, {10, 27, "MA"}, {28, 29, "RI"}, {30, 38, "NH"}, {39, 49, "ME"}, {50, 59, "VT"},
	{60, 69, "CT"}, {70, 89, "NJ"}, {100, 149, "NY"}, {150, 196, "PA"}, {197, 199, "DE"}, {206, 219, "MD"},
	{220, 246, "VA"}, {247, 268, "WV"}, {270, 289, "NC"}, {290, 299, "SC"}, {300, 319, "GA"}, {398, 399, "GA"},
	{320, 349, "FL"}, {350, 369, "AL"}, {370, 385, "TN"}, {386, 397, "MS"}, {400, 427, "KY"}, {430, 459, "OH"},
	{460, 479, "IN"}, {480, 499, "MI"}, {500, 528, "IA"}, {530, 549, "WI"}, {550, 567, "MN"}, {570, 577, "SD"},
	{580, 588, "ND"}, {590, 599, "MT"}, {600, 629, "IL"}, {630, 658, "MO"}, {660, 679, "KS"}, {680, 693, "NE"},
	{700, 715, "LA"}, {716, 729, "AR"}, {730, 749, "OK"}, {750, 799, "TX"}, {800, 816, "CO"}, {820, 831, "WY"},
	{832, 838, "ID"}, {840, 847, "UT"}, {850, 865, "AZ"}, {870, 884, "NM"}, {889, 898, "NV"}, {900, 961, "CA"},
	{967, 968, "HI"}, {970, 979, "OR"}, {980, 994, "WA"}, {995, 999, "AK"},
}

var zipRE = regexp.MustCompile(`\b(\d{5})(?:-\d{4})?\b`)

// stateFromPlace guesses the state in a place such as "80920",
// "Colorado Springs, CO", or "Huntsville, Alabama". It returns "" when it
// cannot tell.
func stateFromPlace(q string) string {
	q = strings.TrimSpace(q)
	if m := zipRE.FindStringSubmatch(q); m != nil {
		n, _ := strconv.Atoi(m[1][:3])
		for _, r := range zip3 {
			if n >= r.lo && n <= r.hi {
				return r.st
			}
		}
		return ""
	}
	i := strings.LastIndex(q, ",")
	if i < 0 {
		return ""
	}
	tail := strings.TrimSpace(q[i+1:])
	if _, ok := stateNames[strings.ToUpper(tail)]; ok && len(tail) == 2 {
		return strings.ToUpper(tail)
	}
	for c, n := range stateNames {
		if strings.EqualFold(n, tail) {
			return c
		}
	}
	return ""
}

// taxState is the state to tax retired pay in: the one picked, or else a
// guess from where you plan to retire.
func (st State) taxState() (code string, guessed bool) {
	if s := st.Settings.TaxState; s != "" {
		return s, false
	}
	if s := stateFromPlace(st.Settings.WeatherRetire); s != "" {
		return s, true
	}
	if st.Housing != nil {
		if s := stateFromPlace(st.Housing.Zip); s != "" {
			return s, true
		}
	}
	return "", false
}

// Payroll tax on job wages (retired pay pays neither). The Social Security
// wage base is SSA's 2026 figure; the 0.9% Additional Medicare Tax
// thresholds are fixed by statute.
const (
	ssRate, ssWageBase2026 = 0.062, 184500
	medicareRate           = 0.0145
	addlMedicareRate       = 0.009
)

func fica(wages float64, joint bool) float64 {
	t := min(wages, ssWageBase2026)*ssRate + wages*medicareRate
	if over := map[bool]float64{false: 200000, true: 250000}[joint]; wages > over {
		t += (wages - over) * addlMedicareRate
	}
	return t
}

type retireTax struct {
	Taxable   int64 // retired pay subject to federal tax, cents a month
	Wages     int64 // civilian job pay, cents a month
	FICA      int64 // Social Security and Medicare on the job, cents a month
	Federal   int64 // cents a month
	FedRate   string
	Joint     bool
	State     string // code, "" when unknown
	StateName string
	Guessed   bool // state inferred from the retirement location
	StateTax  int64
	StateRate string
	Rule      milRule
	Age       int
	Total     int64 // income and payroll tax, cents a month
	AfterTax  int64 // retired pay and job pay after tax, plus VA pay, cents a month
	Itemized  itemization
}

func pct(tax, income float64) string {
	if income <= 0 {
		return "0%"
	}
	return strconv.FormatFloat(tax/income*100, 'f', 1, 64) + "%"
}

func cents(dollarsAYear float64) int64 { return int64(dollarsAYear/12*100 + 0.5) }

// estimateRetireTax works out a year's tax on the taxable part of retired
// pay plus any civilian job, and spreads it over 12 months.
func estimateRetireTax(st State, p retirePay) retireTax {
	s := st.Settings
	t := retireTax{Joint: s.RetSpouse, Taxable: max(p.Net-p.Offset, 0), Wages: (s.CivSalary + 6) / 12}
	if rd, err := parseDay(s.RetirementDate); err == nil && s.BirthYear > 0 {
		t.Age = rd.Year() - s.BirthYear
	}
	pay := float64(t.Taxable) * 12 / 100 // retired pay, dollars a year
	wages := float64(s.CivSalary) / 100  // job, dollars a year
	income := pay + wages

	t.FICA = cents(fica(wages, t.Joint))

	t.State, t.Guessed = st.taxState()
	t.StateName = stateNames[t.State]
	t.Rule = milRules[t.State]
	var stax float64
	if rule, ok := milRules[t.State]; ok && !stateTaxes[t.State].NoTax {
		// Every state that taxes wages taxes the job; the rule decides how
		// much of the retired pay it also taxes.
		excl := 0.0
		switch {
		case rule.Kind == "exempt":
			excl = pay
		case rule.exclude != nil:
			excl = rule.exclude(pay, income, t.Age, t.Joint)
		}
		table := stateTaxes[t.State]
		std, ex, cr, bs := table.StdS, table.ExS, table.CrS, table.Single
		if t.Joint {
			std, ex, cr, bs = table.StdJ, table.ExJ, table.CrJ, table.Joint
		}
		stax = max(bracketTax(max(income-excl-float64(std+ex), 0), bs)-float64(cr), 0)
	}
	t.StateTax, t.StateRate = cents(stax), pct(stax, income)

	// Federal: the standard deduction, or itemized when that is larger.
	fstd, fb := float64(fedStdSingle), fedSingle
	if t.Joint {
		fstd, fb = fedStdJoint, fedJoint
	}
	rate, _ := st.homeRate()
	t.Itemized = itemize(s, rate, income, stax, fstd)
	withStd := bracketTax(max(income-fstd, 0), fb)
	fed := withStd
	if t.Itemized.Better {
		fed = bracketTax(max(income-float64(t.Itemized.Total), 0), fb)
		t.Itemized.Saves = int64(withStd - fed + 0.5)
	}
	t.Federal, t.FedRate = cents(fed), pct(fed, income)
	t.Total = t.Federal + t.StateTax + t.FICA
	t.AfterTax = p.Total + t.Wages - t.Total
	return t
}
