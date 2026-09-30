package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseMoney(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"84.99", 8499, true},
		{"$1,250", 125000, true},
		{"-300", -30000, true},
		{"-$12.5", -1250, true},
		{".50", 50, true},
		{"5.", 500, true},
		{"1.999", 200, true}, // rounds half up
		{"1.994", 199, true},
		{"--5", 0, false},
		{"12abc", 0, false},
		{"1.2.3", 0, false},
		{"", 0, false},
		{"$", 0, false},
		{"9999999999999", 0, false}, // over a trillion dollars
	}
	for _, c := range cases {
		got, err := parseMoney(c.in)
		if (err == nil) != c.ok || (c.ok && got != c.want) {
			t.Errorf("parseMoney(%q) = %d, %v; want %d, ok=%v", c.in, got, err, c.want, c.ok)
		}
	}
}

func TestMoneyFormat(t *testing.T) {
	for in, want := range map[int64]string{
		0: "$0.00", 105000: "$1,050.00", -20000: "-$200.00", 123456789: "$1,234,567.89", 99: "$0.99",
	} {
		if got := money(in); got != want {
			t.Errorf("money(%d) = %q, want %q", in, got, want)
		}
	}
	// Whatever money() prints, parseMoney must read back (inputs are pre-filled with it).
	for _, c := range []int64{0, 99, 105000, -20000, 123456789} {
		if back, err := parseMoney(money(c)); err != nil || back != c {
			t.Errorf("round trip %d -> %q -> %d, %v", c, money(c), back, err)
		}
	}
}

func TestCombinedRating(t *testing.T) {
	cases := []struct {
		items []AnalyzedCondition
		want  int
	}{
		{nil, 0},
		{[]AnalyzedCondition{{Percent: 10}}, 10},
		{[]AnalyzedCondition{{Percent: 50}, {Percent: 30}}, 70}, // 65 rounds to 70
		{[]AnalyzedCondition{{Percent: 60}, {Percent: 40}}, 80}, // 76 rounds to 80
	}
	for _, c := range cases {
		if got := combinedRating(c.items); got != c.want {
			t.Errorf("combinedRating(%v) = %d, want %d", c.items, got, c.want)
		}
	}
}

func TestSavingsSummary(t *testing.T) {
	month := time.Now().Format("2006-01")
	st := State{Settings: Settings{SavingsGoal: 1000000, SavingsGoalName: "Move fund"}}
	st.Savings = []SavingsEntry{
		{ID: 1, Date: month + "-01", Amount: 125000},
		{ID: 2, Date: month + "-02", Amount: -20000},
		{ID: 3, Date: "2020-01-01", Amount: 5000},
	}
	sum := summarizeSavings(st)
	if sum.Saved != 110000 || sum.ThisMonth != 105000 || sum.Pct != 11 || sum.Left != 890000 {
		t.Fatalf("got saved=%d month=%d pct=%d left=%d", sum.Saved, sum.ThisMonth, sum.Pct, sum.Left)
	}
	if sum.Entries[0].ID != 2 {
		t.Errorf("entries should be newest first, got %d first", sum.Entries[0].ID)
	}
	if none := summarizeSavings(State{}); none.Pct != -1 {
		t.Errorf("no goal should report Pct -1, got %d", none.Pct)
	}
}

func TestDocPathSurvivesAMove(t *testing.T) {
	s := &Server{data: "/somewhere/else"}
	for stored, want := range map[string]string{
		"data/docs/1-a.pdf": "/somewhere/else/docs/1-a.pdf", // written by an older build
		"docs/1-a.pdf":      "/somewhere/else/docs/1-a.pdf",
	} {
		if got := s.docPath(stored); got != filepath.FromSlash(want) {
			t.Errorf("docPath(%q) = %q, want %q", stored, got, want)
		}
	}
	if got := s.relDoc(filepath.Join("/somewhere/else", "docs", "2-b.pdf")); got != "docs/2-b.pdf" {
		t.Errorf("relDoc = %q", got)
	}
}

func TestResolveDataDir(t *testing.T) {
	if dir, why := resolveDataDir("/x"); dir != "/x" || why != "-data flag" {
		t.Errorf("flag: %s %s", dir, why)
	}
	t.Setenv("RW_DATA", "/y")
	if dir, _ := resolveDataDir(""); dir != "/y" {
		t.Errorf("env: %s", dir)
	}
}

