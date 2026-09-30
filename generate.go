package main

// Generate buttons: the Advisor drafts content in place from what ARW already
// knows about you. Learning gets the topics a company expects; a resume gets
// a headline, a new section, or a section rewritten to the job posting.
//
// Every prompt carries the same rule: use only facts from your own data.
// The Advisor may reword and reorder, but never invents an employer, a
// number, a date, or a certification. You review and edit what it writes.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const noInvent = "Use ONLY facts from the background below. Never invent employers, titles, numbers, dates, clearances, or certifications; if a detail is unknown, write around it. Plain civilian English, no unexplained military jargon, no em dashes."

// resumeBackground is what the Advisor may draw on: service basics, every
// resume section you have written (other resumes too), and your skills by
// status. It is capped so the prompt stays small.
func resumeBackground(st State) string {
	var b strings.Builder
	s := st.Settings
	fmt.Fprintf(&b, "Service: %s, pay grade %s, %s years at retirement, retiring %s.\n", cmpOr(s.Branch, "Army"), cmpOr(s.PayGrade, "unknown"), fmtDays(s.RetYears), cmpOr(s.RetirementDate, "date unknown"))
	seen := map[string]bool{}
	for _, t := range st.ResumeTargets {
		for _, sec := range t.Sections {
			for _, bl := range sec.Bullets {
				k := strings.ToLower(strings.TrimSpace(bl))
				if k == "" || seen[k] {
					continue
				}
				seen[k] = true
				fmt.Fprintf(&b, "- [%s] %s\n", sec.Heading, bl)
			}
		}
	}
	for _, v := range learningView(st) {
		for _, a := range v.Areas {
			for _, sk := range a.Skills {
				if sk.Status != "learn" {
					fmt.Fprintf(&b, "- Skill (%s): %s\n", skillLabels[sk.Status], sk.Name)
				}
			}
		}
	}
	out := b.String()
	if len(out) > 9000 {
		out = out[:9000]
	}
	return out
}

// generate asks the Advisor and decodes its JSON reply into v.
func (s *Server) generate(r *http.Request, prompt string, v any) error {
	st := s.store.snapshot()
	if !st.aiReady() {
		return fmt.Errorf("choose Claude or ChatGPT for the Advisor in Settings first; Generate runs through it")
	}
	out, err := askAIFunc(r.Context(), st.Settings.AdvisorProvider, prompt, "")
	if err != nil {
		return err
	}
	if json.Unmarshal([]byte(extractJSON(out)), v) != nil {
		return fmt.Errorf("the Advisor's reply came back unreadable; try again")
	}
	return nil
}

// companyGenerate adds the topics a company expects for your target role
// that are not on the card yet, and notes what you already bring.
func (s *Server) companyGenerate(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	anchor := "#co-" + strconv.Itoa(id)
	st := s.store.snapshot()
	var co *Company
	for i := range st.Companies {
		if st.Companies[i].ID == id {
			co = &st.Companies[i]
		}
	}
	if co == nil {
		back(w, r, "")
		return
	}
	var have, areas []string
	for _, sk := range st.Skills {
		if sk.CompanyID == id {
			have = append(have, sk.Name)
		}
	}
	areas = skillAreas(st)
	posting := ""
	if t := st.findTarget(co.TargetID); t != nil {
		posting = t.Requirements
	}
	prompt := "You are a hiring manager and technical mentor. A soldier retiring from the Army wants to work at " + co.Name +
		" as " + cmpOr(co.Role, "an engineer") + ". " + cmpOr(co.Notes, "") + "\n\n" +
		"List the skills, protocols, tools, and concepts that company expects for that role which are NOT already on their list. " +
		"Be specific (\"JREAP-C message framing\", not \"networking\"). Group each under a short area; reuse these areas when they fit: " + strings.Join(areas, ", ") + ". " +
		"For each, say in one sentence why it matters there. Give an official documentation URL only when you are certain it exists, else an empty string. " +
		"Then write fit: two or three sentences on what the soldier already brings that matches, and the biggest gap, using only the background.\n\n"
	if posting != "" {
		prompt += "JOB POSTING:\n" + posting + "\n\n"
	}
	if len(have) > 0 {
		prompt += "ALREADY ON THE LIST, leave out: " + strings.Join(have, "; ") + "\n\n"
	}
	prompt += "BACKGROUND:\n" + resumeBackground(st) + "\n" + noInvent + "\n\n" +
		"Respond with ONLY JSON: {\"fit\":str,\"skills\":[{\"name\":str,\"area\":str,\"why\":str,\"url\":str}]}, at most 12 skills. No prose, no code fence."

	var reply struct {
		Fit    string                                  `json:"fit"`
		Skills []struct{ Name, Area, Why, URL string } `json:"skills"`
	}
	if err := s.generate(r, prompt, &reply); err != nil {
		flash(w, "err", err.Error())
		back(w, r, anchor)
		return
	}
	added := 0
	_ = s.store.mutate(func(st *State) {
		for _, g := range reply.Skills {
			name := strings.TrimSpace(g.Name)
			dup := name == ""
			for _, sk := range st.Skills {
				dup = dup || (sk.CompanyID == id && sameCondition(sk.Name, name))
			}
			if dup || added >= 12 {
				continue
			}
			st.Skills = append(st.Skills, Skill{ID: st.id(), CompanyID: id, Name: name, Area: strings.TrimSpace(g.Area),
				URL: webURL(g.URL), Notes: strings.TrimSpace(g.Why), Status: "learn"})
			added++
		}
		editByID(st.Companies, id, func(c Company) int { return c.ID }, func(c *Company) {
			if f := strings.TrimSpace(reply.Fit); f != "" {
				c.Fit, c.FitAt = f, time.Now().Format("2006-01-02")
			}
		})
	})
	if added == 0 {
		flash(w, "ok", "Nothing new to add. The list already covers what the Advisor expects for this role.")
	} else {
		flash(w, "ok", fmt.Sprintf("Added %d topics to %s. Delete any that do not fit.", added, co.Name))
	}
	back(w, r, anchor)
}

