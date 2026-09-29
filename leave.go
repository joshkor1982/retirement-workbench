package main

// Leave: your balance today and what it grows into by terminal leave. Leave
// accrues 2.5 days a month; on 1 October anything over 60 days is lost
// (special leave accrual aside), so the projection applies that cap and
// warns before it bites.

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	leavePerMonth = 2.5
	leaveCap      = 60.0
)

type leaveView struct {
	Set        bool
	Balance    string // "42.5"
	AsOf       string // "Sep 29, 2026"
	AtTerminal string // projected balance the day terminal leave starts
	Forfeit    string // days lost at the October caps before then, "" if none
}

func fmtDays(d float64) string {
	return strconv.FormatFloat(d, 'f', -1, 64)
}

// parseLeaveDays accepts "42", "42.5", or "" (unset).
func parseLeaveDays(s string) (float64, bool, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < -30 || v > 200 {
		return 0, false, fmt.Errorf("leave balance must be a number of days, like 42.5")
	}
	return v, true, nil
}

// projectLeave walks month by month from the as-of date to the start of
// terminal leave, adding 2.5 days each month and applying the 60-day cap
// every 1 October.
func projectLeave(balance float64, asOf, terminal time.Time) (atTerminal, forfeit float64) {
	bal := balance
	cur := localDay(asOf)
	for {
		next := cur.AddDate(0, 1, 0)
		if next.After(terminal) {
			break
		}
		bal += leavePerMonth
		// Crossing into a new fiscal year: excess over the cap is lost.
		if fy := time.Date(next.Year(), time.October, 1, 0, 0, 0, 0, time.Local); !fy.Before(cur) && !fy.After(next) && bal > leaveCap {
			forfeit += bal - leaveCap
			bal = leaveCap
		}
		cur = next
	}
	return bal, forfeit
}

func buildLeaveView(st State, terminalStart string) leaveView {
	s := st.Settings
	if !s.LeaveSet {
		return leaveView{}
	}
	v := leaveView{Set: true, Balance: fmtDays(s.LeaveDays)}
	asOf, err := parseDay(s.LeaveAsOf)
	if err != nil {
		asOf = localDay(time.Now())
	}
	v.AsOf = asOf.Format("Jan 2, 2006")
	if ts, err := parseDay(terminalStart); err == nil && ts.After(asOf) {
		at, lost := projectLeave(s.LeaveDays, asOf, ts)
		v.AtTerminal = fmtDays(float64(int(at*2+0.5)) / 2) // to the half day, like the LES
		if lost > 0 {
			v.Forfeit = fmtDays(float64(int(lost*2+0.5)) / 2)
		}
	}
	return v
}

func leaveDigest(st State) string {
	if !st.Settings.LeaveSet {
		return "Leave balance: not entered\n"
	}
	return fmt.Sprintf("Leave balance: %s days as of %s\n", fmtDays(st.Settings.LeaveDays), st.Settings.LeaveAsOf)
}

func leaveTool() map[string]any {
	return tool("set_leave_balance", "Set the soldier's leave balance in days, as shown on the LES.", []string{"days"},
		map[string]any{"days": prop("string", "e.g. 42.5"), "as_of": prop("string", "YYYY-MM-DD, default today")})
}

func toolLeave(st *State, in map[string]any) string {
	v, ok, err := parseLeaveDays(toolStr(in, "days"))
	if err != nil || !ok {
		return "error: days must be a number like 42.5"
	}
	asOf := toolStr(in, "as_of")
	if _, err := parseDay(asOf); err != nil {
		asOf = time.Now().Format("2006-01-02")
	}
	st.Settings.LeaveDays, st.Settings.LeaveSet, st.Settings.LeaveAsOf = v, true, asOf
	return fmt.Sprintf("leave balance set to %s days as of %s", fmtDays(v), asOf)
}
