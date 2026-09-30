package main

// Housing: live for-sale listings around a ZIP code, refreshed in the
// background. The feed is realtor.com's public search endpoint, the same one
// its own website calls. Only listings that are still for sale survive a
// sweep: sold, off-market, pending, and contingent homes drop out on the next
// refresh, so the page never shows a house you cannot buy.
//
// The search lives in state.json (State.Housing). The listing cache lives in
// data/homes.json so the personal state file stays small and the "New" badge
// survives a restart.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type HouseSearch struct {
	Zip      string   `json:"zip"`
	Radius   int      `json:"radius_miles"`
	MinPrice int      `json:"min_price"` // whole dollars, 0 = no floor
	MaxPrice int      `json:"max_price"` // whole dollars, 0 = no ceiling
	MinBeds  int      `json:"min_beds"`
	Types    []string `json:"types"`
}

// homeTypes are the realtor.com property types offered as filters, in display
// order. Land and farms are left out on purpose: this is a house hunt.
var homeTypes = []struct{ Key, Label string }{
	{"single_family", "House"},
	{"townhomes", "Townhome"},
	{"condos", "Condo"},
	{"multi_family", "Multi-Family"},
	{"mobile", "Mobile"},
}

func defaultHouseSearch() HouseSearch {
	return HouseSearch{Zip: "", Radius: 25, Types: []string{"single_family", "townhomes", "condos"}}
}

// key fingerprints the search so a filter change starts a fresh baseline.
func (h HouseSearch) key() string {
	t := slices.Clone(h.Types)
	slices.Sort(t)
	return fmt.Sprintf("%s|%d|%d|%d|%d|%s", h.Zip, h.Radius, h.MinPrice, h.MaxPrice, h.MinBeds, strings.Join(t, ","))
}

type Home struct {
	ID        string  `json:"id"`
	URL       string  `json:"url"`
	Photo     string  `json:"photo"`
	Address   string  `json:"address"`
	City      string  `json:"city"`
	State     string  `json:"state"`
	Zip       string  `json:"zip"`
	Type      string  `json:"type"`
	Price     int     `json:"price"`
	Cut       int     `json:"cut,omitempty"` // latest price reduction, dollars
	Beds      int     `json:"beds"`
	Baths     string  `json:"baths"`
	Sqft      int     `json:"sqft"`
	Year      int     `json:"year,omitempty"`
	Listed    string  `json:"listed"`        // RFC3339 list date from the feed
	FirstSeen string  `json:"first_seen"`    // RFC3339, when this app first saw it
	Lat       float64 `json:"lat,omitempty"` // for the neighborhood map
	Lon       float64 `json:"lon,omitempty"`
}

type homeCache struct {
	Key       string `json:"key"`
	Baseline  string `json:"baseline"` // first sweep for this search; nothing in it counts as new
	FetchedAt string `json:"fetched_at"`
	Total     int    `json:"total"` // matches upstream, before the cap
	Err       string `json:"err,omitempty"`
	ErrAt     string `json:"err_at,omitempty"`
	Homes     []Home `json:"homes"`
}

const (
	homeRefresh  = 30 * time.Minute
	homePageSize = 200
	homeMax      = 1000 // newest first; wider searches are truncated, not paged forever
)

// homeFeed owns the cache and the background refresher.
type homeFeed struct {
	mu    sync.Mutex
	path  string
	cache homeCache
	busy  bool
	kick  chan struct{}
	store *Store
}

func newHomeFeed(dataDir string, store *Store) *homeFeed {
	f := &homeFeed{path: filepath.Join(dataDir, "homes.json"), kick: make(chan struct{}, 1), store: store}
	if b, err := os.ReadFile(f.path); err == nil {
		_ = json.Unmarshal(b, &f.cache)
	}
	return f
}

func (st State) houseSearch() HouseSearch {
	if st.Housing == nil {
		return defaultHouseSearch()
	}
	return *st.Housing
}

// run refreshes on start when the cache is stale or for another search, then
// every homeRefresh, and immediately whenever kicked.
func (f *homeFeed) run() {
	t := time.NewTicker(homeRefresh)
	defer t.Stop()
	f.mu.Lock()
	stale := true
	if at, err := time.Parse(time.RFC3339, f.cache.FetchedAt); err == nil {
		hs := f.store.snapshot().houseSearch()
		stale = time.Since(at) > homeRefresh || f.cache.Key != hs.key()
	}
	f.mu.Unlock()
	if stale {
		f.refresh()
	}
	for {
		select {
		case <-t.C:
		case <-f.kick:
		}
		f.refresh()
	}
}