func TestGuard(t *testing.T) {
	ok := guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	try := func(method, host string, hdr map[string]string) int {
		path := "/settings/erase"
		if host == "10.1.2.3:5252" {
			path = "/healthz"
		}
		r := httptest.NewRequest(method, "http://"+host+path, nil)
		r.Host = host
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		ok.ServeHTTP(w, r)
		return w.Code
	}
	checks := []struct {
		name   string
		method string
		host   string
		hdr    map[string]string
		want   int
	}{
		{"page GET", "GET", "127.0.0.1:5252", nil, 204},
		{"localhost GET", "GET", "localhost:5252", nil, 204},
		{"DNS rebinding", "GET", "evil.example:5252", nil, http.StatusMisdirectedRequest},
		{"own form", "POST", "127.0.0.1:5252", map[string]string{"Sec-Fetch-Site": "same-origin"}, 204},
		{"cross-site form", "POST", "127.0.0.1:5252", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"foreign Origin", "POST", "127.0.0.1:5252", map[string]string{"Origin": "https://evil.example"}, 403},
		{"null Origin", "POST", "127.0.0.1:5252", map[string]string{"Origin": "null"}, 403},
		{"curl on this machine", "POST", "127.0.0.1:5252", nil, 204},
		{"probe by pod IP", "GET", "10.1.2.3:5252", nil, 200},
	}
	for _, c := range checks {
		if got := try(c.method, c.host, c.hdr); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

func TestAllowedHosts(t *testing.T) {
	t.Setenv("RW_ALLOWED_HOSTS", "Workbench.Example.com, other.lan")
	for host, want := range map[string]bool{
		"workbench.example.com": true, "workbench.example.com:443": true, "other.lan:5252": true,
		"evil.example": false, "127.0.0.1:5252": true,
	} {
		if got := allowedHost(host); got != want {
			t.Errorf("allowedHost(%q) = %v", host, got)
		}
	}
}

func TestParseAdvisorReply(t *testing.T) {
	ans, acts := parseAdvisorReply("Sure.\n{\"answer\":\"Added it.\",\"actions\":[{\"tool\":\"log_savings\",\"input\":{\"amount\":\"500\"}}]}")
	if ans != "Added it." || len(acts) != 1 || acts[0].Tool != "log_savings" {
		t.Fatalf("got %q %v", ans, acts)
	}
	if ans, acts := parseAdvisorReply("Just words."); ans != "Just words." || acts != nil {
		t.Errorf("plain text should pass through, got %q %v", ans, acts)
	}
}

func TestAvalanche(t *testing.T) {
	// Two debts with the same name must not share a payment.
	_, _, this, free, _, _, _, _, ok := avalancheProject([]Debt{
		{ID: 1, Name: "Visa", APR: "20%", Balance: 100000, Min: 5000},
		{ID: 2, Name: "Visa", APR: "10%", Balance: 100000, Min: 5000},
	}, 10000)
	if !ok || len(this) != 2 || this[0].Pay == this[1].Pay {
		t.Fatalf("same-name debts: %+v", this)
	}
	if !strings.Contains(free, "20") { // pays off in the 2020s or later, as a month name
		t.Logf("debt free: %s", free)
	}
	// Minimums below the interest never pay off; the page must say so.
	_, _, _, never, _, _, _, _, _ := avalancheProject([]Debt{{ID: 3, Name: "Loan", APR: "30%", Balance: 10000000, Min: 1000}}, 0)
	if !strings.HasPrefix(never, "not within") {
		t.Errorf("a debt that never pays off reported %q", never)
	}
}

func TestHouseSearchKeyIgnoresTypeOrder(t *testing.T) {
	a := HouseSearch{Zip: "80903", Radius: 25, Types: []string{"condos", "single_family"}}
	b := HouseSearch{Zip: "80903", Radius: 25, Types: []string{"single_family", "condos"}}
	if a.key() != b.key() {
		t.Error("filter order should not start a new baseline")
	}
}

// ---------- End-to-end journey ---------------------------------------------------

type journey struct {
	t   *testing.T
	srv *httptest.Server
	app *Server
	c   *http.Client
}

func newJourney(t *testing.T) *journey {
	t.Helper()
	// Journeys never reach real employers' career sites.
	employerSearch = func(employer, string) ([]jobHit, error) { return nil, nil }
	t.Cleanup(func() { employerSearch = searchEmployer })
	dir := t.TempDir()
	app, h, err := newApp(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &journey{t, srv, app, c}
}

func (j *journey) get(path string) (int, string) {
	j.t.Helper()
	resp, err := j.c.Get(j.srv.URL + path)
	if err != nil {
		j.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (j *journey) post(path string, form url.Values) int {
	j.t.Helper()
	req, _ := http.NewRequest("POST", j.srv.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := j.c.Do(req)
	if err != nil {
		j.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

var allPages = []string{"/", "/timeline", "/todos", "/notes", "/appointments", "/docs", "/medical",
	"/packet", "/budget", "/debt", "/savings", "/retired-pay", "/resume", "/itp", "/jobs", "/housing", "/home-team", "/resources", "/learning", "/settings"}

func TestJourneyFirstRunDemoAndErase(t *testing.T) {
	j := newJourney(t)

	// A brand-new user: every page renders empty, and no page reaches the network.
	for _, p := range allPages {
		if code, body := j.get(p); code != 200 {
			t.Fatalf("empty %s: %d %s", p, code, firstN(body, 200))
		}
	}

	// Load the fictional soldier and walk every page again.
	if code := j.post("/settings/demo", nil); code != 303 {
		t.Fatalf("demo: %d", code)
	}
	for _, p := range allPages {
		code, body := j.get(p)
		if code != 200 {
			t.Fatalf("demo %s: %d %s", p, code, firstN(body, 200))
		}
		if strings.Contains(body, "ZgotmplZ") {
			t.Errorf("%s rendered an unsafe value the template refused", p)
		}
	}
	if _, body := j.get("/"); !strings.Contains(body, "John Doe") && !strings.Contains(body, "days to go") {
		t.Error("dashboard does not show the demo soldier")
	}

	// Daily use: a task, a note, a savings deposit and goal, a PAR card.
	base := j.app.store.snapshot()
	j.post("/todos", url.Values{"title": {"Call the VSO"}, "due": {"2030-01-02"}})
	j.post("/notes", url.Values{"topic": {"TAP day one"}, "date": {"2030-01-01"}})
	j.post("/savings", url.Values{"kind": {"deposit"}, "amount": {"1,250"}})
	j.post("/savings/goal", url.Values{"name": {"Move fund"}, "goal": {"10000"}})
	j.post("/packet-par/doc-stp", url.Values{"status": {"done"}})
	st := j.app.store.snapshot()
	saved := summarizeSavings(st).Saved
	if len(st.Notes) != len(base.Notes)+1 || saved != summarizeSavings(base).Saved+125000 || st.PARDone["doc-stp"] == "" {
		t.Fatalf("daily use did not stick: notes=%d saved=%d par=%q", len(st.Notes), saved, st.PARDone["doc-stp"])
	}
	if _, body := j.get("/"); !strings.Contains(body, money(saved)) {
		t.Errorf("dashboard Money Saved tile should show %s", money(saved))
	}

	// Documents: upload, view, delete.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "orders.pdf")
	fw.Write([]byte("%PDF-1.4 test"))
	mw.Close()
	req, _ := http.NewRequest("POST", j.srv.URL+"/docs", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if resp, err := j.c.Do(req); err != nil || resp.StatusCode != 303 {
		t.Fatalf("upload: %v %v", resp, err)
	}
	st = j.app.store.snapshot()
	doc := st.Docs[len(st.Docs)-1]
	if strings.HasPrefix(doc.File, "data/") || filepath.IsAbs(doc.File) {
		t.Errorf("document path should be relative to the data folder, got %q", doc.File)
	}
	if code, body := j.get("/docs/" + itoa(doc.ID) + "/file"); code != 200 || !strings.HasPrefix(body, "%PDF") {
		t.Errorf("download: %d", code)
	}
	j.post("/docs/"+itoa(doc.ID)+"/delete", nil)
	if fileExists(j.app.docPath(doc.File)) {
		t.Error("deleted document's file is still on disk")
	}

	// A hostile page cannot erase anything.
	req, _ = http.NewRequest("POST", j.srv.URL+"/settings/erase", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if resp, _ := j.c.Do(req); resp.StatusCode != 403 {
		t.Errorf("cross-site erase: %d", resp.StatusCode)
	}
	if len(j.app.store.snapshot().Todos) == 0 {
		t.Fatal("cross-site erase went through")
	}

	// Erase Everything really erases, files included.
	if code := j.post("/settings/erase", nil); code != 303 {
		t.Fatalf("erase: %d", code)
	}
	if st := j.app.store.snapshot(); len(st.Todos) != 0 || len(st.Docs) != 0 {
		t.Error("state survived erase")
	}
	entries, _ := os.ReadDir(filepath.Join(j.app.data, "docs"))
	if len(entries) != 0 {
		t.Errorf("%d files survived erase", len(entries))
	}
}

func TestJourneyStateIsPrivateAndAtomic(t *testing.T) {
	j := newJourney(t)
	j.post("/notes", url.Values{"topic": {"x"}})
	info, err := os.Stat(filepath.Join(j.app.data, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("state.json is readable by others: %v", info.Mode().Perm())
	}
	var st State
	b, _ := os.ReadFile(filepath.Join(j.app.data, "state.json"))
	if err := json.Unmarshal(b, &st); err != nil || len(st.Notes) != 1 {
		t.Errorf("state on disk: %v, notes=%d", err, len(st.Notes))
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestLeaveProjection(t *testing.T) {
	day := func(s string) time.Time { d, _ := parseDay(s); return d }
	// 58 days in mid-August crosses 1 October at 63: 3 days are lost, then
	// four more months of accrual land at 70 by the start of terminal leave.
	at, lost := projectLeave(58, day("2026-08-15"), day("2027-03-01"))
	if at != 70 || lost != 3 {
		t.Errorf("got %v at terminal leave, %v lost; want 70 and 3", at, lost)
	}
	// Under the cap: no loss.
	if _, lost := projectLeave(20, day("2026-08-15"), day("2027-03-01")); lost != 0 {
		t.Errorf("under the cap lost %v", lost)
	}
	for in, ok := range map[string]bool{"42.5": true, "": true, "abc": false, "500": false} {
		if _, _, err := parseLeaveDays(in); (err == nil) != ok {
			t.Errorf("parseLeaveDays(%q) err=%v", in, err)
		}
	}
}

func TestJourneyResumePDF(t *testing.T) {
	j := newJourney(t)
	j.post("/settings/demo", nil)
	id := j.app.store.snapshot().ResumeTargets[0].ID
	for _, d := range resumeDesigns {
		j.post("/resume/targets/"+itoa(id)+"/style", url.Values{"design": {d[0]}, "dark": {"1"}})
		code, body := j.get("/resume/print?t=" + itoa(id))
		if code != 200 || !strings.Contains(body, "rp-"+d[0]+" rp-dark") || !strings.Contains(body, "Logistics Manager") && !strings.Contains(body, "Experience") {
			t.Errorf("print page for %s: %d", d[0], code)
		}
	}
	// With a Chromium browser the download is a real PDF; without one the
	// app hands off to the print page. Either is correct.
	resp, err := j.c.Get(j.srv.URL + "/resume/pdf?t=" + itoa(id))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	switch {
	case resp.StatusCode == 200 && bytes.HasPrefix(b, []byte("%PDF")):
		if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "Resume.pdf") {
			t.Errorf("filename: %q", cd)
		}
	case resp.StatusCode == 303 && strings.Contains(resp.Header.Get("Location"), "manual=1"):
	default:
		t.Errorf("pdf: %d %s", resp.StatusCode, firstN(string(b), 100))
	}
}

func TestResumeFilename(t *testing.T) {
	if got := resumeFilename(`SFC "John" Doe`, "Ops / Manager"); got != "SFC_John_Doe_Ops__Manager_Resume.pdf" {
		t.Errorf("got %q", got)
	}
}

func TestJobQueryAndLinks(t *testing.T) {
	jq := parseJobQuery("logistics manager", "80903", "50", "both", "")
	rows := jobLinkRows(jq)
	if len(rows) != 3 || !strings.HasPrefix(rows[0].Label, "Near 80903") || rows[1].Label != "Remote" || !strings.HasPrefix(rows[2].Label, "Defense contractors") {
		t.Fatalf("rows: %+v", rows)
	}
	li := rows[0].Links[0].URL
	if !strings.Contains(li, "keywords=logistics+manager") || !strings.Contains(li, "location=80903") || !strings.Contains(li, "distance=50") {
		t.Errorf("LinkedIn near link: %s", li)
	}
	if !strings.Contains(rows[1].Links[0].URL, "f_WT=2") || !strings.Contains(rows[1].Links[1].URL, "l=Remote") {
		t.Errorf("remote links: %s | %s", rows[1].Links[0].URL, rows[1].Links[1].URL)
	}
	// No ZIP and no saved location: search the whole country, since few
	// federal postings are remote. An explicit Remote Only stays remote.
	if parseJobQuery("analyst", "", "", "both", "").Mode != "anywhere" {
		t.Error("blank ZIP should search anywhere in the US")
	}
	if parseJobQuery("analyst", "", "", "remote", "").Mode != "remote" {
		t.Error("Remote Only should stay remote")
	}
	for q, want := range map[string]string{"Platform Engineer": "IT Specialist", "senior DevOps": "IT Specialist",
		"cybersecurity analyst": "IT Specialist INFOSEC", "logistics manager": ""} {
		if got := fedTitle(q); got != want {
			t.Errorf("fedTitle(%q) = %q, want %q", q, got, want)
		}
	}
	if got := parseJobQuery("x", "12ab5", "999", "odd", "Denver").Radius; got != 25 {
		t.Errorf("bad radius should fall back to 25, got %d", got)
	}
}

func TestJobResultsRender(t *testing.T) {
	j := newJourney(t)
	jq := parseJobQuery("logistics", "80903", "25", "both", "")
	rec := httptest.NewRecorder()
	j.app.page(rec, "jobs", map[string]any{
		"JQ": jq, "LinkRows": jobLinkRows(jq), "Radii": []int{10, 25, 50, 100},
		"Hits": []jobHit{
			{Title: "Logistics Manager", Org: "Acme Defense", Location: "Colorado Springs, CO", URL: "https://example.com/1", Source: "Adzuna", Posted: "2026-09-28", Pay: "$70,000 to $85,000"},
			{Title: "Supply Chain Specialist", Org: "Department of the Army", URL: "https://example.com/2", Source: "USAJOBS", Closes: "2026-10-15", Remote: true},
		},
		"Errs": []string{"USAJOBS: key rejected"}, "Employers": employers, "EmployersOn": employers, "EmployerOn": map[string]bool{},
	})
	body := rec.Body.String()
	for _, want := range []string{"2 Openings", "$70,000 to $85,000", "Track It", "Found on Adzuna", "USAJOBS: key rejected",
		"Closes Oct 15, 2026", ">Remote<", "Save This Search", "Near 80903"} {
		if !strings.Contains(body, want) {
			t.Errorf("jobs page is missing %q", want)
		}
	}
}

func (j *journey) postResp(path string, form url.Values) *http.Response {
	j.t.Helper()
	req, _ := http.NewRequest("POST", j.srv.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := j.c.Do(req)
	if err != nil {
		j.t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func flashOf(resp *http.Response) string {
	for _, c := range resp.Cookies() {
		if c.Name == flashCookie {
			v, _ := url.QueryUnescape(c.Value)
			return v
		}
	}
	return ""
}

func TestJourneyBadInputIsRefusedWithANotice(t *testing.T) {
	j := newJourney(t)
	if f := flashOf(j.postResp("/bills", url.Values{"name": {"Phone"}, "amount": {"eighty"}})); !strings.HasPrefix(f, "err|") {
		t.Errorf("bad bill amount flash = %q", f)
	}
	if n := len(j.app.store.snapshot().Bills); n != 0 {
		t.Errorf("a bill with a bad amount was saved (%d bills)", n)
	}
	if f := flashOf(j.postResp("/bills", url.Values{"name": {"Phone"}, "amount": {"84.99"}})); !strings.HasPrefix(f, "ok|") {
		t.Errorf("good bill flash = %q", f)
	}
	if f := flashOf(j.postResp("/debts", url.Values{"name": {"Visa"}, "balance": {"lots"}})); !strings.HasPrefix(f, "err|") {
		t.Errorf("bad debt flash = %q", f)
	}
	j.post("/debts", url.Values{"name": {"Unknown card"}}) // blank balance: not captured yet, allowed
	if n := len(j.app.store.snapshot().Debts); n != 1 {
		t.Errorf("debts = %d, want 1", n)
	}
	if f := flashOf(j.postResp("/appointments", url.Values{"title": {"Physical"}, "at": {"next tuesday"}})); !strings.HasPrefix(f, "err|") {
		t.Errorf("bad appointment time flash = %q", f)
	}
}

func TestRetirePay(t *testing.T) {
	st := State{Settings: Settings{RetYears: 20, RetHigh3: 600000, RetSBP: true, VaEstimate: 70, MonthlyIncome: 700000}}
	p := estimateRetirePay(st)
	// 20 x 2.5% = 50% of $6,000 = $3,000; SBP 6.5% = $195; 70% alone is
	// $1,808.45, paid on top of retired pay under CRDP.
	if p.Gross != 300000 || p.SBP != 19500 || p.Net != 280500 || !p.CRDP || p.VA != 180845 || p.Total != 461345 {
		t.Fatalf("High-3 at 70%%: %+v", p)
	}
	st.Settings.VaEstimate = 30 // below 50%: VA replaces an equal amount of retired pay
	if p := estimateRetirePay(st); p.CRDP || p.VA != 55247 || p.Offset != 55247 || p.Total != 280500 {
		t.Errorf("below 50%%: %+v", p)
	}
	st.Settings.RetSystem, st.Settings.VaEstimate = "brs", 70
	if p := estimateRetirePay(st); p.Gross != 240000 || p.Multiplier != "40.0%" {
		t.Errorf("BRS: %+v", p)
	}
	if p := estimateRetirePay(State{}); p.Set {
		t.Error("no inputs should mean no estimate")
	}
}

func TestVAMonthlyMatchesVATable(t *testing.T) {
	// Every expected value is read straight off va.gov's table effective
	// December 1, 2025, including combinations built from its add-on rows.
	cases := []struct {
		rating int
		d      vaDependents
		want   int64
	}{
		{0, vaDependents{}, 0},
		{10, vaDependents{Spouse: true, Children: 3}, 18042}, // dependents never change 10% or 20%
		{20, vaDependents{}, 35666},
		{30, vaDependents{Spouse: true, Parents: 1}, 66947},
		{50, vaDependents{Parents: 1}, 122090},
		{70, vaDependents{}, 180845},
		{70, vaDependents{Spouse: true}, 196145},
		{70, vaDependents{Spouse: true, Children: 2}, 207445 + 7600},
		{90, vaDependents{Parents: 2}, 267830},
		{100, vaDependents{Spouse: true, Parents: 2}, 451065},
		{100, vaDependents{Spouse: true, Children: 1, SchoolKids: 1}, 431899 + 35245},
		{100, vaDependents{Children: 1}, 408543},
		{68, vaDependents{}, 180845}, // an estimate of 68 rounds to 70, as VA rounds
		{64, vaDependents{}, 143502}, // and 64 rounds to 60
	}
	for _, c := range cases {
		if got := vaMonthly(c.rating, c.d); got != c.want {
			t.Errorf("vaMonthly(%d, %+v) = %s, want %s", c.rating, c.d, money(got), money(c.want))
		}
	}
}

func TestQuickSearch(t *testing.T) {
	st := State{Todos: []Todo{{ID: 7, Title: "Call the VSO", Notes: "claim review"}},
		Notes: []Note{{ID: 9, Topic: "TAP day one", Body: "VSO office is in building 4"}}}
	hits := quickSearch(st, "vso")
	if len(hits) < 2 || hits[0].Href != "/todos#todo-7" {
		t.Fatalf("title match should rank first: %+v", hits)
	}
	if hits := quickSearch(st, "budget"); len(hits) == 0 || hits[0].Kind != "Page" {
		t.Errorf("pages should be searchable: %+v", hits)
	}
	if hits := quickSearch(st, "vso zebra"); len(hits) != 0 {
		t.Errorf("every word must match: %+v", hits)
	}
	j := newJourney(t)
	code, body := j.get("/search?q=dashboard")
	if code != 200 || !strings.Contains(body, `"href":"/"`) {
		t.Errorf("search endpoint: %d %s", code, body)
	}
}

func TestJourneyBackupAndRestore(t *testing.T) {
	j := newJourney(t)
	j.post("/settings/demo", nil)
	j.post("/notes", url.Values{"topic": {"Keep me"}})
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "dd214.pdf")
	fw.Write([]byte("%PDF-1.4 dd214"))
	mw.Close()
	req, _ := http.NewRequest("POST", j.srv.URL+"/docs", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if resp, err := j.c.Do(req); err != nil || resp.StatusCode != 303 {
		t.Fatalf("upload: %v", err)
	}
	before := j.app.store.snapshot()

	resp, err := j.c.Get(j.srv.URL + "/settings/backup")
	if err != nil || resp.Header.Get("Content-Type") != "application/zip" {
		t.Fatalf("backup: %v %v", err, resp.Header)
	}
	backup, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	j.post("/settings/erase", nil)
	if len(j.app.store.snapshot().Notes) != 0 {
		t.Fatal("erase did not erase")
	}

	restore := func(zipBytes []byte) *http.Response {
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		f, _ := w.CreateFormFile("backup", "backup.zip")
		f.Write(zipBytes)
		w.Close()
		req, _ := http.NewRequest("POST", j.srv.URL+"/settings/restore", &b)
		req.Header.Set("Content-Type", w.FormDataContentType())
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		resp, err := j.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}

	if f := flashOf(restore(backup)); !strings.HasPrefix(f, "ok|") {
		t.Fatalf("restore flash = %q", f)
	}
	after := j.app.store.snapshot()
	if len(after.Notes) != len(before.Notes) || len(after.Todos) != len(before.Todos) || len(after.Docs) != len(before.Docs) {
		t.Fatalf("restored %d notes, %d todos, %d docs; want %d, %d, %d",
			len(after.Notes), len(after.Todos), len(after.Docs), len(before.Notes), len(before.Todos), len(before.Docs))
	}
	doc := after.Docs[len(after.Docs)-1]
	if code, body := j.get("/docs/" + itoa(doc.ID) + "/file"); code != 200 || body != "%PDF-1.4 dd214" {
		t.Errorf("restored document: %d %q", code, body)
	}
	kept, _ := filepath.Glob(filepath.Join(j.app.data, "state.before-restore-*.json"))
	if len(kept) != 1 {
		t.Errorf("the pre-restore state was not kept: %v", kept)
	}

	// Garbage and hostile zips change nothing.
	notesBefore := len(j.app.store.snapshot().Notes)
	for name, z := range map[string][]byte{
		"not a zip":    []byte("hello"),
		"no state":     zipOf(t, map[string]string{"docs/a.pdf": "x"}),
		"path escape":  zipOf(t, map[string]string{"state.json": "{}", "docs/../../evil.txt": "x"}),
		"stray file":   zipOf(t, map[string]string{"state.json": "{}", "evil.sh": "x"}),
		"broken state": zipOf(t, map[string]string{"state.json": "{not json"}),
	} {
		if f := flashOf(restore(z)); !strings.HasPrefix(f, "err|") {
			t.Errorf("%s: flash = %q", name, f)
		}
	}
	if len(j.app.store.snapshot().Notes) != notesBefore {
		t.Error("a rejected restore changed the data")
	}
	if fileExists(filepath.Join(filepath.Dir(j.app.data), "evil.txt")) {
		t.Error("a path-escape entry was written outside the data folder")
	}
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for name, body := range files {
		f, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(body))
	}
	zw.Close()
	return b.Bytes()
}

func TestJourneySkillBridge(t *testing.T) {
	j := newJourney(t)
	j.post("/settings", url.Values{"name": {"SFC John Doe"}, "branch": {"Army"}, "retirement_date": {"2027-12-03"}})
	if f := flashOf(j.postResp("/skillbridge/plan", url.Values{"company": {"Acme"}, "start": {"not a date"}})); !strings.HasPrefix(f, "err|") {
		t.Errorf("bad date flash = %q", f)
	}
	j.post("/skillbridge/plan", url.Values{"company": {"Acme Defense"}, "program": {"Ops Fellowship"}, "location": {"Remote"},
		"start": {"2027-06-15"}, "end": {"2027-09-10"}, "rank": {"SFC"}, "unit": {"HHC, 1-1 IN"}, "commander": {"Commander, HHC, 1-1 IN"}})
	v := buildSBView(j.app.store.snapshot())
	if v.Length != 88 || len(v.Warnings) != 0 || v.WindowOpen != "Jun 6, 2027" {
		t.Errorf("plan view: %+v", v)
	}
	// Too early and too long both warn.
	j.post("/skillbridge/plan", url.Values{"company": {"Acme"}, "start": {"2027-01-01"}, "end": {"2027-11-30"}})
	if v := buildSBView(j.app.store.snapshot()); len(v.Warnings) != 2 {
		t.Errorf("want 2 warnings, got %v", v.Warnings)
	}
	j.post("/skillbridge/plan", url.Values{"company": {"Acme Defense"}, "program": {"Ops Fellowship"}, "start": {"2027-06-15"}, "end": {"2027-09-10"}, "rank": {"SFC"}})

	j.post("/skillbridge/leads", url.Values{"company": {"Acme Defense"}, "program": {"Ops Fellowship"}})
	id := j.app.store.snapshot().SBLeads[0].ID
	j.post("/skillbridge/leads/"+itoa(id)+"/advance", nil)
	if s := j.app.store.snapshot().SBLeads[0].Status; s != "contacted" {
		t.Errorf("lead status %q", s)
	}
	if code := j.post("/packet-par/sb-memo", url.Values{"status": {"done"}}); code != 303 {
		t.Errorf("checklist mark: %d", code)
	}
	if j.app.store.snapshot().PARDone["sb-memo"] == "" {
		t.Error("SkillBridge checklist mark did not stick")
	}
	code, body := j.get("/skillbridge/memo")
	if code != 200 || !strings.Contains(body, "Acme Defense") || !strings.Contains(body, "15 June 2027") || strings.Contains(body, "SFC SFC") || strings.Count(body, "SFC") != 1 {
		t.Errorf("memo: %d, rank should appear once in the signature", code)
	}
	for _, p := range []string{"/skillbridge", "/skillbridge?q=logistics&zip=80903&mode=both", "/"} {
		if code, body := j.get(p); code != 200 || strings.Contains(body, "ZgotmplZ") {
			t.Errorf("%s: %d", p, code)
		}
	}
	if _, body := j.get("/"); !strings.Contains(body, "Days to SkillBridge") || !strings.Contains(body, "Days to PTDY") {
		t.Error("dashboard is missing the PTDY or SkillBridge tile")
	}
}

func TestStripRank(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"SFC John Doe", "SFC"}: "John Doe", {"John Doe", "SFC"}: "John Doe", {"SFCJohn", "SFC"}: "SFCJohn", {"SFC John", ""}: "SFC John",
	} {
		if got := stripRank(in[0], in[1]); got != want {
			t.Errorf("stripRank(%q, %q) = %q", in[0], in[1], got)
		}
	}
}

func TestResumeFontAndColor(t *testing.T) {
	j := newJourney(t)
	j.post("/settings/demo", nil)
	id := j.app.store.snapshot().ResumeTargets[0].ID
	j.post("/resume/targets/"+itoa(id)+"/style", url.Values{"design": {"executive"}, "font": {"garamond"}, "color": {"burgundy"}, "dark": {"0"}})
	tg := j.app.store.snapshot().ResumeTargets[0]
	if tg.PaperClass() != "resume-paper rp-executive rp-f-garamond rp-c-burgundy" {
		t.Errorf("class = %q", tg.PaperClass())
	}
	j.post("/resume/targets/"+itoa(id)+"/style", url.Values{"design": {"modern"}, "font": {"comic-sans"}, "color": {"hotpink"}})
	if tg := j.app.store.snapshot().ResumeTargets[0]; tg.Font != "garamond" || tg.Color != "burgundy" {
		t.Errorf("unknown font or color should be ignored: %+v", tg)
	}
	if _, body := j.get("/resume/print?t=" + itoa(id)); !strings.Contains(body, "rp-f-garamond") || !strings.Contains(body, "resume-fonts.css") {
		t.Error("print page does not carry the font")
	}
	for _, f := range resumeFonts[1:] {
		if !strings.Contains(mustRead(t, "static/resume-fonts.css"), "rp-f-"+f[0]) {
			t.Errorf("no CSS for font %s", f[0])
		}
	}
}

func TestHeaderIconsJSON(t *testing.T) {
	j := newJourney(t)
	_, body := j.get("/budget")
	i := strings.Index(body, `id="rw-icons">`)
	if i < 0 {
		t.Fatal("no icon set on the page")
	}
	raw := body[i+len(`id="rw-icons">`):]
	raw = raw[:strings.Index(raw, "</script>")]
	var icons map[string]string
	if err := json.Unmarshal([]byte(raw), &icons); err != nil || icons["dollar"] == "" || icons["sparkle"] == "" {
		t.Errorf("icon JSON: %v (%d icons)", err, len(icons))
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMoneyClockWithoutDebt(t *testing.T) {
	rd := time.Now().AddDate(0, 0, 100).Format("2006-01-02")
	st := State{Settings: Settings{RetirementDate: rd, SavingsGoal: 1000000}}
	st.Savings = []SavingsEntry{{ID: 1, Date: time.Now().Format("2006-01-02"), Amount: 500000}}
	c := moneyClock(st, 0)
	if c.HasDebt || c.RetDays != 100 || c.SavPerDay != 5000 {
		t.Fatalf("no-debt clock: %+v", c)
	}
	st.Debts = []Debt{{ID: 2, Name: "Visa", Balance: 10000}}
	if c := moneyClock(st, 10000); !c.HasDebt {
		t.Error("an open debt should switch the clock to payoff mode")
	}
	j := newJourney(t)
	j.post("/settings", url.Values{"name": {"A"}, "retirement_date": {rd}})
	if code, body := j.get("/savings"); code != 200 || !strings.Contains(body, "Money Countdown") || !strings.Contains(body, "To retirement day") {
		t.Errorf("no-debt budget page: %d", code)
	}
}

func TestWeatherPlacesFallBackToHousingZip(t *testing.T) {
	st := State{Settings: Settings{WeatherHere: "Wiesbaden"}, Housing: &HouseSearch{Zip: "80903"}}
	got := weatherPlaces(st)
	if len(got) != 2 || got[0][1] != "Wiesbaden" || got[1][1] != "80903" {
		t.Errorf("places = %v", got)
	}
	if len(weatherPlaces(State{})) != 0 {
		t.Error("no places set should fetch nothing")
	}
}

func TestPARWindowIsTwelveMonths(t *testing.T) {
	st := State{Settings: Settings{RetirementDate: "2027-12-03"}}
	if v := buildPARView(st); v.WindowOpen != "Dec 3, 2025" || v.WindowClose != "Dec 3, 2026" {
		t.Errorf("window = %s to %s, want Dec 3, 2025 to Dec 3, 2026", v.WindowOpen, v.WindowClose)
	}
}

func TestHigh3E8With22Years(t *testing.T) {
	rd, _ := parseDay("2027-11-01")
	h := calcHigh3("E-8", 22, rd, 3.0)
	if !h.OK || h.From != "Nov 2024" || h.To != "Oct 2027" || h.Projected != 10 {
		t.Fatalf("E-8 22 years: %+v", h)
	}
	// Hand-check: the 36 months are Nov 2024 to Oct 2027. Service runs from
	// 19 to 22 years, so the months use "over 18" and "over 20" on each
	// year's table, and the 10 months of 2027 use 2026 plus 3%.
	var sum int64
	add := func(n int, cents int64) { sum += int64(n) * cents }
	add(2, basicPay[2024]["E-8"][18])  // Nov-Dec 2024, 19 years
	add(10, basicPay[2025]["E-8"][18]) // Jan-Oct 2025, 19 years
	add(2, basicPay[2025]["E-8"][20])  // Nov-Dec 2025, 20 years
	add(10, basicPay[2026]["E-8"][20]) // Jan-Oct 2026, 20 years
	add(2, basicPay[2026]["E-8"][20])  // Nov-Dec 2026, 21 years
	add(10, int64(float64(basicPay[2026]["E-8"][20])*1.03+0.5))
	if want := (sum + 18) / 36; h.Amount != want {
		t.Errorf("High-3 = %s, hand-check says %s", money(h.Amount), money(want))
	}
	t.Logf("E-8, 22 years, retiring Nov 1 2027: High-3 %s", money(h.Amount))
	// The official 2026 E-8 row, straight from DFAS.
	if basicPay[2026]["E-8"][20] != 699540 || basicPay[2026]["E-8"][22] != 730830 || basicPay[2026]["E-8"][30] != 806730 {
		t.Error("2026 E-8 row does not match the DFAS table")
	}
	if calcHigh3("E-5", 20, rd, 3).OK || calcHigh3("O-9", 30, rd, 3).OK {
		t.Error("unsupported grades must fall back to manual entry")
	}
}

func TestJourneyAppointmentEditAndDone(t *testing.T) {
	j := newJourney(t)
	at := time.Now().AddDate(0, 0, 3).Format("2006-01-02") + "T09:00"
	j.post("/appointments", url.Values{"title": {"Physical"}, "at": {at}})
	id := j.app.store.snapshot().Appointments[0].ID
	p := "/appointments/" + strconv.Itoa(id)

	later := time.Now().AddDate(0, 0, 4).Format("2006-01-02") + "T13:30"
	j.post(p+"/update", url.Values{"title": {"Separation physical"}, "at": {later}, "place": {"Clinic"}})
	a := j.app.store.snapshot().Appointments[0]
	if a.Title != "Separation physical" || a.At != later || a.Place != "Clinic" {
		t.Fatalf("after edit: %+v", a)
	}
	if f := flashOf(j.postResp(p+"/update", url.Values{"title": {"X"}, "at": {"soon"}})); !strings.HasPrefix(f, "err|") {
		t.Errorf("bad edit time flash = %q", f)
	}

	j.post(p+"/done", nil)
	if !j.app.store.snapshot().Appointments[0].Done {
		t.Fatal("Done did not mark the appointment finished")
	}
	if _, body := j.get("/appointments"); !strings.Contains(body, "btn-done") {
		t.Error("a finished appointment should show the green Done button")
	}
	if _, body := j.get("/"); strings.Contains(body, "Separation physical") {
		t.Error("the dashboard still lists a finished appointment")
	}
	j.post(p+"/done", nil)
	if j.app.store.snapshot().Appointments[0].Done {
		t.Error("a second click should reopen the appointment")
	}
}

func TestFederalTax2026(t *testing.T) {
	// $60,000 single: taxable $43,900 = 10% of 12,400 + 12% of 31,500 = $5,020.
	if got := bracketTax(60000-fedStdSingle, fedSingle); got < 5019.99 || got > 5020.01 {
		t.Errorf("single $60k = %.2f, want 5020", got)
	}
	// $60,000 joint: taxable $27,800 = 10% of 24,800 + 12% of 3,000 = $2,840.
	if got := bracketTax(60000-fedStdJoint, fedJoint); got < 2839.99 || got > 2840.01 {
		t.Errorf("joint $60k = %.2f, want 2840", got)
	}
}

func TestStateFromPlace(t *testing.T) {
	for q, want := range map[string]string{
		"80920": "CO", "35806": "AL", "20190": "VA", "20001": "DC", "98433": "WA", "00501": "NY",
		"Colorado Springs, CO": "CO", "Huntsville, Alabama": "AL", "Wiesbaden, Germany": "", "Tampa": "",
	} {
		if got := stateFromPlace(q); got != want {
			t.Errorf("stateFromPlace(%q) = %q, want %q", q, got, want)
		}
	}
}

func TestStateDataCoversEveryState(t *testing.T) {
	for code := range stateNames {
		if _, ok := milRules[code]; !ok {
			t.Errorf("%s has no military retired pay rule", code)
		}
		if _, ok := stateTaxes[code]; !ok {
			t.Errorf("%s has no tax table", code)
		}
	}
	if len(milRules) != 51 || len(stateTaxes) != 51 {
		t.Errorf("rules %d, tables %d, want 51 each", len(milRules), len(stateTaxes))
	}
}

func TestRetireTaxByState(t *testing.T) {
	// $4,000 a month retired pay, single, under 55: $48,000 a year.
	st := State{Settings: Settings{RetYears: 20, RetHigh3: 800000, RetirementDate: "2027-11-01", BirthYear: 1985}}
	tax := func(state string) retireTax {
		st.Settings.TaxState = state
		return estimateRetirePay(st).Tax
	}
	fed := tax("TX").Federal
	// $48,000 - 16,100 = 31,900: 1,240 + 12% of 19,500 = 3,580 a year.
	if fed != 29833 {
		t.Errorf("federal = %d cents a month, want 29833", fed)
	}
	for _, s := range []string{"TX", "AL", "NY", "UT", "GA"} {
		if got := tax(s).StateTax; got != 0 {
			t.Errorf("%s state tax = %d, want 0", s, got)
		}
	}
	// Virginia: 48,000 - 40,000 - 8,750 - 930 = none left.
	if got := tax("VA").StateTax; got != 0 {
		t.Errorf("VA = %d, want 0", got)
	}
	// Colorado under 55: (48,000 - 15,000 - 16,100) x 4.4% = 743.60 a year.
	if got := tax("CO").StateTax; got != 6197 {
		t.Errorf("CO = %d cents a month, want 6197", got)
	}
	// DC taxes all of it: 48,000 - 16,100 = 31,900 -> 400 + 6% of 21,900 = 1,714.
	if got := tax("DC").StateTax; got != 14283 {
		t.Errorf("DC = %d cents a month, want 14283", got)
	}
	// Colorado at 65 subtracts 24,000 instead: 7,900 x 4.4% = 347.60 a year.
	st.Settings.BirthYear = 1960
	if got := tax("CO").StateTax; got != 2897 {
		t.Errorf("CO at 67 = %d cents a month, want 2897", got)
	}
	// Guess the state from where you plan to retire.
	st.Settings.TaxState, st.Settings.WeatherRetire = "", "Colorado Springs, CO"
	if tx := estimateRetirePay(st).Tax; tx.State != "CO" || !tx.Guessed {
		t.Errorf("guessed state = %q (guessed %v)", tx.State, tx.Guessed)
	}
	p := estimateRetirePay(st)
	if p.Tax.AfterTax != p.Total-p.Tax.Federal-p.Tax.StateTax {
		t.Errorf("after tax does not add up: %+v", p.Tax)
	}
}

func TestRetireTaxWithCivilianJob(t *testing.T) {
	// $4,000 a month retired pay ($48,000) plus a $60,000 job, single.
	st := State{Settings: Settings{RetYears: 20, RetHigh3: 800000, RetirementDate: "2027-11-01", BirthYear: 1985, CivSalary: 6000000}}
	tax := func(state string) retireTax {
		st.Settings.TaxState = state
		return estimateRetirePay(st).Tax
	}
	tx := tax("TX")
	// Federal on $108,000 - 16,100 = 91,900: 1,240 + 4,560 + 9,130 = 14,930 a year.
	if tx.Federal != 124417 || tx.FICA != 38250 || tx.Wages != 500000 || tx.StateTax != 0 {
		t.Errorf("Texas with a job: %+v", tx)
	}
	// Alabama exempts retired pay but taxes the job: 60,000 - 3,000 - 1,500 = 55,500.
	if got := tax("AL").StateTax; got != 22792 {
		t.Errorf("AL job = %d cents a month, want 22792", got)
	}
	// California: AGI $108,000 keeps the $20,000 subtraction.
	if got := tax("CA").StateTax; got != 32953 {
		t.Errorf("CA = %d, want 32953", got)
	}
	// A $100,000 job lifts AGI past $125,000, so the subtraction is lost.
	st.Settings.CivSalary = 10000000
	if got := tax("CA").StateTax; got != 79453 {
		t.Errorf("CA over the limit = %d, want 79453", got)
	}
	p := estimateRetirePay(st)
	if p.Tax.AfterTax != p.Total+p.Tax.Wages-p.Tax.Federal-p.Tax.StateTax-p.Tax.FICA {
		t.Errorf("after tax does not add up: %+v", p.Tax)
	}
}

func TestJourneyCivilianSalary(t *testing.T) {
	j := newJourney(t)
	if f := flashOf(j.postResp("/budget/retirepay", url.Values{"years": {"22"}, "high3": {"6000"}, "civ_salary": {"lots"}})); !strings.HasPrefix(f, "err|") {
		t.Errorf("bad salary flash = %q", f)
	}
	j.post("/budget/retirepay", url.Values{"years": {"22"}, "high3": {"6000"}, "civ_salary": {"85,000"}, "tax_state": {"TX"}})
	if got := j.app.store.snapshot().Settings.CivSalary; got != 8500000 {
		t.Fatalf("salary = %d cents, want 8500000", got)
	}
	if _, body := j.get("/retired-pay"); !strings.Contains(body, "Social Security and Medicare") || !strings.Contains(body, "Taxes After You Retire") {
		t.Error("the job rows are missing from Retired Pay")
	}
}

func TestHealthCosts(t *testing.T) {
	// E-8 retiring Nov 2027 after 22 years joined in 2005: Group A. Married
	// with two children: family Prime, $765 a year.
	s := Settings{RetirementDate: "2027-11-01", RetYears: 22, RetSpouse: true, RetKids: 2}
	h := estimateHealth(s)
	if h.Group != "A" || h.Plan != "prime" || h.Tricare != 6375 || h.Dental != 11369 || h.Vision != 3012 || h.Total != 6375+11369+3012 {
		t.Errorf("family Prime, Group A: %+v", h)
	}
	// Joined in 2020: Group B. Single on Select: $594.96 a year.
	h = estimateHealth(Settings{RetirementDate: "2040-06-01", RetYears: 20, HealthPlan: "select", NoDental: true})
	if h.Group != "B" || h.Tricare != 4958 || h.Dental != 0 || h.Vision != 1004 {
		t.Errorf("single Select, Group B: %+v", h)
	}
	// TRICARE For Life: Part B for both spouses, self plus one FEDVIP.
	h = estimateHealth(Settings{HealthPlan: "tfl", RetSpouse: true})
	if h.Tricare != 0 || h.PartB != 40580 || h.Tier != "self plus one" || h.Dental != 7579 {
		t.Errorf("TFL married: %+v", h)
	}
	// No TRICARE plan: FEDVIP vision is not available, other premiums count.
	h = estimateHealth(Settings{HealthPlan: "none", HealthOther: 25000})
	if h.Vision != 0 || h.Total != 3790+25000 {
		t.Errorf("no TRICARE: %+v", h)
	}
	st := State{Settings: Settings{RetYears: 20, RetHigh3: 800000, RetirementDate: "2027-11-01", MonthlyIncome: 500000}}
	p := estimateRetirePay(st)
	if p.TakeHome != p.Tax.AfterTax-p.Health.Total || p.Change != p.TakeHome-500000 {
		t.Errorf("take-home does not add up: %d after tax, %d health, %d take-home", p.Tax.AfterTax, p.Health.Total, p.TakeHome)
	}
}

func TestJourneyHealthPlan(t *testing.T) {
	j := newJourney(t)
	if f := flashOf(j.postResp("/budget/retirepay", url.Values{"years": {"22"}, "high3": {"6000"}, "health_plan": {"gold"}})); !strings.HasPrefix(f, "err|") {
		t.Errorf("bad plan flash = %q", f)
	}
	j.post("/budget/retirepay", url.Values{"years": {"22"}, "high3": {"6000"}, "health_plan": {"select"}, "dental": {"1"}, "health_other": {"120"}})
	s := j.app.store.snapshot().Settings
	if s.HealthPlan != "select" || s.NoDental || !s.NoVision || s.HealthOther != 12000 {
		t.Fatalf("saved health settings: plan %q dental off %v vision off %v other %d", s.HealthPlan, s.NoDental, s.NoVision, s.HealthOther)
	}
	if _, body := j.get("/retired-pay"); !strings.Contains(body, "Health Coverage After You Retire") || !strings.Contains(body, "Take-Home Each Month") {
		t.Error("the health section is missing from Retired Pay")
	}
}

func TestJourneyRenameBillAndDebt(t *testing.T) {
	j := newJourney(t)
	j.post("/bills", url.Values{"name": {"Phone"}, "amount": {"84.99"}})
	j.post("/debts", url.Values{"name": {"Visa"}, "balance": {"1200"}})
	st := j.app.store.snapshot()
	bill, debt := st.Bills[len(st.Bills)-1], st.Debts[len(st.Debts)-1]

	j.post("/bills/"+strconv.Itoa(bill.ID)+"/update", url.Values{"name": {"Verizon"}, "amount": {"90.00"}})
	j.post("/debts/"+strconv.Itoa(debt.ID)+"/update", url.Values{"name": {"Chase Visa"}})
	st = j.app.store.snapshot()
	if b := st.Bills[len(st.Bills)-1]; b.Name != "Verizon" || b.Amount != 9000 {
		t.Errorf("bill after rename: %+v", b)
	}
	if d := st.Debts[len(st.Debts)-1]; d.Name != "Chase Visa" || d.Balance != 120000 {
		t.Errorf("debt after rename: %+v", d)
	}
	// A blank name keeps the old one.
	j.post("/bills/"+strconv.Itoa(bill.ID)+"/update", url.Values{"name": {" "}, "amount": {"90.00"}})
	if b := j.app.store.snapshot().Bills[len(st.Bills)-1]; b.Name != "Verizon" {
		t.Errorf("blank name replaced the bill name: %q", b.Name)
	}
}

// Anything you can add, you can edit: every list with an add form has an
// update route that changes the item in place.
func TestJourneyEveryListIsEditable(t *testing.T) {
	j := newJourney(t)
	last := func(ids []int) string { return strconv.Itoa(ids[len(ids)-1]) }
	ids := func(get func(State) []int) string { return last(get(j.app.store.snapshot())) }

	j.post("/medical/conditions", url.Values{"name": {"Knee"}})
	id := ids(func(s State) (o []int) {
		for _, x := range s.Conditions {
			o = append(o, x.ID)
		}
		return
	})
	j.post("/medical/conditions/"+id+"/update", url.Values{"name": {"Right knee"}, "notes": {"2019"}})

	j.post("/medical/meds", url.Values{"name": {"Ibuprofen"}})
	id = ids(func(s State) (o []int) {
		for _, x := range s.Meds {
			o = append(o, x.ID)
		}
		return
	})
	j.post("/medical/meds/"+id+"/update", url.Values{"name": {"Naproxen"}, "dose": {"500 mg"}})

	j.post("/medical/symptoms", url.Values{"note": {"Locked up"}, "date": {"2026-09-01"}})
	id = ids(func(s State) (o []int) {
		for _, x := range s.Symptoms {
			o = append(o, x.ID)
		}
		return
	})
	j.post("/medical/symptoms/"+id+"/update", url.Values{"note": {"Locked up on stairs"}, "date": {"2026-09-02"}, "condition": {"Right knee"}})

	j.post("/jobs/prospects", url.Values{"title": {"Engineer"}})
	id = ids(func(s State) (o []int) {
		for _, x := range s.Prospects {
			o = append(o, x.ID)
		}
		return
	})
	j.post("/jobs/prospects/"+id+"/update", url.Values{"title": {"Systems Engineer"}, "status": {"interview"}})

	j.post("/jobs/contacts", url.Values{"name": {"Dana"}})
	id = ids(func(s State) (o []int) {
		for _, x := range s.Contacts {
			o = append(o, x.ID)
		}
		return
	})
	j.post("/jobs/contacts/"+id+"/update", url.Values{"name": {"Dana Reyes"}, "org": {"SAIC"}})

	j.post("/jobs/searches", url.Values{"keyword": {"logistics"}})
	id = ids(func(s State) (o []int) {
		for _, x := range s.JobSearches {
			o = append(o, x.ID)
		}
		return
	})
	j.post("/jobs/searches/"+id+"/update", url.Values{"keyword": {"logistics manager"}, "zip": {"80903"}, "radius": {"50"}, "mode": {"remote"}})

	j.post("/resources/contacts", url.Values{"name": {"TAP"}})
	id = ids(func(s State) (o []int) {
		for _, x := range s.Resources {
			o = append(o, x.ID)
		}
		return
	})
	j.post("/resources/contacts/"+id+"/update", url.Values{"name": {"SFL-TAP Center"}, "info": {"DSN 548"}})

	j.post("/resources/links", url.Values{"title": {"Crisis"}, "url": {"example.org"}})
	id = ids(func(s State) (o []int) {
		for _, x := range s.Links {
			o = append(o, x.ID)
		}
		return
	})
	j.post("/resources/links/"+id+"/update", url.Values{"title": {"Crisis Line"}, "url": {"veteranscrisisline.net"}, "category": {"Health"}})

	j.post("/skillbridge/leads", url.Values{"company": {"Acme"}})
	id = ids(func(s State) (o []int) {
		for _, x := range s.SBLeads {
			o = append(o, x.ID)
		}
		return
	})
	j.post("/skillbridge/leads/"+id+"/update", url.Values{"company": {"Acme Defense"}, "status": {"applied"}})

	j.post("/savings", url.Values{"kind": {"deposit"}, "amount": {"500"}})
	id = ids(func(s State) (o []int) {
		for _, x := range s.Savings {
			o = append(o, x.ID)
		}
		return
	})
	j.post("/savings/"+id+"/update", url.Values{"kind": {"withdraw"}, "amount": {"200"}, "date": {"2026-09-15"}, "note": {"Car repair"}})

	j.post("/budget", url.Values{"kind": {"spend"}, "amount": {"40"}})
	id = ids(func(s State) (o []int) {
		for _, x := range s.Txns {
			o = append(o, x.ID)
		}
		return
	})
	j.post("/budget/"+id+"/update", url.Values{"kind": {"income"}, "amount": {"45.50"}, "category": {"refund"}})

	st := j.app.store.snapshot()
	checks := map[string]bool{
		"condition": st.Conditions[len(st.Conditions)-1].Name == "Right knee",
		"med":       st.Meds[len(st.Meds)-1].Dose == "500 mg",
		"symptom":   st.Symptoms[len(st.Symptoms)-1].Condition == "Right knee",
		"prospect":  st.Prospects[len(st.Prospects)-1].Status == "interview",
		"contact":   st.Contacts[len(st.Contacts)-1].Org == "SAIC",
		"search":    st.JobSearches[len(st.JobSearches)-1].Mode == "remote",
		"resource":  st.Resources[len(st.Resources)-1].Name == "SFL-TAP Center",
		"link":      st.Links[len(st.Links)-1].URL == "https://veteranscrisisline.net",
		"lead":      st.SBLeads[len(st.SBLeads)-1].Status == "applied",
		"savings":   st.Savings[len(st.Savings)-1].Amount == -20000,
		"txn":       st.Txns[len(st.Txns)-1].Amount == 4550,
	}
	for k, ok := range checks {
		if !ok {
			t.Errorf("%s was not edited", k)
		}
	}
	// A blank required field keeps the old value.
	j.post("/medical/conditions/"+strconv.Itoa(st.Conditions[len(st.Conditions)-1].ID)+"/update", url.Values{"name": {""}})
	if n := j.app.store.snapshot().Conditions; n[len(n)-1].Name != "Right knee" {
		t.Errorf("blank name wiped the condition: %q", n[len(n)-1].Name)
	}
	// Every page with an edit form still renders.
	for _, p := range []string{"/medical", "/jobs", "/resources", "/skillbridge", "/savings", "/budget"} {
		if code, body := j.get(p); code != 200 || !strings.Contains(body, `data-edit=`) {
			t.Errorf("%s: code %d, has edit buttons %v", p, code, strings.Contains(body, "data-edit="))
		}
	}
}

func TestJourneyFindConditionsInRecords(t *testing.T) {
	j := newJourney(t)
	// No Advisor and no records yet: refused with a reason, nothing stored.
	if resp := j.postResp("/medical/find", nil); !strings.Contains(resp.Header.Get("Location"), "err=") {
		t.Errorf("find without an Advisor went through: %s", resp.Header.Get("Location"))
	}
	var prompt, dir string
	askAIFunc = func(_ context.Context, _, p, d string) (string, error) {
		prompt, dir = p, d
		return "```json\n" + `{"conditions":[
			{"name":"Tinnitus","where":"STR 2019","evidence":"Ringing after range week."},
			{"name":"Right knee, patellofemoral pain","where":"Ortho note, 2021-03-04","evidence":"Pain on stairs, crepitus."},
			{"name":"right knee,  patellofemoral pain","where":"PT note","evidence":"Repeat."},
			{"name":"Lumbar strain","where":"Sick call 2018","evidence":"Low back pain lifting."}]}` + "\n```", nil
	}
	defer func() { askAIFunc = askAI }()
	_ = j.app.store.mutate(func(st *State) {
		st.Settings.AdvisorProvider = "claude"
		st.Docs = append(st.Docs, Doc{ID: st.id(), Name: "STR copy.pdf", File: "docs/str.pdf", Kind: "medical"})
		st.Conditions = append(st.Conditions, Condition{ID: st.id(), Name: "Tinnitus"})
	})

	j.post("/medical/find", nil)
	if !strings.Contains(prompt, "STR copy.pdf") || !strings.Contains(prompt, "leave these out: Tinnitus") || dir == "" {
		t.Errorf("prompt or read folder is wrong: dir %q\n%s", dir, prompt)
	}
	found := j.app.store.snapshot().Found
	if found == nil || len(found.Items) != 2 || found.Items[0].Name != "Right knee, patellofemoral pain" || found.Items[1].Name != "Lumbar strain" {
		t.Fatalf("found = %+v, want the knee and the back only (Tinnitus is listed, the repeat merged)", found)
	}
	if _, body := j.get("/medical"); !strings.Contains(body, "Found in Your Records") || !strings.Contains(body, "Ortho note, 2021-03-04") {
		t.Error("the found panel is missing")
	}

	// Add the back only; the knee stays for later.
	j.post("/medical/found/add", url.Values{"pick": {"1"}})
	st := j.app.store.snapshot()
	c := st.Conditions[len(st.Conditions)-1]
	if c.Name != "Lumbar strain" || !c.Documented || !strings.Contains(c.Notes, "Sick call 2018") {
		t.Errorf("added condition = %+v", c)
	}
	if len(st.Found.Items) != 1 || st.Found.Items[0].Name != "Right knee, patellofemoral pain" {
		t.Errorf("left to pick = %+v", st.Found.Items)
	}
	j.post("/medical/found/clear", nil)
	if j.app.store.snapshot().Found != nil {
		t.Error("Dismiss All did not clear the list")
	}
}

func TestJourneyLearningByCompany(t *testing.T) {
	j := newJourney(t)
	if code, body := j.get("/learning"); code != 200 || !strings.Contains(body, "No companies yet") {
		t.Fatalf("empty Learning page: %d", code)
	}
	j.post("/learning/companies", url.Values{"name": {"Acme Radar"}, "role": {"Integration engineer"}, "url": {"acme.example/careers"}})
	co := j.app.store.snapshot().Companies[0]
	if co.URL != "https://acme.example/careers" {
		t.Errorf("careers link = %q", co.URL)
	}
	cid := strconv.Itoa(co.ID)
	j.post("/learning/companies/"+cid+"/skills", url.Values{"name": {"Link 16 J-series"}, "area": {"Protocols"}})
	j.post("/learning/companies/"+cid+"/skills", url.Values{"name": {"JREAP-C"}, "area": {"Protocols"}})
	j.post("/learning/companies/"+cid+"/skills", url.Values{"name": {"Kubernetes"}, "area": {"Platform"}})
	if f := flashOf(j.postResp("/learning/companies/"+cid+"/skills", url.Values{"name": {" "}})); !strings.HasPrefix(f, "err|") {
		t.Errorf("blank skill flash = %q", f)
	}
	st := j.app.store.snapshot()
	if len(st.Skills) != 3 || st.Skills[0].Status != "learn" {
		t.Fatalf("skills = %+v", st.Skills)
	}

	// Status cycles To Learn, Learning, Mastered, and back.
	sid := strconv.Itoa(st.Skills[0].ID)
	for _, want := range []string{"learning", "mastered", "learn", "learning"} {
		j.post("/learning/skills/"+sid+"/status", nil)
		if got := j.app.store.snapshot().Skills[0].Status; got != want {
			t.Fatalf("status = %q, want %q", got, want)
		}
	}
	j.post("/learning/skills/"+strconv.Itoa(st.Skills[2].ID)+"/update", url.Values{"name": {"Kubernetes operators"}, "area": {"Platform"}, "status": {"mastered"}})
	v := learningView(j.app.store.snapshot())
	if len(v) != 1 || v[0].Total != 3 || v[0].Mastered != 1 || v[0].Learning != 1 || v[0].Pct != 33 ||
		len(v[0].Areas) != 2 || v[0].Areas[0].Name != "Protocols" || len(v[0].Areas[0].Skills) != 2 {
		t.Errorf("view = %+v", v)
	}
	if _, body := j.get("/learning"); !strings.Contains(body, "Kubernetes operators") || !strings.Contains(body, "1 of 3 mastered") {
		t.Error("the page does not show the edited skill or progress")
	}

	// The Advisor can add a skill, and makes the company when it is new.
	_ = j.app.store.mutate(func(st *State) {
		addSkillTool(st, "acme radar", "SIMPLE", "Protocols", "", "")
		addSkillTool(st, "New Co", "Pulumi", "Tools", "pulumi.com/docs", "")
	})
	st = j.app.store.snapshot()
	if len(st.Companies) != 2 || len(st.Skills) != 5 || st.Skills[3].CompanyID != co.ID {
		t.Errorf("advisor add: %d companies, %d skills", len(st.Companies), len(st.Skills))
	}

	// Deleting a company takes its skills with it.
	j.post("/learning/companies/"+cid+"/delete", nil)
	st = j.app.store.snapshot()
	if len(st.Companies) != 1 || len(st.Skills) != 1 || st.Skills[0].Name != "Pulumi" {
		t.Errorf("after delete: %+v %+v", st.Companies, st.Skills)
	}
}

func TestJourneyCompaniesAndResumesLink(t *testing.T) {
	j := newJourney(t)
	// A company starts a tailored resume, carrying the posting.
	j.post("/learning/companies", url.Values{"name": {"Acme Radar"}, "role": {"TDL engineer"}, "posting": {"Link 16, JREAP-C"}})
	st := j.app.store.snapshot()
	co := st.Companies[0]
	tg := st.findTarget(co.TargetID)
	if tg == nil || tg.Position != "TDL engineer" || tg.Company != "Acme Radar" || tg.Requirements != "Link 16, JREAP-C" {
		t.Fatalf("company did not start a resume: %+v", tg)
	}
	// A resume adds a company, or links to the one with that name.
	j.post("/resume/targets", url.Values{"position": {"Platform engineer"}, "company": {"Beta Systems"}})
	st = j.app.store.snapshot()
	if len(st.Companies) != 2 || st.Companies[1].Name != "Beta Systems" || st.findTarget(st.Companies[1].TargetID) == nil {
		t.Fatalf("resume did not add a company: %+v", st.Companies)
	}
	// Editing the company's posting updates the resume's.
	j.post("/learning/companies/"+strconv.Itoa(co.ID)+"/update", url.Values{"name": {"Acme Radar"}, "posting": {"Link 16, JREAP-C, SIMPLE"}})
	if got := tgt(j.app.store.snapshot(), co.TargetID).Requirements; got != "Link 16, JREAP-C, SIMPLE" {
		t.Errorf("shared posting = %q", got)
	}
	// Deleting the resume keeps the card, unlinked; the card can start another.
	j.post("/resume/targets/"+strconv.Itoa(co.TargetID)+"/delete", nil)
	st = j.app.store.snapshot()
	if st.findTarget(co.TargetID) != nil || st.Companies[0].TargetID != 0 {
		t.Fatalf("resume delete: target still there or card still linked")
	}
	j.post("/learning/companies/"+strconv.Itoa(co.ID)+"/resume", nil)
	st = j.app.store.snapshot()
	if st.findTarget(st.Companies[0].TargetID) == nil {
		t.Fatal("Start Tailored Resume did not make one")
	}
	// Deleting the company keeps its resume.
	tid := st.Companies[0].TargetID
	j.post("/learning/companies/"+strconv.Itoa(co.ID)+"/delete", nil)
	if tgt(j.app.store.snapshot(), tid) == nil {
		t.Error("deleting a company deleted its resume")
	}
	// Every resume page has a working delete outside the save form.
	if _, body := j.get("/resume?t=" + strconv.Itoa(tid)); !strings.Contains(body, `class="target-delete"`) {
		t.Error("no delete form for the open resume")
	}
}

func TestJourneyGenerateButtons(t *testing.T) {
	j := newJourney(t)
	reply := ""
	var prompts []string
	askAIFunc = func(_ context.Context, _, p, _ string) (string, error) {
		prompts = append(prompts, p)
		return reply, nil
	}
	defer func() { askAIFunc = askAI }()
	_ = j.app.store.mutate(func(st *State) { st.Settings.AdvisorProvider = "claude" })
	j.post("/learning/companies", url.Values{"name": {"Acme Radar"}, "role": {"TDL engineer"}, "posting": {"Must know JREAP-C"}})
	st := j.app.store.snapshot()
	cid, tid := st.Companies[0].ID, st.Companies[0].TargetID
	j.post("/learning/companies/"+strconv.Itoa(cid)+"/skills", url.Values{"name": {"Link 16"}, "area": {"Protocols"}})

	// Generate Topics adds new skills only, and records the fit.
	reply = `{"fit":"You already run ASTERIX chains.","skills":[{"name":"link 16","area":"Protocols","why":"dup"},{"name":"JREAP-C","area":"Protocols","why":"Posting asks for it.","url":"example.org/jreap"}]}`
	j.post("/learning/companies/"+strconv.Itoa(cid)+"/generate", nil)
	st = j.app.store.snapshot()
	if len(st.Skills) != 2 || st.Skills[1].Name != "JREAP-C" || st.Skills[1].URL != "https://example.org/jreap" || st.Companies[0].Fit == "" {
		t.Fatalf("generate topics: %+v fit %q", st.Skills, st.Companies[0].Fit)
	}
	if p := prompts[len(prompts)-1]; !strings.Contains(p, "Must know JREAP-C") || !strings.Contains(p, "leave out: Link 16") || !strings.Contains(p, "Never invent") {
		t.Errorf("topics prompt missing the posting, the skip list, or the no-invent rule:\n%s", p)
	}

	// Resume: headline, new section, rewrite.
	base := "/resume/targets/" + strconv.Itoa(tid)
	reply = `{"headline":"Tactical data link integrator"}`
	j.post(base+"/generate/headline", nil)
	reply = `{"heading":"Integration","bullets":["- Integrated radar tracks into C2", ""]}`
	j.post(base+"/generate/section", url.Values{"focus": {"radar work"}})
	tg := tgt(j.app.store.snapshot(), tid)
	if tg.Headline != "Tactical data link integrator" || len(tg.Sections) != 1 || len(tg.Sections[0].Bullets) != 1 || tg.Sections[0].Bullets[0] != "Integrated radar tracks into C2" {
		t.Fatalf("headline or section: %+v", tg)
	}
	reply = `{"bullets":["Integrated radar tracks into a JREAP-C-ready C2 picture"]}`
	j.post(base+"/sections/"+strconv.Itoa(tg.Sections[0].ID)+"/generate", nil)
	if b := tgt(j.app.store.snapshot(), tid).Sections[0].Bullets[0]; !strings.Contains(b, "JREAP-C") {
		t.Errorf("rewrite = %q", b)
	}

	// Mastered skills go on the resume as Technical Skills, rerunnable.
	if f := flashOf(j.postResp(base+"/learning-skills", nil)); !strings.HasPrefix(f, "err|") {
		t.Errorf("no mastered skills yet should refuse, got %q", f)
	}
	j.post("/learning/skills/"+strconv.Itoa(st.Skills[1].ID)+"/update", url.Values{"name": {"JREAP-C"}, "area": {"Protocols"}, "status": {"mastered"}})
	j.post(base+"/learning-skills", nil)
	j.post(base+"/learning-skills", nil)
	tg = tgt(j.app.store.snapshot(), tid)
	if n := len(tg.Sections); n != 2 || tg.Sections[1].Heading != "Technical Skills" || tg.Sections[1].Bullets[0] != "Protocols: JREAP-C" {
		t.Errorf("skills section: %+v", tg.Sections)
	}

	// A bad reply is refused with a notice, and nothing changes.
	reply = "sorry"
	if f := flashOf(j.postResp(base+"/generate/headline", nil)); !strings.HasPrefix(f, "err|") {
		t.Errorf("unreadable reply flash = %q", f)
	}
}

func tgt(st State, id int) *ResumeTarget { return st.findTarget(id) }

func TestLinkCompanyToExistingResume(t *testing.T) {
	j := newJourney(t)
	j.post("/resume/targets", url.Values{"position": {"Engineer"}, "company": {"Acme"}, "requirements": {"Link 16"}})
	j.post("/learning/companies", url.Values{"name": {"Acme Systems"}})
	st := j.app.store.snapshot()
	var old, card int
	for _, c := range st.Companies {
		if c.Name == "Acme" {
			old = c.TargetID
		} else {
			card = c.ID
		}
	}
	j.post("/learning/companies/"+strconv.Itoa(card)+"/update", url.Values{"name": {"Acme Systems"}, "target": {strconv.Itoa(old)}, "posting": {"stale box"}})
	st = j.app.store.snapshot()
	for _, c := range st.Companies {
		if c.ID == card && c.TargetID != old {
			t.Errorf("card not linked to resume %d: %+v", old, c)
		}
	}
	if got := tgt(st, old).Requirements; got != "Link 16" {
		t.Errorf("switching resumes overwrote the posting with the old box: %q", got)
	}
}

func TestMarkdownIsSafeAndComplete(t *testing.T) {
	src := "# Title\n\nSummary here.\n\n## Core Concepts\n\nText with **bold**, *ital*, `a<b>` and [ok](https://x.example/a?b=1&c=2) and [bad](javascript:alert(1)).\n\n" +
		"<script>alert(1)</script>\n\n- one\n- two\n\n1. first\n2. second\n\n```bash\necho \"<hi>\" && ls\n```\n\n| A | B |\n|---|---|\n| 1 | <i>2</i> |\n\n> **Warning:** careful\n\n### Sub\n"
	out, toc := renderMarkdown(src)
	h := string(out)
	for _, bad := range []string{"<script", "javascript:", "<i>2"} {
		if strings.Contains(h, bad) {
			t.Errorf("rendered HTML contains %q:\n%s", bad, h)
		}
	}
	for _, want := range []string{"<strong>bold</strong>", "<em>ital</em>", "<code>a&lt;b&gt;</code>", `href="https://x.example/a?b=1&amp;c=2"`,
		"<ul>", "<ol>", `<code class="lang-bash">echo &#34;&lt;hi&gt;&#34; &amp;&amp; ls</code>`, "<th>A</th>", `md-warning`, `<h3 id="core-concepts">`, `<h4 id="sub">`} {
		if !strings.Contains(h, want) {
			t.Errorf("missing %q in:\n%s", want, h)
		}
	}
	if len(toc) != 2 || toc[0].Text != "Core Concepts" {
		t.Errorf("toc = %+v", toc)
	}
	title, sum := mdTitle(src)
	if title != "Title" || sum != "Summary here." {
		t.Errorf("title %q summary %q", title, sum)
	}
	_, refs := splitSection("# T\n\n## References\n\n- [Go](https://go.dev/doc/) - the docs\n- [x](javascript:1) - no\n\n## After\n", "References")
	if r := parseRefs(refs); len(r) != 1 || r[0].URL != "https://go.dev/doc/" || r[0].Note != "the docs" {
		t.Errorf("refs = %+v", r)
	}
}

func TestJourneyTopicAndCompanyPages(t *testing.T) {
	j := newJourney(t)
	j.post("/learning/companies", url.Values{"name": {"Acme Radar"}, "role": {"TDL engineer"}})
	cid := j.app.store.snapshot().Companies[0].ID
	j.post("/learning/companies/"+strconv.Itoa(cid)+"/skills", url.Values{"name": {"JREAP-C"}, "area": {"Protocols"}})
	sk := j.app.store.snapshot().Skills[0]
	page := "/learning/skills/" + strconv.Itoa(sk.ID)
	if code, body := j.get(page); code != 200 || !strings.Contains(body, "No handbook for this topic yet") {
		t.Fatalf("topic page without a handbook: %d", code)
	}

	askAIFunc = func(_ context.Context, _, _, _ string) (string, error) {
		return "```markdown\n# JREAP-C Handbook\n\nWhat JREAP-C is.\n\n## Core Concepts\n\nIt carries J-series over IP.\n\n## References\n\n- [MIL-STD-3011 overview](https://example.mil/jreap) - public summary\n```", nil
	}
	defer func() { askAIFunc = askAI }()
	_ = j.app.store.mutate(func(st *State) { st.Settings.AdvisorProvider = "claude" })
	j.post(page+"/generate-handbook", nil)
	sk = j.app.store.snapshot().Skills[0]
	if sk.Handbook != "jreap-c" {
		t.Fatalf("handbook slug = %q", sk.Handbook)
	}
	_, body := j.get(page)
	for _, want := range []string{"JREAP-C Handbook", "It carries J-series over IP.", "MIL-STD-3011 overview", `id="references"`} {
		if !strings.Contains(body, want) {
			t.Errorf("topic page missing %q", want)
		}
	}

	// Add and remove a reference; they persist in the file.
	j.post("/handbooks/jreap-c/refs", url.Values{"title": {"Guidebook"}, "url": {"example.org/guide"}, "note": {"primer"}, "back": {page}})
	if h := j.app.loadHandbook("jreap-c"); len(h.Refs) != 2 || h.Refs[1].URL != "https://example.org/guide" {
		t.Fatalf("after add: %+v", h.Refs)
	}
	j.post("/handbooks/jreap-c/refs/delete", url.Values{"url": {"https://example.mil/jreap"}, "back": {page}})
	if h := j.app.loadHandbook("jreap-c"); len(h.Refs) != 1 || h.Refs[0].Title != "Guidebook" {
		t.Fatalf("after delete: %+v", h.Refs)
	}
	// A back link off this app is ignored.
	if loc := j.postResp("/handbooks/jreap-c/refs/delete", url.Values{"url": {"x"}, "back": {"https://evil.example/"}}).Header.Get("Location"); strings.Contains(loc, "evil") {
		t.Errorf("redirected off-app: %s", loc)
	}
	// Path tricks never reach the file system.
	if code, _ := j.get("/handbooks/..%2Fstate"); code == 200 {
		t.Error("a bad slug rendered a page")
	}

	// The company page lists the topic, the Documents page lists the handbook.
	if _, body := j.get("/learning/companies/" + strconv.Itoa(cid)); !strings.Contains(body, "JREAP-C") || !strings.Contains(body, "Generate Profile") {
		t.Error("company page is missing the topic or the profile button")
	}
	if _, body := j.get("/docs"); !strings.Contains(body, "/handbooks/jreap-c") {
		t.Error("Documents does not list the handbook")
	}
	if _, body := j.get("/handbooks/jreap-c"); !strings.Contains(body, "used by") {
		t.Error("handbook page does not show which topics use it")
	}
}

func TestRetirePayEmptyStillShowsHealth(t *testing.T) {
	p := estimateRetirePay(State{})
	if p.Health.PlanLabel != "TRICARE Prime" || p.Health.Group != "A" || p.Health.Tricare != 3183 {
		t.Errorf("empty settings health = %+v", p.Health)
	}
}

func TestJourneyClaimEvidenceUpload(t *testing.T) {
	j := newJourney(t)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("kind", "evidence")
	_ = mw.WriteField("notes", "Buddy statement")
	for _, n := range []string{"buddy.pdf", "photo.jpg"} {
		fw, _ := mw.CreateFormFile("files", n)
		_, _ = fw.Write([]byte("%PDF-1.4 test"))
	}
	mw.Close()
	req, _ := http.NewRequest("POST", j.srv.URL+"/docs/bulk", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Referer", j.srv.URL+"/medical")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := j.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if loc := resp.Header.Get("Location"); loc != "/medical" {
		t.Errorf("upload went back to %q, want /medical", loc)
	}
	n := 0
	for _, d := range j.app.store.snapshot().Docs {
		if d.Kind == "evidence" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("evidence docs = %d, want 2", n)
	}
	if _, body := j.get("/medical"); !strings.Contains(body, "buddy.pdf") || !strings.Contains(body, `id="evidence"`) || strings.Contains(body, "spring 2027") {
		t.Error("Claim Evidence card is missing the upload, or shows personal text")
	}
}

func TestVAFundingFee(t *testing.T) {
	for _, c := range []struct {
		pct   float64
		first bool
		want  float64
	}{{0, true, 2.15}, {4.9, true, 2.15}, {5, true, 1.5}, {10, true, 1.25}, {0, false, 3.3}, {6, false, 1.5}} {
		if got := fundingFeeRate(c.pct, c.first); got != c.want {
			t.Errorf("rate(%v%%, first %v) = %v, want %v", c.pct, c.first, got, c.want)
		}
	}
	// VA's own example: $200,000 home, $10,000 down, first use: 1.5% of $190,000 = $2,850.
	f := estimateFee(Settings{HomePrice: 20000000, HomeDown: 1000000})
	if !f.Set || f.Fee != 285000 || f.Exempt {
		t.Errorf("VA example = %+v", f)
	}
	if !estimateFee(Settings{HomePrice: 20000000, VaEstimate: 30}).Exempt {
		t.Error("a VA rating should make the fee exempt")
	}
}

func TestJourneyAgentsAndLenders(t *testing.T) {
	j := newJourney(t)
	j.post("/home-team/agents", url.Values{"name": {"Pat Lee"}, "brokerage": {"Acme Realty"}, "mrp": {"1"}})
	j.post("/home-team/lenders", url.Values{"name": {"Big Bank"}, "kind": {"Bank"}, "rate": {"6.5"}, "apr": {"6.71%"}, "costs": {"5,100"}})
	j.post("/home-team/lenders", url.Values{"name": {"Local CU"}, "kind": {"Credit union"}, "rate": {"6.25"}, "apr": {"6.40"}, "costs": {"3,900"}})
	if f := flashOf(j.postResp("/home-team/lenders", url.Values{"name": {"X"}, "costs": {"lots"}})); !strings.HasPrefix(f, "err|") {
		t.Errorf("bad costs flash = %q", f)
	}
	st := j.app.store.snapshot()
	if len(st.Agents) != 1 || !st.Agents[0].MRP || len(st.Lenders) != 2 || st.Lenders[0].APR != "6.71" || st.Lenders[1].Costs != 390000 {
		t.Fatalf("saved: %+v %+v", st.Agents, st.Lenders)
	}
	j.post("/home-team/agents/"+strconv.Itoa(st.Agents[0].ID)+"/update", url.Values{"name": {"Pat Lee"}, "status": {"interviewed"}})
	if a := j.app.store.snapshot().Agents[0]; a.Status != "interviewed" || a.MRP {
		t.Errorf("edited agent = %+v (unchecked MRP should clear)", a)
	}
	_, body := j.get("/home-team")
	if i, k := strings.Index(body, "Local CU"), strings.Index(body, "Big Bank"); i < 0 || k < 0 || i > k || !strings.Contains(body, "Lowest APR") {
		t.Error("lenders are not sorted lowest APR first with the badge")
	}
	j.post("/home-team/fee", url.Values{"price": {"350,000"}, "down": {"0"}})
	if _, body := j.get("/home-team"); !strings.Contains(body, "$7,525.00") {
		t.Error("funding fee for $350,000 at 0% down, first use, should be $7,525.00")
	}
}

func TestMapBBoxAndPlaces(t *testing.T) {
	if _, err := parseBBox("-86.7,34.6,-86.5,34.8", 0.5); err != nil {
		t.Errorf("good bbox refused: %v", err)
	}
	for _, bad := range []string{"", "1,2,3", "-86,34,-87,35", "-90,30,-80,40", "a,b,c,d", "NaN,1,2,3"} {
		if _, err := parseBBox(bad, 0.5); err == nil {
			t.Errorf("bbox %q accepted", bad)
		}
	}
	if yearRate(map[string]float64{"01": 10, "02": 20.04}) != 30 {
		t.Error("yearRate should sum monthly rates to one decimal")
	}
	if d := milesBetween(34.73, -86.59, 34.73, -86.59); d != 0 {
		t.Errorf("zero distance = %v", d)
	}
	if d := milesBetween(34.7304, -86.5861, 33.5186, -86.8104); d < 83 || d > 85 {
		t.Errorf("Huntsville to Birmingham = %.1f miles, want about 84", d)
	}
	j := newJourney(t)
	if code, body := j.get("/map/places?kind=nope&bbox=-86.7,34.6,-86.5,34.8"); code != http.StatusBadGateway || !strings.Contains(body, "unknown place kind") {
		t.Errorf("unknown kind: %d %s", code, body)
	}
	if _, body := j.get("/map/center"); !strings.Contains(body, `"ok":false`) {
		t.Error("an empty app should not have a map center")
	}
	if _, body := j.get("/housing"); !strings.Contains(body, `id="hood-map"`) || !strings.Contains(body, "/static/vendor/leaflet/leaflet.js") {
		t.Error("Housing is missing the map")
	}
}

func TestItemizeHomeAndRV(t *testing.T) {
	if got := firstYearInterest(350000, 6.25, 30); math.Abs(got-21758.84) > 0.01 {
		t.Errorf("home first-year interest = %.2f, want 21758.84", got)
	}
	if got := payment(35000000, 6.25, 30); got != 215501 {
		t.Errorf("P&I = %d cents, want 215501", got)
	}
	// Married, $4,000 a month retired pay in Alabama (exempt), 60% rating
	// (no funding fee), $350,000 home at 6.25%, $1,800 property tax.
	s := Settings{RetYears: 20, RetHigh3: 800000, RetirementDate: "2027-11-01", RetSpouse: true, VaEstimate: 60, TaxState: "AL",
		OwnHome: true, HomePrice: 35000000, HomeRate: 6.25, PropTax: 180000}
	tax := estimateRetirePay(State{Settings: s}).Tax
	// 21,759 interest + 1,800 property tax = 23,559, under the $32,200 standard.
	if it := tax.Itemized; it.Better || it.Total != 23559 || it.Standard != 32200 {
		t.Errorf("home only: %+v", it)
	}
	// Add an $80,000 RV at 8% over 15 years with $4,000 sales tax: interest
	// 6,296, and the sales tax beats Alabama income tax on exempt pay (0).
	s.OwnRV, s.RVIsHome, s.RVLoan, s.RVRate, s.RVYears, s.RVSalesTax = true, true, 8000000, 8, 15, 400000
	tax = estimateRetirePay(State{Settings: s}).Tax
	it := tax.Itemized
	if !it.Better || it.RVInterest != 6296 || !it.SALTUsesSales || it.SALT != 5800 || it.Total != 33855 {
		t.Fatalf("home and RV: %+v", it)
	}
	// Federal on $48,000: standard 15,800 taxable = 1,580; itemized 14,145 = 1,414.50.
	if it.Saves != 166 {
		t.Errorf("saves = %d, want 166", it.Saves)
	}
	// Without sleeping, cooking, and toilet the RV interest does not count.
	s.RVIsHome = false
	if it := estimateRetirePay(State{Settings: s}).Tax.Itemized; it.RVInterest != 0 || !it.RVNotHome {
		t.Errorf("RV not a home: %+v", it)
	}
	if c := saltCap(600000); c != 11900 {
		t.Errorf("SALT cap at $600k = %v, want 11,900", c)
	}
	if c := saltCap(900000); c != 10000 {
		t.Errorf("SALT cap floor = %v", c)
	}
}

func TestHomePlanUsesQuotesAndSavings(t *testing.T) {
	st := State{Settings: Settings{RetYears: 20, RetHigh3: 800000, RetirementDate: "2027-11-01", OwnHome: true,
		HomePrice: 30000000, HomeDown: 0, PropTax: 120000, HomeIns: 144000},
		Lenders: []Lender{{Name: "Big Bank", Rate: "6.5", Costs: 500000}, {Name: "Local CU", Rate: "6.25", Costs: 390000}},
		Savings: []SavingsEntry{{ID: 1, Date: "2026-09-01", Amount: 1000000}}}
	p := estimateRetirePay(st)
	h := p.Home
	// No VA rating: first-use fee 2.15% of $300,000 = $6,450, financed.
	if h.Fee != 645000 || h.Loan != 30645000 || h.Rate != 6.25 || h.RateFrom != "Local CU" {
		t.Fatalf("loan and rate: %+v", h)
	}
	if h.PI != payment(30645000, 6.25, 30) || h.Tax != 10000 || h.Ins != 12000 {
		t.Errorf("monthly pieces: %+v", h)
	}
	if h.Closing != 390000 || h.ClosingFrom != "Local CU" || h.Cash != 390000 || h.SavingsAfter != 610000 {
		t.Errorf("closing and savings: %+v", h)
	}
	if h.Left != p.TakeHome-h.Total {
		t.Errorf("left = %d, want take-home %d minus housing %d", h.Left, p.TakeHome, h.Total)
	}
}

func TestCarLoanInterestDeduction(t *testing.T) {
	base := Settings{RetYears: 20, RetHigh3: 800000, RetirementDate: "2027-11-01", RetSpouse: true, VaEstimate: 60, TaxState: "AL",
		OwnCar: true, CarLoan: 3200000, CarRate: 6.9, CarYears: 6, CarNew: true, CarUS: true}
	in := firstYearInterest(32000, 6.9, 6)
	tx := estimateRetirePay(State{Settings: base}).Tax
	it := tx.Itemized
	if it.CarInterest != int64(in+0.5) || it.CarDed != it.CarInterest || it.CarWhyNot != "" {
		t.Fatalf("new US car: %+v", it)
	}
	// It lowers federal tax without itemizing: $48,000 - 32,200 - interest.
	want := bracketTax(48000-32200-float64(it.CarDed), fedJoint)
	if tx.Federal != cents(want) || it.Better {
		t.Errorf("federal = %d, want %d (standard deduction plus car interest)", tx.Federal, cents(want))
	}
	used := base
	used.CarNew = false
	if it := estimateRetirePay(State{Settings: used}).Tax.Itemized; it.CarDed != 0 || !strings.Contains(it.CarWhyNot, "Used") {
		t.Errorf("used car: %+v", it)
	}
	foreign := base
	foreign.CarUS = false
	if it := estimateRetirePay(State{Settings: foreign}).Tax.Itemized; it.CarDed != 0 {
		t.Errorf("foreign assembly should not qualify: %+v", it)
	}
	// Joint income of $230,000: $30,000 over, so $6,000 off the deduction.
	rich := base
	rich.CivSalary = 18200000
	if it := estimateRetirePay(State{Settings: rich}).Tax.Itemized; !it.CarPhasedOut || it.CarDed != int64(max(min(in, 10000)-6000, 0)+0.5) {
		t.Errorf("phase-out: %+v", it)
	}
	// The payment and yearly tag tax join the monthly total.
	base.CarPropTax = 60000
	h := estimateRetirePay(State{Settings: base}).Home
	if h.CarPay != payment(3200000, 6.9, 6)+5000 || h.Total != h.CarPay {
		t.Errorf("car pay: %+v", h)
	}
}

func TestContractorFilters(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]string{"Posted Today": "2026-09-30", "Posted Yesterday": "2026-09-29", "Posted 8 Days Ago": "2026-09-22", "Posted 30+ Days Ago": "2026-08-31"} {
		if got := workdayPosted(in, now); got != want {
			t.Errorf("workdayPosted(%q) = %s, want %s", in, got, want)
		}
	}
	if !matchesWords("systems engineer", "Senior Systems Engineer II") || matchesWords("systems engineer", "Program Manager") {
		t.Error("matchesWords")
	}
	hits := []jobHit{
		{Title: "Systems Engineer", Location: "Huntsville, AL"},
		{Title: "Systems Engineer", Location: "USA AL Redstone Arsenal"},
		{Title: "Systems Engineer", Location: "Springfield, VA"},
		{Title: "Systems Engineer (Remote)", Location: "United States"},
		{Title: "Systems Engineer", Location: "3 Locations"},
	}
	count := func(mode string) int { return len(filterByMode(append([]jobHit(nil), hits...), mode, "AL")) }
	if n := count("near"); n != 3 {
		t.Errorf("near AL = %d, want 3 (two in Alabama plus the unnamed multi-location)", n)
	}
	if n := count("remote"); n != 1 {
		t.Errorf("remote = %d, want 1", n)
	}
	if n := count("both"); n != 4 {
		t.Errorf("both = %d, want 4", n)
	}
	if n := count("anywhere"); n != 5 {
		t.Errorf("anywhere = %d, want 5", n)
	}
	if on := employersOn(Settings{EmployersOff: []string{"gdit", "bah"}}); len(on) != len(employers)-2 {
		t.Errorf("employersOn = %d", len(on))
	}
}

func TestJourneyEmployerPick(t *testing.T) {
	j := newJourney(t)
	j.post("/jobs/employers", url.Values{"on": {"gdit", "caci"}})
	if on := employersOn(j.app.store.snapshot().Settings); len(on) != 2 || on[0].Key != "gdit" || on[1].Key != "caci" {
		t.Errorf("saved employers = %+v", on)
	}
	var (
		mu       sync.Mutex
		searched []string
	)
	employerSearch = func(e employer, q string) ([]jobHit, error) {
		mu.Lock()
		searched = append(searched, e.Key)
		mu.Unlock()
		return []jobHit{{Title: "Systems Engineer", Org: e.Name, Location: "Huntsville, AL", URL: "https://example.com/" + e.Key, Source: e.Name, Posted: "2026-09-29"}}, nil
	}
	_, body := j.get("/jobs?q=systems+engineer&zip=35801&radius=25&mode=both")
	if len(searched) != 2 || !strings.Contains(body, "https://example.com/caci") || !strings.Contains(body, "Defense Contractors Searched (2 of") {
		t.Errorf("searched %v; page shows contractor jobs: %v", searched, strings.Contains(body, "example.com/caci"))
	}
}