func (s *Server) routeGenerate(mux *http.ServeMux) {
	mux.HandleFunc("POST /resume/targets/{id}/generate/headline", s.genHeadline)
	mux.HandleFunc("POST /resume/targets/{id}/generate/section", s.genSection)
	mux.HandleFunc("POST /resume/targets/{id}/sections/{sid}/generate", s.genRewrite)
	mux.HandleFunc("POST /resume/targets/{id}/learning-skills", s.skillsToResume)
}

// targetPrompt opens every resume prompt: the job, the posting, what the
// resume already says, and the background.
func targetPrompt(st State, t ResumeTarget) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You write resumes for soldiers moving to civilian jobs. Target: %s%s.\n", t.Position, map[bool]string{true: " at " + t.Company, false: ""}[t.Company != ""])
	if t.Requirements != "" {
		b.WriteString("JOB POSTING:\n" + t.Requirements + "\n\n")
	}
	if cv := st.companyFor(t.ID); cv != nil && cv.Total > 0 {
		b.WriteString("Skills this company expects (from their Learning list): ")
		for _, a := range cv.Areas {
			for _, sk := range a.Skills {
				fmt.Fprintf(&b, "%s [%s]; ", sk.Name, sk.Status)
			}
		}
		b.WriteString("\n\n")
	}
	b.WriteString("BACKGROUND:\n" + resumeBackground(st) + "\n" + noInvent + "\n\n")
	return b.String()
}

func toTarget(w http.ResponseWriter, r *http.Request, tid int, anchor string) {
	http.Redirect(w, r, "/resume?t="+strconv.Itoa(tid)+anchor, http.StatusSeeOther)
}

func (s *Server) genHeadline(w http.ResponseWriter, r *http.Request) {
	tid := pathID(r)
	st := s.store.snapshot()
	t := st.findTarget(tid)
	if t == nil {
		toTarget(w, r, tid, "")
		return
	}
	var reply struct{ Headline string }
	err := s.generate(r, targetPrompt(st, *t)+"Write one resume headline, under 90 characters, aimed at this job. Respond with ONLY JSON: {\"headline\":str}.", &reply)
	if err == nil && strings.TrimSpace(reply.Headline) == "" {
		err = fmt.Errorf("the Advisor sent back an empty headline; try again")
	}
	if err != nil {
		flash(w, "err", err.Error())
	} else {
		_ = s.store.mutate(func(st *State) {
			if t := st.findTarget(tid); t != nil {
				t.Headline = strings.TrimSpace(reply.Headline)
			}
		})
		flash(w, "ok", "New headline written. Edit it if it does not sound like you.")
	}
	toTarget(w, r, tid, "")
}

