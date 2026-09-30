package main

// Handbooks: a long-form guide per topic, and a profile per company, each a
// Markdown file in the docs folder (docs/handbook-<slug>.md) so backup and
// restore carry them with everything else. A skill links to a handbook by
// slug; two skills can share one. Every topic and company has its own page:
// the guide, a table of contents, and the references (and books), which you
// can add to or trim. Generate Handbook and Generate Profile have the Advisor
// write one where none exists.

import (
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

type handbook struct {
	Slug, Title, Summary string
	Body                 template.HTML
	TOC                  []mdHeading
	Refs, Books          []mdRef
	Words                int
	Updated              string
	Back                 string // the page it is shown on, for the reference forms
}

type handbookInfo struct {
	Slug, Title, Summary string
	Company              bool // a company profile, not a topic guide
	Words                int
	Updated              string
}

func (s *Server) hbPath(slug string) string {
	return filepath.Join(s.data, "docs", "handbook-"+slug+".md")
}

func (s *Server) loadHandbook(slug string) *handbook {
	if !slugRE.MatchString(slug) {
		return nil
	}
	raw, err := os.ReadFile(s.hbPath(slug))
	if err != nil {
		return nil
	}
	src := string(raw)
	h := &handbook{Slug: slug, Words: len(strings.Fields(src))}
	h.Title, h.Summary = mdTitle(src)
	rest, refs := splitSection(src, "References")
	rest, books := splitSection(rest, "Recommended Books")
	h.Refs, h.Books = parseRefs(refs), parseRefs(books)
	h.Body, h.TOC = renderMarkdown(dropLead(rest))
	if fi, err := os.Stat(s.hbPath(slug)); err == nil {
		h.Updated = fi.ModTime().Format("2006-01-02")
	}
	return h
}

// handbookLibrary lists every handbook and profile on disk, by title.
func (s *Server) handbookLibrary() []handbookInfo {
	entries, _ := os.ReadDir(filepath.Join(s.data, "docs"))
	var out []handbookInfo
	for _, e := range entries {
		n := e.Name()
		if !strings.HasPrefix(n, "handbook-") || !strings.HasSuffix(n, ".md") {
			continue
		}
		slug := strings.TrimSuffix(strings.TrimPrefix(n, "handbook-"), ".md")
		if !slugRE.MatchString(slug) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.data, "docs", n))
		if err != nil {
			continue
		}
		t, sum := mdTitle(string(raw))
		info := handbookInfo{Slug: slug, Title: cmpOr(t, slug), Summary: sum, Company: strings.HasPrefix(slug, "company_"), Words: len(strings.Fields(string(raw)))}
		if fi, err := e.Info(); err == nil {
			info.Updated = fi.ModTime().Format("2006-01-02")
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title) })
	return out
}

// saveHandbook writes the file atomically: a temp file, then a rename.
func (s *Server) saveHandbook(slug, src string) error {
	if !slugRE.MatchString(slug) {
		return fmt.Errorf("bad handbook name %q", slug)
	}
	dir := filepath.Join(s.data, "docs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".handbook-*")
	if err != nil {
		return err
	}
	if _, err = tmp.WriteString(strings.TrimSpace(src) + "\n"); err == nil {
		err = tmp.Sync()
	}
	tmp.Close()
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o600)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), s.hbPath(slug))
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// freeSlug turns a name into a slug no other handbook uses.
func (s *Server) freeSlug(prefix, name string) string {
	base := strings.Trim(mdID(name), "-")
	if len(base) > 48 {
		base = strings.Trim(base[:48], "-")
	}
	base = prefix + cmpOr(base, "topic")
	slug := base
	for n := 2; ; n++ {
		if _, err := os.Stat(s.hbPath(slug)); os.IsNotExist(err) {
			return slug
		}
		slug = base + "-" + strconv.Itoa(n)
	}
}

