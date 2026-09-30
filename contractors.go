package main

// Defense contractors: search employers' own career sites from the Jobs page,
// with no API keys. Most large contractors post on Workday, whose career
// sites answer a public JSON search; newer defense tech companies post on
// Greenhouse or Lever, which publish their boards as JSON on purpose. Each
// company is one request, run alongside USAJOBS and Adzuna and cached for 30
// minutes. Results open the posting on the company's own site, where the
// application happens.
//
// Employers without a public search (Lockheed Martin, RTX, L3Harris, BAE,
// SAIC, Peraton, ManTech) appear as links instead.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

type employer struct {
	Key, Name string
	Kind      string // workday | greenhouse | lever
	Tenant    string // workday tenant, greenhouse board, or lever company
	Host      string // workday: wd1, wd5, ...
	Site      string // workday career site
}

var employers = []employer{
	{"gdit", "GDIT", "workday", "gdit", "wd5", "External_Career_Site"},
	{"bah", "Booz Allen", "workday", "bah", "wd1", "BAH_Jobs"},
	{"caci", "CACI", "workday", "caci", "wd1", "External"},
	{"leidos", "Leidos", "workday", "leidos", "wd5", "External"},
	{"ngc", "Northrop Grumman", "workday", "ngc", "wd1", "Northrop_Grumman_External_Site"},
	{"parsons", "Parsons", "workday", "parsons", "wd5", "Search"},
	{"boeing", "Boeing", "workday", "boeing", "wd1", "EXTERNAL_CAREERS"},
	{"kbr", "KBR", "workday", "kbr", "wd5", "kbr_careers"},
	{"anduril", "Anduril", "greenhouse", "andurilindustries", "", ""},
	{"defenseunicorns", "Defense Unicorns", "greenhouse", "defenseunicorns", "", ""},
	{"epirus", "Epirus", "greenhouse", "epirus", "", ""},
	{"palantir", "Palantir", "lever", "palantir", "", ""},
	{"shieldai", "Shield AI", "lever", "shieldai", "", ""},
}

// contractorLinks are big employers whose career sites have no public search.
var contractorLinks = []jobLink{
	{"Lockheed Martin", "https://www.lockheedmartinjobs.com/", ""},
	{"RTX (Raytheon)", "https://careers.rtx.com/", ""},
	{"L3Harris", "https://careers.l3harris.com/", ""},
	{"BAE Systems", "https://jobs.baesystems.com/", ""},
	{"SAIC", "https://jobs.saic.com/", ""},
	{"Peraton", "https://careers.peraton.com/", ""},
	{"ManTech", "https://careers.mantech.com/", ""},
}

// employersOn are the employers to search: all of them, minus the ones
// switched off, so a newly added employer is searched by default.
func employersOn(s Settings) []employer {
	var out []employer
	for _, e := range employers {
		if !slices.Contains(s.EmployersOff, e.Key) {
			out = append(out, e)
		}
	}
	return out
}

