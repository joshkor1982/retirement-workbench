package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	"/packet", "/budget", "/debt", "/savings", "/retired-pay", "/resume", "/itp", "/jobs", "/housing", "/resources", "/settings"}

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
	if len(rows) != 2 || !strings.HasPrefix(rows[0].Label, "Near 80903") || rows[1].Label != "Remote" {
		t.Fatalf("rows: %+v", rows)
	}
	li := rows[0].Links[0].URL
	if !strings.Contains(li, "keywords=logistics+manager") || !strings.Contains(li, "location=80903") || !strings.Contains(li, "distance=50") {
		t.Errorf("LinkedIn near link: %s", li)
	}
	if !strings.Contains(rows[1].Links[0].URL, "f_WT=2") || !strings.Contains(rows[1].Links[1].URL, "l=Remote") {
		t.Errorf("remote links: %s | %s", rows[1].Links[0].URL, rows[1].Links[1].URL)
	}
	// No ZIP and no saved location: remote is the only search that makes sense.
	if parseJobQuery("analyst", "", "", "both", "").Mode != "remote" {
		t.Error("blank ZIP should search remote only")
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
		"Errs": []string{"USAJOBS: key rejected"},
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
