package main

// VA disability compensation rates, from
// https://www.va.gov/disability/compensation-rates/veteran-rates/
// (effective December 1, 2025). VA adjusts these every December 1 with the
// cost-of-living increase; update the table and vaRatesEffective together.

const vaRatesEffective = "December 1, 2025"

// vaRow is one rating's monthly rates in cents. Parent add-ons are the same
// whether or not there is a spouse or child (the table's own increments), so
// one per-parent amount covers every combination.
type vaRow struct {
	Alone, Spouse, Child1, SpouseChild1, PerParent, AddChild, AddSchoolChild int64
}

var vaRates = map[int]vaRow{
	30:  {Alone: 55247, Spouse: 61747, Child1: 59647, SpouseChild1: 66647, PerParent: 5200, AddChild: 3200, AddSchoolChild: 10500},
	40:  {Alone: 79584, Spouse: 88284, Child1: 85384, SpouseChild1: 94784, PerParent: 7000, AddChild: 4300, AddSchoolChild: 14000},
	50:  {Alone: 113290, Spouse: 124190, Child1: 120590, SpouseChild1: 132290, PerParent: 8800, AddChild: 5400, AddSchoolChild: 17600},
	60:  {Alone: 143502, Spouse: 156602, Child1: 152302, SpouseChild1: 166302, PerParent: 10500, AddChild: 6500, AddSchoolChild: 21100},
	70:  {Alone: 180845, Spouse: 196145, Child1: 191045, SpouseChild1: 207445, PerParent: 12300, AddChild: 7600, AddSchoolChild: 24600},
	80:  {Alone: 210215, Spouse: 227715, Child1: 221915, SpouseChild1: 240615, PerParent: 14000, AddChild: 8700, AddSchoolChild: 28100},
	90:  {Alone: 236230, Spouse: 255930, Child1: 249430, SpouseChild1: 270430, PerParent: 15800, AddChild: 9800, AddSchoolChild: 31700},
	100: {Alone: 393858, Spouse: 415817, Child1: 408543, SpouseChild1: 431899, PerParent: 17624, AddChild: 10911, AddSchoolChild: 35245},
}

// 10% and 20% pay the same with or without dependents.
var vaLowRates = map[int]int64{10: 18042, 20: 35666}

type vaDependents struct {
	Spouse     bool
	Children   int // under 18
	SchoolKids int // 18 to 23 in a qualifying school program
	Parents    int // dependent parents, 0 to 2
}

// roundRating rounds a combined rating to the nearest 10, as VA does.
func roundRating(r int) int {
	if r <= 0 {
		return 0
	}
	if r >= 100 {
		return 100
	}
	return (r + 5) / 10 * 10
}

// vaMonthly returns the monthly VA compensation in cents for a rating and
// dependents. The first child is in the base "with child" rate; each
// additional child adds the table's amount for their age group. Aid and
// Attendance for a spouse is not modeled.
func vaMonthly(rating int, d vaDependents) int64 {
	r := roundRating(rating)
	if v, ok := vaLowRates[r]; ok {
		return v
	}
	row, ok := vaRates[r]
	if !ok {
		return 0
	}
	kids := max(0, d.Children) + max(0, d.SchoolKids)
	var total int64
	switch {
	case kids > 0 && d.Spouse:
		total = row.SpouseChild1
	case kids > 0:
		total = row.Child1
	case d.Spouse:
		total = row.Spouse
	default:
		total = row.Alone
	}
	if kids > 0 {
		under, school := max(0, d.Children), max(0, d.SchoolKids)
		if under > 0 {
			under-- // the base rate already counts one child
		} else {
			school--
		}
		total += int64(under)*row.AddChild + int64(school)*row.AddSchoolChild
	}
	total += int64(min(max(0, d.Parents), 2)) * row.PerParent
	return total
}