func (s *Server) routeHandbooks(mux *http.ServeMux) {
	mux.HandleFunc("GET /learning/skills/{id}", s.topicPage)
	mux.HandleFunc("GET /learning/companies/{id}", s.companyPage)
	mux.HandleFunc("GET /handbooks/{slug}", s.handbookPage)
	mux.HandleFunc("POST /learning/skills/{id}/handbook", s.topicLink)
	mux.HandleFunc("POST /learning/skills/{id}/generate-handbook", s.topicGenerate)
	mux.HandleFunc("POST /learning/companies/{id}/profile", s.companyProfileLink)
	mux.HandleFunc("POST /learning/companies/{id}/generate-profile", s.companyProfileGenerate)
	mux.HandleFunc("POST /handbooks/{slug}/refs", s.refAdd)
	mux.HandleFunc("POST /handbooks/{slug}/refs/delete", s.refDelete)
}

func (st State) skill(id int) (Skill, Company, bool) {
	for _, sk := range st.Skills {
		if sk.ID == id {
			for _, c := range st.Companies {
				if c.ID == sk.CompanyID {
					return sk, c, true
				}
			}
			return sk, Company{}, true
		}
	}
	return Skill{}, Company{}, false
}

func (s *Server) topicPage(w http.ResponseWriter, r *http.Request) {
	st := s.store.snapshot()
	sk, co, ok := st.skill(pathID(r))
	if !ok {
		http.Redirect(w, r, "/learning", http.StatusSeeOther)
		return
	}
	hb := s.loadHandbook(sk.Handbook)
	if hb != nil {
		hb.Back = "/learning/skills/" + strconv.Itoa(sk.ID)
	}
	s.page(w, "learning", map[string]any{
		"Template": "topic", "Skill": sk, "Co": co, "HB": hb,
		"Library": s.handbookLibrary(), "SkillLabels": skillLabels, "AdvisorReady": st.aiReady(),
	})
}

func (s *Server) companyPage(w http.ResponseWriter, r *http.Request) {
	st := s.store.snapshot()
	id := pathID(r)
	for _, v := range learningView(st) {
		if v.ID != id {
			continue
		}
		hbs := map[string]string{}
		for _, a := range v.Areas {
			for _, sk := range a.Skills {
				if h := s.loadHandbook(sk.Handbook); h != nil {
					hbs[sk.Handbook] = h.Title
				}
			}
		}
		prof := s.loadHandbook(v.Profile)
		if prof != nil {
			prof.Back = "/learning/companies/" + strconv.Itoa(v.ID)
		}
		s.page(w, "learning", map[string]any{
			"Template": "company", "Co": v, "Profile": prof, "HBTitles": hbs,
			"Library": s.handbookLibrary(), "SkillLabels": skillLabels, "AdvisorReady": st.aiReady(),
		})
		return
	}
	http.Redirect(w, r, "/learning", http.StatusSeeOther)
}

// handbookPage shows a handbook on its own, with the topics that use it.
func (s *Server) handbookPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	h := s.loadHandbook(slug)
	if h == nil {
		http.Redirect(w, r, "/docs", http.StatusSeeOther)
		return
	}
	st := s.store.snapshot()
	var users []Skill
	for _, sk := range st.Skills {
		if sk.Handbook == slug {
			users = append(users, sk)
		}
	}
	var cos []Company
	for _, c := range st.Companies {
		if c.Profile == slug {
			cos = append(cos, c)
		}
	}
	h.Back = "/handbooks/" + slug
	s.page(w, "learning", map[string]any{"Template": "handbook", "HB": h, "Users": users, "Cos": cos})
}

func (s *Server) topicLink(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	slug := field(r, "handbook")
	if slug != "" && s.loadHandbook(slug) == nil {
		flash(w, "err", "That handbook is not in the library.")
	} else {
		_ = s.store.mutate(func(st *State) {
			editByID(st.Skills, id, func(sk Skill) int { return sk.ID }, func(sk *Skill) { sk.Handbook = slug })
		})
	}
	http.Redirect(w, r, "/learning/skills/"+strconv.Itoa(id), http.StatusSeeOther)
}

func (s *Server) companyProfileLink(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	slug := field(r, "profile")
	if slug != "" && s.loadHandbook(slug) == nil {
		flash(w, "err", "That profile is not in the library.")
	} else {
		_ = s.store.mutate(func(st *State) {
			editByID(st.Companies, id, func(c Company) int { return c.ID }, func(c *Company) { c.Profile = slug })
		})
	}
	http.Redirect(w, r, "/learning/companies/"+strconv.Itoa(id), http.StatusSeeOther)
}