// reset forgets every listing, on disk and in memory.
func (f *homeFeed) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cache = homeCache{}
	_ = os.Remove(f.path)
}

func (f *homeFeed) poke() {
	select {
	case f.kick <- struct{}{}:
	default:
	}
}

func (f *homeFeed) snapshot() (homeCache, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.cache
	c.Homes = slices.Clone(f.cache.Homes)
	return c, f.busy
}

func (f *homeFeed) refresh() {
	hs := f.store.snapshot().houseSearch()
	if hs.Zip == "" {
		return // nothing to search until the user sets a ZIP code
	}
	f.mu.Lock()
	f.busy = true
	f.mu.Unlock()

	homes, total, err := fetchHomes(hs)

	f.mu.Lock()
	defer f.mu.Unlock()
	f.busy = false
	now := time.Now().UTC().Format(time.RFC3339)
	if err != nil {
		// Keep the last good sweep on screen; a failed fetch must never empty the page.
		f.cache.Err, f.cache.ErrAt = err.Error(), now
		log.Printf("housing: refresh failed: %v", err)
		f.saveLocked()
		return
	}
	prev := map[string]Home{}
	if f.cache.Key == hs.key() {
		for _, h := range f.cache.Homes {
			prev[h.ID] = h
		}
	} else {
		f.cache.Baseline = now
	}
	for i := range homes {
		if old, ok := prev[homes[i].ID]; ok {
			homes[i].FirstSeen = old.FirstSeen
			if homes[i].Cut == 0 && homes[i].Price < old.Price {
				homes[i].Cut = old.Price - homes[i].Price
			}
		} else {
			homes[i].FirstSeen = now
		}
	}
	f.cache = homeCache{Key: hs.key(), Baseline: f.cache.Baseline, FetchedAt: now, Total: total, Homes: homes}
	if f.cache.Baseline == "" {
		f.cache.Baseline = now
	}
	f.saveLocked()
}

func (f *homeFeed) saveLocked() {
	if b, err := json.MarshalIndent(f.cache, "", "  "); err == nil {
		_ = writeFileAtomic(f.path, b)
	}
}

const homeQuery = `query ConsumerSearchQuery($query: HomeSearchCriteria!, $limit: Int, $offset: Int, $sort: [SearchAPISort]) {
  home_search(query: $query, limit: $limit, offset: $offset, sort: $sort) {
    total count
    results {
      property_id status href list_price list_date price_reduced_amount
      flags { is_pending is_contingent }
      description { type beds baths_consolidated sqft year_built }
      location { address { line city state_code postal_code coordinate { lat lon } } }
      primary_photo(https: true) { href }
    }
  }
}`

type rdcResult struct {
	PropertyID string  `json:"property_id"`
	Status     string  `json:"status"`
	Href       string  `json:"href"`
	ListPrice  float64 `json:"list_price"`
	ListDate   string  `json:"list_date"`
	Reduced    float64 `json:"price_reduced_amount"`
	Flags      struct {
		Pending    bool `json:"is_pending"`
		Contingent bool `json:"is_contingent"`
	} `json:"flags"`
	Description struct {
		Type  string `json:"type"`
		Beds  int    `json:"beds"`
		Baths string `json:"baths_consolidated"`
		Sqft  int    `json:"sqft"`
		Year  int    `json:"year_built"`
	} `json:"description"`
	Location struct {
		Address struct {
			Line  string `json:"line"`
			City  string `json:"city"`
			State string `json:"state_code"`
			Zip   string `json:"postal_code"`
			Coord struct {
				Lat float64 `json:"lat"`
				Lon float64 `json:"lon"`
			} `json:"coordinate"`
		} `json:"address"`
	} `json:"location"`
	Photo struct {
		Href string `json:"href"`
	} `json:"primary_photo"`
}