// postJSON is getJSON for the Workday search, which is a POST.
func postJSON(u string, body any, ttl time.Duration) ([]byte, error) {
	b, _ := json.Marshal(body)
	key := u + "|" + string(b)
	mapMu.Lock()
	if c, ok := mapCache[key]; ok && time.Since(c.at) < ttl {
		mapMu.Unlock()
		return c.body, nil
	}
	mapMu.Unlock()
	req, _ := http.NewRequest("POST", u, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ARW-Army-Retirement-Workbench/1.0 (+https://github.com/joshkor1982/retirement-workbench)")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("answered %d", resp.StatusCode)
	}
	mapMu.Lock()
	mapCache[key] = cached{time.Now(), buf.Bytes()}
	mapMu.Unlock()
	return buf.Bytes(), nil
}

// workdayPosted turns "Posted 3 Days Ago" into a date.
func workdayPosted(s string, now time.Time) string {
	l := strings.ToLower(s)
	days := 0
	switch {
	case strings.Contains(l, "today"):
	case strings.Contains(l, "yesterday"):
		days = 1
	default:
		f := strings.Fields(strings.TrimPrefix(l, "posted "))
		if len(f) > 0 {
			days, _ = strconv.Atoi(strings.TrimSuffix(f[0], "+"))
		}
	}
	return now.AddDate(0, 0, -days).Format("2006-01-02")
}

// matchesWords is the keyword test for boards searched locally: every word
// of the query appears in the text.
func matchesWords(q, text string) bool {
	text = strings.ToLower(text)
	for _, w := range strings.Fields(strings.ToLower(q)) {
		if !strings.Contains(text, w) {
			return false
		}
	}
	return true
}

// employerSearch is searchEmployer, swapped out in tests so they never reach
// the companies' sites.
var employerSearch = searchEmployer

func searchEmployer(e employer, q string) ([]jobHit, error) {
	now := time.Now()
	var hits []jobHit
	switch e.Kind {
	case "workday":
		base := fmt.Sprintf("https://%s.%s.myworkdayjobs.com", e.Tenant, e.Host)
		body, err := postJSON(fmt.Sprintf("%s/wday/cxs/%s/%s/jobs", base, e.Tenant, e.Site),
			map[string]any{"appliedFacets": map[string]any{}, "limit": 20, "offset": 0, "searchText": q}, 30*time.Minute)
		if err != nil {
			return nil, err
		}
		var d struct {
			JobPostings []struct {
				Title, ExternalPath, LocationsText, PostedOn string
			} `json:"jobPostings"`
		}
		if err := json.Unmarshal(body, &d); err != nil {
			return nil, fmt.Errorf("unreadable reply")
		}
		for _, p := range d.JobPostings {
			hits = append(hits, jobHit{Title: p.Title, Org: e.Name, Location: p.LocationsText,
				URL: base + "/" + e.Site + p.ExternalPath, Posted: workdayPosted(p.PostedOn, now), Source: e.Name})
		}
	case "greenhouse":
		body, err := getJSON("https://boards-api.greenhouse.io/v1/boards/"+e.Tenant+"/jobs", 30*time.Minute)
		if err != nil {
			return nil, err
		}
		var d struct {
			Jobs []struct {
				Title    string                `json:"title"`
				URL      string                `json:"absolute_url"`
				Updated  string                `json:"updated_at"`
				Location struct{ Name string } `json:"location"`
			} `json:"jobs"`
		}
		if err := json.Unmarshal(body, &d); err != nil {
			return nil, fmt.Errorf("unreadable reply")
		}
		for _, j := range d.Jobs {
			if matchesWords(q, j.Title) {
				hits = append(hits, jobHit{Title: j.Title, Org: e.Name, Location: strings.TrimSpace(j.Location.Name), URL: j.URL,
					Posted: firstN(j.Updated, 10), Source: e.Name})
			}
		}
	case "lever":
		body, err := getJSON("https://api.lever.co/v0/postings/"+e.Tenant+"?mode=json", 30*time.Minute)
		if err != nil {
			return nil, err
		}
		var d []struct {
			Text       string `json:"text"`
			HostedURL  string `json:"hostedUrl"`
			CreatedAt  int64  `json:"createdAt"`
			Workplace  string `json:"workplaceType"`
			Categories struct {
				Location, Team string
			} `json:"categories"`
		}
		if err := json.Unmarshal(body, &d); err != nil {
			return nil, fmt.Errorf("unreadable reply")
		}
		for _, j := range d {
			if matchesWords(q, j.Text+" "+j.Categories.Team) {
				loc := j.Categories.Location
				if j.Workplace == "remote" && !strings.Contains(strings.ToLower(loc), "remote") {
					loc = strings.Trim(loc+" (remote)", " ")
				}
				hits = append(hits, jobHit{Title: j.Text, Org: e.Name, Location: loc, URL: j.HostedURL,
					Posted: time.UnixMilli(j.CreatedAt).Format("2006-01-02"), Source: e.Name})
			}
		}
	}
	// Boards searched locally can match hundreds (Anduril lists thousands):
	// keep each company to its newest 25 so no one employer drowns the rest.
	slices.SortStableFunc(hits, func(a, b jobHit) int { return strings.Compare(b.Posted, a.Posted) })
	if len(hits) > 25 {
		hits = hits[:25]
	}
	return hits, nil
}

func isRemoteText(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "remote") || strings.Contains(l, "telework") || strings.Contains(l, "virtual")
}

// inState reports whether a posting's location names the ZIP's state, or
// lists several locations Workday does not spell out ("3 Locations").
func inState(loc, st string) bool {
	if st == "" || strings.Contains(loc, "Locations") {
		return true
	}
	if name := stateNames[st]; name != "" && strings.Contains(strings.ToLower(loc), strings.ToLower(name)) {
		return true
	}
	for _, f := range strings.FieldsFunc(loc, func(r rune) bool { return strings.ContainsRune(" ,-/()", r) }) {
		if f == st {
			return true
		}
	}
	return false
}

// filterByMode applies the work type to contractor postings, which have no
// radius search: near means in the ZIP's state, remote means the posting
// says remote, both keeps either, and anywhere keeps everything.
func filterByMode(hits []jobHit, mode, st string) []jobHit {
	out := hits[:0]
	for _, h := range hits {
		h.Remote = isRemoteText(h.Location) || isRemoteText(h.Title)
		keep := true
		switch mode {
		case "near":
			keep = inState(h.Location, st)
		case "remote":
			keep = h.Remote
		case "both":
			keep = h.Remote || inState(h.Location, st)
		}
		if keep {
			out = append(out, h)
		}
	}
	return out
}

func employerOnSet(s Settings) map[string]bool {
	m := map[string]bool{}
	for _, e := range employersOn(s) {
		m[e.Key] = true
	}
	return m
}

// employersSave stores which contractors to leave out, so any employer
// added in a later version is searched until switched off.
func (s *Server) employersSave(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	on := map[string]bool{}
	for _, k := range r.Form["on"] {
		on[k] = true
	}
	var off []string
	for _, e := range employers {
		if !on[e.Key] {
			off = append(off, e.Key)
		}
	}
	_ = s.store.mutate(func(st *State) { st.Settings.EmployersOff = off })
	flash(w, "ok", fmt.Sprintf("Searching %d of %d defense contractors.", len(employers)-len(off), len(employers)))
	back := field(r, "back")
	if !strings.HasPrefix(back, "/jobs") {
		back = "/jobs"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
