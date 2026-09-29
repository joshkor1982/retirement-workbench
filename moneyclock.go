package main

// The Budget page's live clock counts toward whatever money goal fits the
// person: the debt-free date when there is debt to pay down, and otherwise
// retirement day with the savings goal. Not everyone retiring has debt.

import "time"

type clockView struct {
	HasDebt   bool
	RetDays   int    // days to retirement; -1 unknown
	RetDate   string // "Dec 3, 2027"
	RetISO    string // for the live ticker
	SavPerDay int64  // savings needed per day to reach the goal by retirement
	SavPerPay int64  // the same per paycheck (twice a month)
	Paychecks int
}

func moneyClock(st State, openDebt int64) clockView {
	c := clockView{RetDays: -1}
	for _, d := range st.Debts {
		if !d.PaidOff {
			c.HasDebt = true
		}
	}
	c.HasDebt = c.HasDebt || openDebt > 0
	rd, err := parseDay(st.Settings.RetirementDate)
	if err != nil {
		return c
	}
	c.RetDays = max(0, daysBetween(localDay(time.Now()), rd))
	c.RetDate = rd.Format("Jan 2, 2006")
	c.RetISO = rd.Format("2006-01-02T00:00:00")
	c.Paychecks = max(1, c.RetDays/15) // DFAS pays on the 1st and the 15th
	if sav := summarizeSavings(st); sav.Goal > 0 && sav.Left > 0 && c.RetDays > 0 {
		c.SavPerDay = sav.Left / int64(c.RetDays)
		c.SavPerPay = sav.Left / int64(c.Paychecks)
	}
	return c
}
