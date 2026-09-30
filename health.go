package main

// Health coverage after you retire: what comes out of take-home each month.
//
//   - TRICARE Prime and Select charge retirees a yearly enrollment fee, set
//     each calendar year. It depends on the plan, individual or family, and
//     your group: Group A if you first enlisted or were appointed before
//     January 1, 2018, Group B after. Figures: TRICARE 2026 Costs and Fees.
//   - TRICARE For Life (at 65) has no fee but requires Medicare Part B,
//     $202.90 a month each in 2026 (CMS), more at higher incomes.
//   - Dental and vision come from FEDVIP, bought through BENEFEDS. Premiums
//     vary by plan and ZIP code, so ARW uses the median 2026 premium across
//     every plan and region in OPM's rate files, and says so.
//
// These are paid after tax: DFAS takes TRICARE fees by allotment from retired
// pay but does not take them before tax. Copays, deductibles, and
// prescriptions are left out.

import "time"

const healthYear = 2026

// Yearly TRICARE enrollment fees for retirees, in cents: [individual, family].
var tricareFees = map[string][2]int64{
	"prime-A":  {38196, 76500},
	"select-A": {18696, 37500},
	"prime-B":  {46296, 92700},
	"select-B": {59496, 119100},
}

const partBMonthly = 20290 // Medicare Part B standard premium, cents a month

// Median FEDVIP monthly premiums in cents: [self, self plus one, family].
var (
	fedvipDental = [3]int64{3790, 7579, 11369}
	fedvipVision = [3]int64{1004, 2008, 3012}
)

var healthPlans = []struct{ Value, Label string }{
	{"prime", "TRICARE Prime"},
	{"select", "TRICARE Select"},
	{"tfl", "TRICARE For Life (65 or older)"},
	{"none", "No TRICARE (employer plan or VA care)"},
}

type healthCost struct {
	Plan      string // prime | select | tfl | none
	PlanLabel string
	Group     string // A | B
	People    int
	Tier      string // "self only" | "self plus one" | "self and family"
	Tricare   int64  // cents a month
	PartB     int64
	Dental    int64
	Vision    int64
	Other     int64
	Total     int64
}

// tricareGroup is A when you first joined before 2018, working back from
// the retirement date and years of service.
func tricareGroup(s Settings) string {
	rd, err := parseDay(s.RetirementDate)
	if err != nil || s.RetYears <= 0 {
		return "A"
	}
	joined := rd.AddDate(0, -int(s.RetYears*12+0.5), 0)
	if joined.Before(time.Date(2018, 1, 1, 0, 0, 0, 0, time.UTC)) {
		return "A"
	}
	return "B"
}

func estimateHealth(s Settings) healthCost {
	h := healthCost{Plan: s.HealthPlan, Group: tricareGroup(s), Other: s.HealthOther}
	if h.Plan == "" {
		h.Plan = "prime"
	}
	for _, p := range healthPlans {
		if p.Value == h.Plan {
			h.PlanLabel = p.Label
		}
	}
	h.People = 1 + s.RetKids + s.RetSchoolKids
	if s.RetSpouse {
		h.People++
	}
	tier := min(h.People, 3) - 1
	h.Tier = [3]string{"self only", "self plus one", "self and family"}[tier]

	switch h.Plan {
	case "prime", "select":
		fee := tricareFees[h.Plan+"-"+h.Group]
		yearly := fee[0]
		if h.People > 1 {
			yearly = fee[1]
		}
		h.Tricare = (yearly + 6) / 12
	case "tfl":
		h.PartB = partBMonthly
		if s.RetSpouse {
			h.PartB *= 2
		}
	}
	if !s.NoDental {
		h.Dental = fedvipDental[tier]
	}
	// FEDVIP vision needs a TRICARE health plan (TFL counts).
	if !s.NoVision && h.Plan != "none" {
		h.Vision = fedvipVision[tier]
	}
	h.Total = h.Tricare + h.PartB + h.Dental + h.Vision + h.Other
	return h
}

func validHealthPlan(v string) bool {
	for _, p := range healthPlans {
		if p.Value == v {
			return true
		}
	}
	return v == ""
}