// safeBack returns a redirect target from the form, only within this app.
func safeBack(r *http.Request, fallback string) string {
	b := field(r, "back")
	if strings.HasPrefix(b, "/learning/") || strings.HasPrefix(b, "/handbooks/") {
		return b
	}
	return fallback
}

func (s *Server) editRefs(w http.ResponseWriter, r *http.Request, change func(lines []string) ([]string, string)) {
	slug := r.PathValue("slug")
	back := safeBack(r, "/handbooks/"+slug)
	if !slugRE.MatchString(slug) {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	raw, err := os.ReadFile(s.hbPath(slug))
	if err != nil {
		flash(w, "err", "That handbook is gone.")
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	lines, msg := change(strings.Split(strings.TrimRight(string(raw), "\n"), "\n"))
	if msg != "" && strings.HasPrefix(msg, "err|") {
		flash(w, "err", strings.TrimPrefix(msg, "err|"))
	} else if err := s.saveHandbook(slug, strings.Join(lines, "\n")); err != nil {
		flash(w, "err", err.Error())
	} else if msg != "" {
		flash(w, "ok", msg)
	}
	http.Redirect(w, r, back+"#references", http.StatusSeeOther)
}

// refAdd appends a reference to the handbook's References section, making
// the section when there is none.
func (s *Server) refAdd(w http.ResponseWriter, r *http.Request) {
	title, u, note := field(r, "title"), webURL(field(r, "url")), field(r, "note")
	s.editRefs(w, r, func(lines []string) ([]string, string) {
		if title == "" || safeURL(u) == "" {
			return lines, "err|A reference needs a title and a link."
		}
		row := "- [" + strings.NewReplacer("[", "(", "]", ")").Replace(title) + "](" + strings.ReplaceAll(u, " ", "%20") + ")"
		if note != "" {
			row += " - " + note
		}
		at := -1
		for i, l := range lines {
			if strings.EqualFold(strings.TrimSpace(l), "## References") {
				at = i
			}
		}
		if at < 0 {
			return append(lines, "", "## References", "", row), "Added " + title + "."
		}
		end := at + 1
		for end < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[end]), "## ") {
			end++
		}
		for end > at+1 && strings.TrimSpace(lines[end-1]) == "" {
			end--
		}
		out := append(append(append([]string{}, lines[:end]...), row), lines[end:]...)
		return out, "Added " + title + "."
	})
}

func (s *Server) refDelete(w http.ResponseWriter, r *http.Request) {
	u := field(r, "url")
	s.editRefs(w, r, func(lines []string) ([]string, string) {
		var out []string
		for _, l := range lines {
			if m := mdRefRow.FindStringSubmatch(strings.TrimSpace(l)); m != nil && m[2] == u {
				continue
			}
			out = append(out, l)
		}
		return out, "Removed the reference."
	})
}

const handbookFormat = "Write in this Markdown subset only: one '# Title' first, a two or three sentence summary paragraph, '## ' and '### ' headings, paragraphs, bold, italic, inline code, one-level bullet or numbered lists, fenced code blocks with a language, pipe tables with a header row, callouts as one-line blockquotes starting with **Tip:**, **Warning:**, or **Note:**, and https links. No HTML, no images, no nested lists, no em dashes or en dashes. Active voice, second person."

