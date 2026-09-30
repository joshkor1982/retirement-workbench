package main

// Job search across sources. Results come from official APIs only: USAJOBS
// for federal jobs and Adzuna, a job aggregator with a free key, for
// everything else. LinkedIn, Indeed, and the rest offer no public search API
// and forbid scraping, so for those the app builds one-click searches that
// open on the site itself with your keywords filled in.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type jobLink struct{ Name, URL, Note string }

// jobQuery is one search: keywords, where, and whether remote work counts.
type jobQuery struct {
	Q      string
	Zip    string // five-digit US ZIP code
	Radius int    // miles around the ZIP
	Mode   string // near | remote | both | anywhere
	Where  string // free-text location, kept for searches saved by older builds
	City   string // "Colorado Springs, CO", looked up from the ZIP
}

func parseJobQuery(q, zip, radius, mode, where string) jobQuery {
	jq := jobQuery{Q: strings.TrimSpace(q), Zip: strings.TrimSpace(zip), Where: strings.TrimSpace(where), Mode: mode}
	if len(jq.Zip) != 5 || strings.Trim(jq.Zip, "0123456789") != "" {
		jq.Zip = ""
	}
	jq.Radius, _ = strconv.Atoi(radius)
	if jq.Radius <= 0 || jq.Radius > 200 {
		jq.Radius = 25
	}
	switch jq.Mode {
	case "near", "remote", "both", "anywhere":
	default:
		jq.Mode = "both"
	}
	if jq.Zip == "" && jq.Where == "" && jq.Mode != "remote" {
		// Nowhere to be near: search the whole country. Remote-only would
		// find almost nothing on USAJOBS, where few postings are remote.
		jq.Mode = "anywhere"
	}
	return jq
}

// place is the human name for "near": the ZIP's city, the ZIP, or free text.
func (jq jobQuery) place() string {
	switch {
	case jq.City != "":
		return jq.City + " " + jq.Zip
	case jq.Zip != "":
		return jq.Zip
	}
	return jq.Where
}

var (
	zipMu    sync.Mutex
	zipCache = map[string]string{}
)

// zipCity turns a ZIP code into "City, ST" with zippopotam.us (free, no key),
// because USAJOBS searches by place name, not ZIP.
func zipCity(zip string) string {
	zipMu.Lock()
	if c, ok := zipCache[zip]; ok {
		zipMu.Unlock()
		return c
	}
	zipMu.Unlock()
	client := &http.Client{Timeout: 6 * time.Second}
	resp, err := client.Get("https://api.zippopotam.us/us/" + zip)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var body struct {
		Places []struct {
			Name  string `json:"place name"`
			State string `json:"state abbreviation"`
		} `json:"places"`
	}
	city := ""
	if resp.StatusCode == 200 && json.NewDecoder(resp.Body).Decode(&body) == nil && len(body.Places) > 0 {
		city = body.Places[0].Name + ", " + body.Places[0].State
	}
	zipMu.Lock()
	zipCache[zip] = city
	zipMu.Unlock()
	return city
}

type jobLinkRow struct {
	Label string
	Links []jobLink
}