func (s *Server) genSection(w http.ResponseWriter, r *http.Request) {
	tid := pathID(r)
	st := s.store.snapshot()
	t := st.findTarget(tid)
	if t == nil {
		toTarget(w, r, tid, "")
		return
	}
	focus := cmpOr(field(r, "focus"), "the most important section this resume is missing for this job")
	var existing []string
	for _, sec := range t.Sections {
		existing = append(existing, sec.Heading)
	}
	prompt := targetPrompt(st, *t) + "This resume already has: " + cmpOr(strings.Join(existing, "; "), "no sections") + ".\n" +
		"Write one new section about: " + focus + ". Give it a short heading and 3 to 6 achievement bullets that match the posting, strongest first. " +
		"Respond with ONLY JSON: {\"heading\":str,\"bullets\":[str]}."
	var reply struct {
		Heading string
		Bullets []string
	}
	err := s.generate(r, prompt, &reply)
	if err == nil && len(cleanBullets(reply.Bullets)) == 0 {
		err = fmt.Errorf("the Advisor sent back no bullets; add more background to a resume first, then try again")
	}
	if err != nil {
		flash(w, "err", err.Error())
		toTarget(w, r, tid, "")
		return
	}
	var sid int
	_ = s.store.mutate(func(st *State) {
		if t := st.findTarget(tid); t != nil {
			sid = st.id()
			t.Sections = append(t.Sections, ResumeSection{ID: sid, Heading: cmpOr(reply.Heading, "Experience"), Bullets: cleanBullets(reply.Bullets)})
		}
	})
	flash(w, "ok", "Section drafted. Check every bullet is true before you send it.")
	toTarget(w, r, tid, "#rsec-"+strconv.Itoa(sid))
}

// genRewrite rewrites one section's bullets to fit the job posting, keeping
// the same facts.
func (s *Server) genRewrite(w http.ResponseWriter, r *http.Request) {
	tid := pathID(r)
	sid, _ := strconv.Atoi(r.PathValue("sid"))
	st := s.store.snapshot()
	t := st.findTarget(tid)
	var sec *ResumeSection
	if t != nil {
		for i := range t.Sections {
			if t.Sections[i].ID == sid {
				sec = &t.Sections[i]
			}
		}
	}
	if sec == nil {
		toTarget(w, r, tid, "")
		return
	}
	prompt := targetPrompt(st, *t) + "Rewrite this section so it speaks to the job: lead with what the posting asks for, use its words where they are true, quantify only with numbers already given. Keep every fact; add none.\n" +
		"SECTION: " + sec.Heading + "\n- " + strings.Join(sec.Bullets, "\n- ") + "\n\nRespond with ONLY JSON: {\"bullets\":[str]}."
	var reply struct{ Bullets []string }
	err := s.generate(r, prompt, &reply)
	if err == nil && len(cleanBullets(reply.Bullets)) == 0 {
		err = fmt.Errorf("the Advisor sent back no bullets; try again")
	}
	if err != nil {
		flash(w, "err", err.Error())
	} else {
		_ = s.store.mutate(func(st *State) {
			if t := st.findTarget(tid); t != nil {
				for i := range t.Sections {
					if t.Sections[i].ID == sid {
						t.Sections[i].Bullets = cleanBullets(reply.Bullets)
					}
				}
			}
		})
		flash(w, "ok", "Section rewritten for this job.")
	}
	toTarget(w, r, tid, "#rsec-"+strconv.Itoa(sid))
}

func cleanBullets(in []string) []string {
	var out []string
	for _, b := range in {
		if b = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(b), "-•* ")); b != "" {
			out = append(out, b)
		}
	}
	return out
}

// skillsToResume writes a Technical Skills section from the skills you have
// mastered for the linked company, one line per area, replacing the last
// copy so it can be rerun as you learn.
func (s *Server) skillsToResume(w http.ResponseWriter, r *http.Request) {
	tid := pathID(r)
	st := s.store.snapshot()
	cv := st.companyFor(tid)
	var lines []string
	if cv != nil {
		for _, a := range cv.Areas {
			var names []string
			for _, sk := range a.Skills {
				if sk.Status == "mastered" {
					names = append(names, sk.Name)
				}
			}
			if len(names) > 0 {
				lines = append(lines, a.Name+": "+strings.Join(names, ", "))
			}
		}
	}
	if len(lines) == 0 {
		flash(w, "err", "Mark a skill Mastered on the Learning page first. Only mastered skills go on the resume.")
		toTarget(w, r, tid, "")
		return
	}
	var sid int
	_ = s.store.mutate(func(st *State) {
		t := st.findTarget(tid)
		if t == nil {
			return
		}
		for i := range t.Sections {
			if t.Sections[i].Heading == "Technical Skills" {
				t.Sections[i].Bullets, sid = lines, t.Sections[i].ID
				return
			}
		}
		sid = st.id()
		t.Sections = append(t.Sections, ResumeSection{ID: sid, Heading: "Technical Skills", Bullets: lines})
	})
	flash(w, "ok", "Technical Skills updated from what you have mastered.")
	toTarget(w, r, tid, "#rsec-"+strconv.Itoa(sid))
}