func (s *Server) topicGenerate(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	st := s.store.snapshot()
	sk, co, ok := st.skill(id)
	back := "/learning/skills/" + strconv.Itoa(id)
	if !ok {
		http.Redirect(w, r, "/learning", http.StatusSeeOther)
		return
	}
	prompt := "Write a practical technical handbook, in the spirit of an O'Reilly handbook, on: " + sk.Name +
		". The reader is a retiring Army senior NCO moving into platform engineering and defense integration, aiming for " + cmpOr(co.Role, "an engineering role") + " at " + cmpOr(co.Name, "a defense company") + ". " +
		cmpOr(sk.Notes, "") + "\n\nSections, in order: ## Why It Matters Here, ## Core Concepts, ## How It Works, ## Hands-On Lab (real commands only), ## Operating and Troubleshooting, ## Interview Questions (each question a ### heading with a short answer paragraph), ## Check Yourself, ## Cheat Sheet (a table), ## References (6-12 lines, each exactly '- [Title](https://url) - what it is good for', official sources, only URLs you are certain exist).\n\n" +
		"Never invent commands, flags, versions, or message formats. If the topic is a distribution-controlled standard, cover only public information and say where the real document comes from. 2,500-4,000 words.\n\n" + handbookFormat +
		"\n\nRespond with ONLY the Markdown document."
	s.generateDoc(w, r, prompt, back, func(st *State, slug string) {
		editByID(st.Skills, id, func(k Skill) int { return k.ID }, func(k *Skill) { k.Handbook = slug })
	}, sk.Handbook, "", sk.Name)
}

func (s *Server) companyProfileGenerate(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	st := s.store.snapshot()
	var co Company
	for _, c := range st.Companies {
		if c.ID == id {
			co = c
		}
	}
	back := "/learning/companies/" + strconv.Itoa(id)
	if co.ID == 0 {
		http.Redirect(w, r, "/learning", http.StatusSeeOther)
		return
	}
	prompt := "Write a company profile of " + co.Name + " for a job seeker aiming for " + cmpOr(co.Role, "an engineering role") + " there. " + cmpOr(co.Notes, "") +
		"\n\nSections, in order: ## What They Do, ## Customers and Programs, ## Technology, ## People and Culture, ## How to Get Hired There, ## Recommended Books (5-10 real published books, each '- [Title by Author (Publisher, Year)](https://url) - why it helps'), ## References (each '- [Title](https://url) - what it is good for').\n\n" +
		"Only state what you know to be true, say how current your knowledge is, and mark anything uncertain. Never invent books, people, contracts, or URLs. 1,200-2,000 words.\n\n" + handbookFormat +
		"\n\nRespond with ONLY the Markdown document."
	s.generateDoc(w, r, prompt, back, func(st *State, slug string) {
		editByID(st.Companies, id, func(c Company) int { return c.ID }, func(c *Company) { c.Profile = slug })
	}, co.Profile, "company_", co.Name)
}

// generateDoc runs a Generate for a handbook or profile, saves it (over the
// linked one, or under a new name), and links it.
func (s *Server) generateDoc(w http.ResponseWriter, r *http.Request, prompt, back string, link func(*State, string), current, prefix, name string) {
	st := s.store.snapshot()
	if !st.aiReady() {
		flash(w, "err", "Choose Claude or ChatGPT for the Advisor in Settings first; Generate runs through it.")
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	out, err := askAIFunc(r.Context(), st.Settings.AdvisorProvider, prompt, "")
	out = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(out), "```markdown"), "```"))
	if err == nil && !strings.HasPrefix(out, "# ") {
		if i := strings.Index(out, "\n# "); i >= 0 {
			out = out[i+1:]
		} else {
			err = fmt.Errorf("the Advisor did not send back a document; try again")
		}
	}
	if err != nil {
		flash(w, "err", err.Error())
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	slug := current
	if s.loadHandbook(slug) == nil {
		slug = s.freeSlug(prefix, name)
	}
	if err := s.saveHandbook(slug, out); err != nil {
		flash(w, "err", err.Error())
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	_ = s.store.mutate(func(st *State) { link(st, slug) })
	flash(w, "ok", "Written "+time.Now().Format("Jan 2")+". Check the facts and references before you rely on it.")
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// dropLead removes the title and the summary paragraph under it; the page
// shows both above the body already.
func dropLead(src string) string {
	lines := strings.Split(src, "\n")
	i := 0
	for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "# ") {
		i++
	}
	if i == len(lines) {
		return src
	}
	i++
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	for i < len(lines) && strings.TrimSpace(lines[i]) != "" && !strings.HasPrefix(strings.TrimSpace(lines[i]), "#") {
		i++
	}
	return strings.Join(lines[i:], "\n")
}