// jobLinkRows builds one-click searches on the big boards: one row for jobs
// near the ZIP, one for remote jobs, per the chosen work type.
func jobLinkRows(jq jobQuery) []jobLinkRow {
	if jq.Q == "" {
		return nil
	}
	e := url.QueryEscape
	q := jq.Q
	var rows []jobLinkRow
	if jq.Mode == "anywhere" {
		us := "United States"
		rows = append(rows, jobLinkRow{"Anywhere in the US", []jobLink{
			{"LinkedIn", "https://www.linkedin.com/jobs/search/?keywords=" + e(q) + "&location=" + e(us), ""},
			{"Indeed", "https://www.indeed.com/jobs?q=" + e(q) + "&l=" + e(us), ""},
			{"ClearanceJobs", "https://www.clearancejobs.com/jobs?keywords=" + e(q), "cleared jobs"},
			{"Glassdoor", "https://www.glassdoor.com/Job/jobs.htm?sc.keyword=" + e(q), ""},
			{"ZipRecruiter", "https://www.ziprecruiter.com/jobs-search?search=" + e(q), ""},
			{"Dice", "https://www.dice.com/jobs?q=" + e(q), "tech"},
			{"Google Jobs", "https://www.google.com/search?ibp=htl;jobs&q=" + e(q+" jobs"), "every board at once"},
			{"USAJOBS", "https://www.usajobs.gov/search/results/?k=" + e(q), "federal"},
		}})
	}
	if jq.Mode == "near" || jq.Mode == "both" {
		loc := jq.Zip
		if loc == "" {
			loc = jq.Where
		}
		r := strconv.Itoa(jq.Radius)
		near := "Near " + jq.place()
		if jq.Zip != "" {
			near += " (" + r + " miles)"
		}
		rows = append(rows, jobLinkRow{near, []jobLink{
			{"LinkedIn", "https://www.linkedin.com/jobs/search/?keywords=" + e(q) + "&location=" + e(loc) + "&distance=" + r, ""},
			{"Indeed", "https://www.indeed.com/jobs?q=" + e(q) + "&l=" + e(loc) + "&radius=" + r, ""},
			{"ClearanceJobs", "https://www.clearancejobs.com/jobs?keywords=" + e(q) + "&location=" + e(loc), "cleared jobs"},
			{"Glassdoor", "https://www.glassdoor.com/Job/jobs.htm?sc.keyword=" + e(q) + "&locKeyword=" + e(loc), ""},
			{"ZipRecruiter", "https://www.ziprecruiter.com/jobs-search?search=" + e(q) + "&location=" + e(loc) + "&radius=" + r, ""},
			{"Dice", "https://www.dice.com/jobs?q=" + e(q) + "&location=" + e(loc), "tech"},
			{"Google Jobs", "https://www.google.com/search?ibp=htl;jobs&q=" + e(q+" jobs near "+loc), "every board at once"},
			{"USAJOBS", "https://www.usajobs.gov/search/results/?k=" + e(q) + "&l=" + e(firstNonEmpty(jq.City, loc)), "federal"},
		}})
	}
	if jq.Mode == "remote" || jq.Mode == "both" {
		rows = append(rows, jobLinkRow{"Remote", []jobLink{
			{"LinkedIn", "https://www.linkedin.com/jobs/search/?keywords=" + e(q) + "&location=United%20States&f_WT=2", ""},
			{"Indeed", "https://www.indeed.com/jobs?q=" + e(q) + "&l=Remote", ""},
			{"ClearanceJobs", "https://www.clearancejobs.com/jobs?keywords=" + e(q+" remote"), "cleared jobs"},
			{"Glassdoor", "https://www.glassdoor.com/Job/jobs.htm?sc.keyword=" + e(q) + "&remoteWorkType=1", ""},
			{"ZipRecruiter", "https://www.ziprecruiter.com/jobs-search?search=" + e(q) + "&location=Remote", ""},
			{"Dice", "https://www.dice.com/jobs?q=" + e(q) + "&filters.isRemote=true", "tech"},
			{"Google Jobs", "https://www.google.com/search?ibp=htl;jobs&q=" + e("remote "+q+" jobs"), "every board at once"},
			{"USAJOBS", "https://www.usajobs.gov/search/results/?k=" + e(q+" remote"), "federal"},
		}})
	}
	return rows
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

type jobResults struct {
	Hits  []jobHit
	Errs  []string
	Notes []string // e.g. which federal title a search was retried with
}

// fedTitles maps civilian job titles to the title USAJOBS postings use, for a
// retry when the civilian words find nothing. Federal IT work is posted as
// "IT Specialist" (series 2210), whatever the private sector calls it.
var fedTitles = []struct {
	civ []string
	fed string
}{
	{[]string{"cybersecurity", "cyber security", "security engineer", "information security", "security analyst"}, "IT Specialist INFOSEC"},
	{[]string{"platform engineer", "devops", "site reliability", "sre", "systems engineer", "system administrator", "systems administrator",
		"sysadmin", "software engineer", "software developer", "cloud engineer", "network engineer", "kubernetes", "infrastructure engineer"}, "IT Specialist"},
	{[]string{"data engineer", "data analyst"}, "IT Specialist DATAMGT"},
	{[]string{"project manager", "program manager"}, "Program Manager"},
}

func fedTitle(q string) string {
	l := strings.ToLower(q)
	for _, m := range fedTitles {
		for _, c := range m.civ {
			if strings.Contains(l, c) {
				return m.fed
			}
		}
	}
	return ""
}

// searchJobs runs every configured source in parallel, near and remote per
// the work type, then merges, drops duplicates, and sorts newest first.
func searchJobs(cfg Settings, jq *jobQuery) jobResults {
	if jq.Zip != "" {
		jq.City = zipCity(jq.Zip)
	}
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out jobResults
	)
	run := func(name string, remote bool, fn func() ([]jobHit, error)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hits, err := fn()
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				out.Errs = append(out.Errs, name+": "+err.Error())
				return
			}
			for i := range hits {
				hits[i].Remote = hits[i].Remote || remote
			}
			out.Hits = append(out.Hits, hits...)
		}()
	}
	near := jq.Mode == "near" || jq.Mode == "both" || jq.Mode == "anywhere"
	remote := jq.Mode == "remote" || jq.Mode == "both"
	// usajobs retries with the federal title when the civilian one finds nothing.
	usajobs := func(v url.Values) ([]jobHit, error) {
		hits, err := usajobsSearch(cfg, jq.Q, v)
		if err == nil && len(hits) == 0 {
			if ft := fedTitle(jq.Q); ft != "" {
				if hits, err = usajobsSearch(cfg, ft, v); err == nil && len(hits) > 0 {
					mu.Lock()
					out.Notes = append(out.Notes, fmt.Sprintf("USAJOBS had nothing for %q, so these are for %q, the title federal agencies use.", jq.Q, ft))
					mu.Unlock()
				}
			}
		}
		return hits, err
	}
	if cfg.USAJobsKey != "" {
		if near {
			v := url.Values{}
			if jq.Mode != "anywhere" {
				if loc := firstNonEmpty(jq.City, jq.Where); loc != "" {
					v.Set("LocationName", loc)
					if jq.City != "" {
						v.Set("Radius", strconv.Itoa(jq.Radius))
					}
				}
			}
			run("USAJOBS", false, func() ([]jobHit, error) { return usajobs(v) })
		}
		if remote {
			run("USAJOBS remote", true, func() ([]jobHit, error) {
				return usajobs(url.Values{"RemoteIndicator": {"True"}})
			})
		}
	}
	if cfg.AdzunaID != "" && cfg.AdzunaKey != "" {
		if near {
			run("Adzuna", false, func() ([]jobHit, error) {
				return adzunaSearch(cfg, jq.Q, firstNonEmpty(jq.Zip, jq.Where), jq.Radius)
			})
		}
		if remote {
			// Adzuna has no remote filter; "remote" in the search is the closest match.
			run("Adzuna remote", true, func() ([]jobHit, error) { return adzunaSearch(cfg, jq.Q+" remote", "", 0) })
		}
	}
	wg.Wait()
	seen := map[string]bool{}
	merged := out.Hits[:0]
	for _, h := range out.Hits {
		if h.URL != "" && seen[h.URL] {
			continue
		}
		seen[h.URL] = true
		merged = append(merged, h)
	}
	out.Hits = merged
	sort.SliceStable(out.Hits, func(i, j int) bool { return out.Hits[i].Posted > out.Hits[j].Posted })
	sort.Strings(out.Errs)
	return out
}

