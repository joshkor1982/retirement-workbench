package main

// High-3: the average of your highest 36 months of basic pay, which for
// nearly everyone is the last 36 months before retirement. ARW works it out
// month by month from your pay grade and years of service, using the DoD
// basic pay table in force each month (paytables_data.go).
//
// Where the tables come from: the official tables live on dfas.mil and
// militarypay.defense.gov, which refuse automated downloads. The 2026
// enlisted table is the official DFAS table, entered from DoD FMR Vol. 7A,
// Ch. 1. The other rows were taken from published copies and kept only
// where three independent copies agreed (scripts/paytables).
//
// Months in a year with no published table use the latest table plus an
// assumed raise, which the page labels as an estimate.

import (
	"strconv"
	"time"
)

// high3Grades are the grades ARW calculates. E-1 to E-6 had a mid-2025
// raise the tables here do not split out yet, and O-8 to O-10 are capped by
// the civilian Executive Schedule; those enter High-3 by hand for now.
var high3Grades = []string{"E-7", "E-8", "E-9", "W-1", "W-2", "W-3", "W-4", "W-5",
	"O-1E", "O-2E", "O-3E", "O-1", "O-2", "O-3", "O-4", "O-5", "O-6", "O-7"}

var payGrades = []string{"E-1", "E-2", "E-3", "E-4", "E-5", "E-6", "E-7", "E-8", "E-9",
	"W-1", "W-2", "W-3", "W-4", "W-5", "O-1E", "O-2E", "O-3E",
	"O-1", "O-2", "O-3", "O-4", "O-5", "O-6", "O-7", "O-8", "O-9", "O-10"}

func high3Supported(g string) bool {
	for _, x := range high3Grades {
		if x == g {
			return true
		}
	}
	return false
}

const defaultRaise = 3.0 // percent a year, for years with no published table

var payColumns = []int{0, 2, 3, 4, 6, 8, 10, 12, 14, 16, 18, 20, 22, 24, 26, 28, 30, 32, 34, 36, 38, 40}

// column maps completed years of service to the table column: "2 or less"
// is column 0, "over 2" is 2, and so on.
func column(years int) int {
	c := 0
	for _, x := range payColumns {
		if years >= x && x > 0 {
			c = x
		}
	}
	return c
}

// monthlyBasic is the basic pay for a grade in a given year at a given
// number of completed years, projecting past the newest table.
func monthlyBasic(grade string, year, years int, raisePct float64) (int64, bool) {
	latest := 0
	for y := range basicPay {
		latest = max(latest, y)
	}
	oldest := latest
	for y := range basicPay {
		oldest = min(oldest, y)
	}
	y := min(max(year, oldest), latest)
	row, ok := basicPay[y][grade]
	if !ok {
		return 0, false
	}
	// Some grades start later (E-8 at over 8): use the lowest column paid.
	c := column(years)
	v, ok := row[c]
	for !ok && c < 40 {
		c = nextColumn(c)
		v, ok = row[c]
	}
	if !ok {
		return 0, false
	}
	f := float64(v)
	for i := latest; i < year; i++ {
		f *= 1 + raisePct/100
	}
	return int64(f + 0.5), true
}

func nextColumn(c int) int {
	for _, x := range payColumns {
		if x > c {
			return x
		}
	}
	return 40
}

type high3Calc struct {
	Grade     string
	Amount    int64 // cents a month
	Projected int   // months that used an assumed raise
	Raise     string
	From, To  string // the 36-month window
	OK        bool
}

// calcHigh3 averages basic pay over the 36 months before retirement,
// assuming the same grade throughout.
func calcHigh3(grade string, yearsAtRetirement float64, retire time.Time, raisePct float64) high3Calc {
	h := high3Calc{Grade: grade, Raise: strconv.FormatFloat(raisePct, 'f', -1, 64) + "%"}
	if !high3Supported(grade) || yearsAtRetirement <= 0 || retire.IsZero() {
		return h
	}
	latest := 0
	for y := range basicPay {
		latest = max(latest, y)
	}
	totalMonths := int(yearsAtRetirement*12 + 0.5)
	var sum int64
	for k := 1; k <= 36; k++ {
		m := retire.AddDate(0, -k, 0)
		v, ok := monthlyBasic(grade, m.Year(), max(0, totalMonths-k)/12, raisePct)
		if !ok {
			return h
		}
		if m.Year() > latest {
			h.Projected++
		}
		sum += v
	}
	h.Amount = (sum + 18) / 36
	h.From = retire.AddDate(0, -36, 0).Format("Jan 2006")
	h.To = retire.AddDate(0, -1, 0).Format("Jan 2006")
	h.OK = true
	return h
}
