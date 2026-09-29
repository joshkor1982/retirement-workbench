package main

// Savings: money put away, next to the debt paid off. A savings log of
// deposits (positive) and withdrawals (negative); the balance is the sum, so
// it never drifts from the entries you can see. An optional named goal
// ("Move fund", $15,000) turns the balance into progress.

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

type SavingsEntry struct {
	ID     int    `json:"id"`
	Date   string `json:"date"`         // YYYY-MM-DD
	Amount int64  `json:"amount_cents"` // + deposit, - withdrawal
	Note   string `json:"note"`
}

type savingsSummary struct {
	Saved     int64 // current balance
	Goal      int64
	GoalName  string
	Pct       int   // of the goal, capped at 100; -1 with no goal
	Left      int64 // to the goal, never negative
	ThisMonth int64 // net change this calendar month
	Entries   []SavingsEntry
}

func summarizeSavings(st State) savingsSummary {
	sum := savingsSummary{Goal: st.Settings.SavingsGoal, GoalName: st.Settings.SavingsGoalName, Pct: -1}
	month := time.Now().Format("2006-01")
	for _, e := range st.Savings {
		sum.Saved += e.Amount
		if strings.HasPrefix(e.Date, month) {
			sum.ThisMonth += e.Amount
		}
	}
	if sum.Goal > 0 {
		sum.Pct = int(max(0, min(100, sum.Saved*100/sum.Goal)))
		sum.Left = max(0, sum.Goal-sum.Saved)
	}
	sum.Entries = append([]SavingsEntry(nil), st.Savings...)
	sort.SliceStable(sum.Entries, func(i, j int) bool {
		if sum.Entries[i].Date != sum.Entries[j].Date {
			return sum.Entries[i].Date > sum.Entries[j].Date
		}
		return sum.Entries[i].ID > sum.Entries[j].ID
	})
	return sum
}

func addSavings(st *State, amount int64, date, note string) (SavingsEntry, error) {
	if amount == 0 {
		return SavingsEntry{}, fmt.Errorf("amount is zero")
	}
	if _, err := time.Parse("2006-01-02", date); err != nil {
		date = time.Now().Format("2006-01-02")
	}
	e := SavingsEntry{ID: st.id(), Date: date, Amount: amount, Note: strings.TrimSpace(note)}
	st.Savings = append(st.Savings, e)
	return e, nil
}

func (s *Server) savingsAdd(w http.ResponseWriter, r *http.Request) {
	amt, err := parseMoney(r.FormValue("amount"))
	if err == nil && amt != 0 {
		if amt < 0 {
			amt = -amt
		}
		if r.FormValue("kind") == "withdraw" {
			amt = -amt
		}
		_ = s.store.mutate(func(st *State) {
			_, _ = addSavings(st, amt, strings.TrimSpace(r.FormValue("date")), r.FormValue("note"))
		})
	}
	http.Redirect(w, r, "/budget#savings", http.StatusSeeOther)
}

func (s *Server) savingsDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	_ = s.store.mutate(func(st *State) {
		st.Savings = deleteByID(st.Savings, id, func(e SavingsEntry) int { return e.ID })
	})
	http.Redirect(w, r, "/budget#savings", http.StatusSeeOther)
}

func (s *Server) savingsGoal(w http.ResponseWriter, r *http.Request) {
	goal, err := parseMoney(r.FormValue("goal"))
	if strings.TrimSpace(r.FormValue("goal")) == "" {
		goal, err = 0, nil // an empty amount clears the goal
	}
	if err == nil && goal >= 0 {
		_ = s.store.mutate(func(st *State) {
			st.Settings.SavingsGoal = goal
			st.Settings.SavingsGoalName = strings.TrimSpace(r.FormValue("name"))
		})
	}
	http.Redirect(w, r, "/budget#savings", http.StatusSeeOther)
}

// ---------- Advisor tools ------------------------------------------------------

func savingsTools() []map[string]any {
	return []map[string]any{
		tool("log_savings", "Record money moved into savings (positive) or taken out (negative).", []string{"amount"},
			map[string]any{"amount": prop("string", "dollars; negative for a withdrawal"),
				"date": prop("string", "YYYY-MM-DD, default today"), "note": prop("string", "")}),
		tool("set_savings_goal", "Set the savings goal amount and its name (e.g. 'Move fund'). Amount 0 clears it.", []string{"amount"},
			map[string]any{"amount": prop("string", "dollars"), "name": prop("string", "")}),
	}
}

func toolSavings(st *State, name string, in map[string]any) (string, bool) {
	switch name {
	case "log_savings":
		amt, err := parseMoney(toolStr(in, "amount"))
		if err != nil {
			return "error: amount must be dollars, e.g. 500 or -120.50", true
		}
		e, err := addSavings(st, amt, toolStr(in, "date"), toolStr(in, "note"))
		if err != nil {
			return "error: " + err.Error(), true
		}
		return fmt.Sprintf("logged savings #%d %s on %s; balance now %s", e.ID, money(e.Amount), e.Date, money(summarizeSavings(*st).Saved)), true
	case "set_savings_goal":
		amt, err := parseMoney(toolStr(in, "amount"))
		if err != nil || amt < 0 {
			return "error: amount must be dollars", true
		}
		st.Settings.SavingsGoal = amt
		if v := toolStr(in, "name"); v != "" || amt == 0 {
			st.Settings.SavingsGoalName = v
		}
		return fmt.Sprintf("savings goal set to %s %q", money(amt), st.Settings.SavingsGoalName), true
	}
	return "", false
}

func savingsDigest(st State) string {
	sum := summarizeSavings(st)
	line := fmt.Sprintf("Savings: balance %s, this month %s", money(sum.Saved), money(sum.ThisMonth))
	if sum.Goal > 0 {
		line += fmt.Sprintf(", goal %s %q (%d%%)", money(sum.Goal), sum.GoalName, sum.Pct)
	}
	return line + "\n"
}