func fetchHomes(hs HouseSearch) ([]Home, int, error) {
	q := map[string]any{
		"status":          []string{"for_sale"},
		"search_location": map[string]any{"location": hs.Zip, "buffer": hs.Radius},
	}
	if len(hs.Types) > 0 {
		q["type"] = hs.Types
	}
	if hs.MinBeds > 0 {
		q["beds"] = map[string]int{"min": hs.MinBeds}
	}
	if hs.MinPrice > 0 || hs.MaxPrice > 0 {
		p := map[string]int{}
		if hs.MinPrice > 0 {
			p["min"] = hs.MinPrice
		}
		if hs.MaxPrice > 0 {
			p["max"] = hs.MaxPrice
		}
		q["list_price"] = p
	}
	client := &http.Client{Timeout: 25 * time.Second}
	var out []Home
	total := 0
	for offset := 0; offset < homeMax; offset += homePageSize {
		body, _ := json.Marshal(map[string]any{
			"operationName": "ConsumerSearchQuery",
			"query":         homeQuery,
			"variables": map[string]any{
				"query":  q,
				"sort":   []map[string]string{{"field": "list_date", "direction": "desc"}},
				"limit":  homePageSize,
				"offset": offset,
			},
		})
		req, err := http.NewRequest("POST", "https://www.realtor.com/frontdoor/graphql", bytes.NewReader(body))
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0 Safari/537.36")
		req.Header.Set("rdc-client-name", "RDC_WEB_SRP_FS_PAGE")
		req.Header.Set("rdc-client-version", "3.0.2515")
		resp, err := client.Do(req)
		if err != nil {
			return nil, 0, err
		}
		var page struct {
			Data struct {
				HomeSearch struct {
					Total   int         `json:"total"`
					Results []rdcResult `json:"results"`
				} `json:"home_search"`
			} `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, 0, fmt.Errorf("realtor.com returned %s", resp.Status)
		}
		if err != nil {
			return nil, 0, fmt.Errorf("realtor.com sent an unreadable reply: %w", err)
		}
		if len(page.Errors) > 0 {
			return nil, 0, fmt.Errorf("realtor.com: %s", page.Errors[0].Message)
		}
		hsr := page.Data.HomeSearch
		total = hsr.Total
		for _, r := range hsr.Results {
			if r.Status != "for_sale" || r.Flags.Pending || r.Flags.Contingent || r.Href == "" {
				continue
			}
			a := r.Location.Address
			out = append(out, Home{
				ID: r.PropertyID, URL: r.Href, Photo: r.Photo.Href,
				Address: a.Line, City: a.City, State: a.State, Zip: a.Zip,
				Type: r.Description.Type, Price: int(r.ListPrice), Cut: int(r.Reduced),
				Beds: r.Description.Beds, Baths: r.Description.Baths, Sqft: r.Description.Sqft,
				Year: r.Description.Year, Listed: r.ListDate, Lat: a.Coord.Lat, Lon: a.Coord.Lon,
			})
		}
		if len(hsr.Results) < homePageSize || offset+homePageSize >= total {
			break
		}
		time.Sleep(700 * time.Millisecond) // be a polite client
	}
	return out, total, nil
}

// ---------- Views -----------------------------------------------------------

type homeCard struct {
	Home
	TypeLabel string
	PriceText string
	CutText   string
	SqftText  string
	Age       string // "Listed today", "Listed 4 days ago"
	Days      int
	New       bool
}

func dollars(n int) string {
	s := strconv.Itoa(n)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return "$" + b.String()
}

func homeTypeLabel(k string) string {
	for _, t := range homeTypes {
		if t.Key == k {
			return t.Label
		}
	}
	return strings.ReplaceAll(k, "_", " ")
}

func homeCards(c homeCache) []homeCard {
	now := time.Now()
	baseline, _ := time.Parse(time.RFC3339, c.Baseline)
	cards := make([]homeCard, 0, len(c.Homes))
	for _, h := range c.Homes {
		hc := homeCard{Home: h, TypeLabel: homeTypeLabel(h.Type), PriceText: dollars(h.Price), Days: -1}
		if h.Cut > 0 {
			hc.CutText = dollars(h.Cut)
		}
		if h.Sqft > 0 {
			hc.SqftText = strings.TrimPrefix(dollars(h.Sqft), "$")
		}
		if t, err := time.Parse(time.RFC3339, h.Listed); err == nil {
			hc.Days = int(now.Sub(t).Hours() / 24)
			switch hc.Days {
			case 0:
				hc.Age = "Listed today"
			case 1:
				hc.Age = "Listed yesterday"
			default:
				hc.Age = fmt.Sprintf("Listed %d days ago", hc.Days)
			}
		}
		// New = listed in the last 3 days, or it appeared after this search's
		// first sweep and within the last 3 days.
		seen, _ := time.Parse(time.RFC3339, h.FirstSeen)
		hc.New = (hc.Days >= 0 && hc.Days <= 2) || (seen.After(baseline) && now.Sub(seen) < 72*time.Hour)
		cards = append(cards, hc)
	}
	return cards
}

func (s *Server) housingData() map[string]any {
	st := s.store.snapshot()
	hs := st.houseSearch()
	c, busy := s.homes.snapshot()
	current := c.Key == hs.key()
	var cards []homeCard
	if current {
		cards = homeCards(c)
	}
	newCount := 0
	for _, hc := range cards {
		if hc.New {
			newCount++
		}
	}
	fetched := ""
	if t, err := time.Parse(time.RFC3339, c.FetchedAt); err == nil && current {
		fetched = t.Local().Format("Jan 2, 15:04")
	}
	type typeOpt struct {
		Key, Label string
		On         bool
	}
	var types []typeOpt
	for _, t := range homeTypes {
		types = append(types, typeOpt{t.Key, t.Label, slices.Contains(hs.Types, t.Key)})
	}
	return map[string]any{
		"HS": hs, "Types": types, "Cards": cards, "NewCount": newCount,
		"Fetched": fetched, "Busy": hs.Zip != "" && (busy || !current), "Err": c.Err,
		"NeedZip": hs.Zip == "",
		"Total":   c.Total, "Capped": current && c.Total > len(c.Homes) && len(c.Homes) >= homeMax-homePageSize,
		"Radii": []int{5, 10, 15, 25, 35, 50},
		"Stamp": c.FetchedAt + c.ErrAt,
	}
}

func (s *Server) housing(w http.ResponseWriter, r *http.Request) {
	data := s.housingData()
	if r.URL.Query().Get("frag") == "1" {
		// The grid alone, swapped in place by the page's poller.
		var buf bytes.Buffer
		if err := s.tmpl.ExecuteTemplate(&buf, "housing-grid", data); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = buf.WriteTo(w)
		return
	}
	s.page(w, "housing", data)
}

// housingStamp is the cheap poll: the page swaps its grid when the stamp moves.
func (s *Server) housingStamp(w http.ResponseWriter, r *http.Request) {
	c, busy := s.homes.snapshot()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"stamp": c.FetchedAt + c.ErrAt, "busy": busy})
}

func (s *Server) housingSearch(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	atoi := func(k string) int {
		v := strings.NewReplacer("$", "", ",", "", " ", "").Replace(r.FormValue(k))
		n, _ := strconv.Atoi(v)
		if n < 0 {
			n = 0
		}
		return n
	}
	hs := HouseSearch{
		Zip:      strings.TrimSpace(r.FormValue("zip")),
		Radius:   atoi("radius"),
		MinPrice: atoi("min_price"),
		MaxPrice: atoi("max_price"),
		MinBeds:  atoi("min_beds"),
	}
	for _, t := range r.Form["type"] {
		for _, known := range homeTypes {
			if t == known.Key {
				hs.Types = append(hs.Types, t)
			}
		}
	}
	if len(hs.Zip) != 5 || strings.Trim(hs.Zip, "0123456789") != "" {
		hs.Zip = "" // not a US ZIP; the page asks again
	}
	if hs.Radius <= 0 || hs.Radius > 100 {
		hs.Radius = defaultHouseSearch().Radius
	}
	if hs.MaxPrice > 0 && hs.MinPrice > hs.MaxPrice {
		hs.MinPrice, hs.MaxPrice = hs.MaxPrice, hs.MinPrice
	}
	_ = s.store.mutate(func(st *State) { st.Housing = &hs })
	s.homes.poke()
	http.Redirect(w, r, "/housing", http.StatusSeeOther)
}

func (s *Server) housingRefresh(w http.ResponseWriter, r *http.Request) {
	s.homes.poke()
	http.Redirect(w, r, "/housing", http.StatusSeeOther)
}