func adzunaSearch(cfg Settings, q, where string, radiusMiles int) ([]jobHit, error) {
	v := url.Values{
		"app_id": {cfg.AdzunaID}, "app_key": {cfg.AdzunaKey},
		"what": {q}, "results_per_page": {"30"}, "content-type": {"application/json"},
		"sort_by": {"date"},
	}
	if where != "" {
		v.Set("where", where)
		if radiusMiles > 0 {
			v.Set("distance", strconv.Itoa(radiusMiles*1609/1000)) // Adzuna measures in kilometers
		}
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get("https://api.adzuna.com/v1/api/jobs/us/search/1?" + v.Encode())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Display string `json:"display"`
		Results []struct {
			Title    string  `json:"title"`
			URL      string  `json:"redirect_url"`
			Created  string  `json:"created"`
			SalaryLo float64 `json:"salary_min"`
			SalaryHi float64 `json:"salary_max"`
			Company  struct {
				Name string `json:"display_name"`
			} `json:"company"`
			Location struct {
				Name string `json:"display_name"`
			} `json:"location"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("unreadable reply (%s)", resp.Status)
	}
	if resp.StatusCode != 200 {
		if resp.StatusCode == 401 {
			return nil, fmt.Errorf("the App ID or App Key in Settings is wrong")
		}
		return nil, fmt.Errorf("%s %s", resp.Status, body.Display)
	}
	var hits []jobHit
	for _, r := range body.Results {
		h := jobHit{Title: cleanAdzuna(r.Title), Org: r.Company.Name, Location: r.Location.Name, URL: r.URL, Source: "Adzuna"}
		h.Remote = strings.Contains(strings.ToLower(h.Title+" "+h.Location), "remote")
		if len(r.Created) >= 10 {
			h.Posted = r.Created[:10]
		}
		if r.SalaryLo > 0 {
			h.Pay = dollars(int(r.SalaryLo))
			if r.SalaryHi > r.SalaryLo {
				h.Pay += " to " + dollars(int(r.SalaryHi))
			}
		}
		hits = append(hits, h)
	}
	return hits, nil
}

// cleanAdzuna drops the <strong> highlight tags Adzuna puts around matches.
func cleanAdzuna(s string) string {
	return strings.NewReplacer("<strong>", "", "</strong>", "").Replace(s)
}
