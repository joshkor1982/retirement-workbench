package main

// Find Conditions in My Records: the Advisor reads your uploaded medical
// records and DBQs and lists conditions they document that are not on your
// claim list yet, each with where it found them. Nothing is added until you
// pick it. A condition you have lived with for years is the easiest one to
// forget to claim, and the record is what the VA rates from.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// askAIFunc is the Advisor call, swapped out in tests.
var askAIFunc = askAI

type FoundCondition struct {
	Name     string `json:"name"`
	Where    string `json:"where"`    // document, and date or page when given
	Evidence string `json:"evidence"` // the finding, in one sentence
}

type ConditionScan struct {
	At       string           `json:"at"`
	Provider string           `json:"provider"`
	Read     int              `json:"read"` // PDFs the Advisor was given
	Items    []FoundCondition `json:"items"`
}

// claimEvidence lists the DBQs, medical records, and other evidence (lay
// statements, photos of injuries, letters) on file and the PDFs the Advisor
// may read, from the docs folder only, DBQs first. Other evidence counts as
// records in the prompt.
func (s *Server) claimEvidence(st State) (dbqNames, recNames, files []string, docsDir string) {
	pdf := func(d Doc) {
		if strings.HasSuffix(strings.ToLower(d.Name), ".pdf") && len(files) < 20 {
			files = append(files, filepath.Base(s.docPath(d.File))+"  ("+d.Name+")")
		}
	}
	for _, kind := range []string{"dbq", "medical", "evidence"} {
		for _, d := range st.Docs {
			if d.Kind != kind {
				continue
			}
			if kind == "dbq" {
				dbqNames = append(dbqNames, d.Name)
			} else {
				recNames = append(recNames, d.Name)
			}
			pdf(d)
		}
	}
	if len(files) > 0 {
		docsDir, _ = filepath.Abs(filepath.Join(s.data, "docs"))
	}
	return
}

func sameCondition(a, b string) bool {
	norm := func(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }
	return norm(a) == norm(b)
}

func medicalErr(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/medical?err="+url.QueryEscape(msg)+"#conditions", http.StatusSeeOther)
}

func (s *Server) conditionsFind(w http.ResponseWriter, r *http.Request) {
	st := s.store.snapshot()
	if !st.aiReady() {
		medicalErr(w, r, "Choose Claude or ChatGPT for the Advisor in Settings first. The search runs through it.")
		return
	}
	_, _, files, docsDir := s.claimEvidence(st)
	if len(files) == 0 {
		medicalErr(w, r, "Upload your medical records or DBQs as PDFs below first. The search reads those.")
		return
	}
	var have []string
	for _, c := range st.Conditions {
		have = append(have, c.Name)
	}
	prompt := "You are helping a soldier prepare a VA disability claim. Read every PDF listed below, in " + docsDir + ", " +
		"and list each distinct condition the records document: a diagnosis, a recurring complaint, an injury, or a finding on an exam, lab, or image. " +
		"Name it the way a claim would (\"Right knee, patellofemoral pain\", \"Tinnitus\", \"Lumbar strain\"), with the side when there is one. " +
		"For each, give where you found it (the document name, and the date or page when you can) and the finding in one plain sentence. " +
		"Only list what the documents actually say; never guess or add conditions that are not there. Merge repeats of the same condition into one entry.\n\n" +
		"PDFs:\n- " + strings.Join(files, "\n- ") + "\n\n"
	if len(have) > 0 {
		prompt += "Already on the claim list, so leave these out: " + strings.Join(have, "; ") + "\n\n"
	}
	prompt += "Respond with ONLY a JSON object: {\"conditions\":[{\"name\":str,\"where\":str,\"evidence\":str}]}. " +
		"An empty list is a fine answer. No prose, no code fence."

	out, err := askAIFunc(r.Context(), st.Settings.AdvisorProvider, prompt, docsDir)
	if err != nil {
		medicalErr(w, r, err.Error())
		return
	}
	var parsed struct {
		Conditions []FoundCondition `json:"conditions"`
	}
	if json.Unmarshal([]byte(extractJSON(out)), &parsed) != nil {
		medicalErr(w, r, "The search came back unreadable. Try again.")
		return
	}
	scan := &ConditionScan{At: time.Now().Format("2006-01-02 15:04"), Provider: providerLabel(st.Settings.AdvisorProvider), Read: len(files)}
	for _, f := range parsed.Conditions {
		f.Name, f.Where, f.Evidence = strings.TrimSpace(f.Name), strings.TrimSpace(f.Where), strings.TrimSpace(f.Evidence)
		if f.Name == "" {
			continue
		}
		dup := false
		for _, h := range have {
			dup = dup || sameCondition(h, f.Name)
		}
		for _, x := range scan.Items {
			dup = dup || sameCondition(x.Name, f.Name)
		}
		if !dup && len(scan.Items) < 30 {
			scan.Items = append(scan.Items, f)
		}
	}
	_ = s.store.mutate(func(st *State) { st.Found = scan })
	if len(scan.Items) == 0 {
		flash(w, "ok", "No new conditions found in your records. Everything they document is already on your list.")
	} else {
		flash(w, "ok", fmt.Sprintf("Found %d conditions in your records. Pick the ones to add.", len(scan.Items)))
	}
	http.Redirect(w, r, "/medical#found", http.StatusSeeOther)
}

// conditionsFoundAdd adds the picked conditions to the claim list, marked as
// in the record, with where they were found as the note.
func (s *Server) conditionsFoundAdd(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	added := 0
	_ = s.store.mutate(func(st *State) {
		if st.Found == nil {
			return
		}
		pick := map[int]bool{}
		for _, v := range r.Form["pick"] {
			if n, err := strconv.Atoi(v); err == nil {
				pick[n] = true
			}
		}
		var keep []FoundCondition
		for i, f := range st.Found.Items {
			if !pick[i] {
				keep = append(keep, f)
				continue
			}
			note := f.Evidence
			if f.Where != "" {
				note = "Found in " + f.Where + ": " + f.Evidence
			}
			st.Conditions = append(st.Conditions, Condition{ID: st.id(), Name: f.Name, Documented: true, Notes: note})
			added++
		}
		st.Found.Items = keep
	})
	switch added {
	case 0:
		flash(w, "err", "Check at least one condition to add.")
	case 1:
		flash(w, "ok", "Added 1 condition to your claim list.")
	default:
		flash(w, "ok", fmt.Sprintf("Added %d conditions to your claim list.", added))
	}
	http.Redirect(w, r, "/medical#conditions", http.StatusSeeOther)
}

func (s *Server) conditionsFoundClear(w http.ResponseWriter, r *http.Request) {
	_ = s.store.mutate(func(st *State) { st.Found = nil })
	http.Redirect(w, r, "/medical#conditions", http.StatusSeeOther)
}
