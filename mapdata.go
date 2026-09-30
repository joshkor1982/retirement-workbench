package main

// The neighborhood map on Housing. The browser draws it with Leaflet
// (vendored in static/vendor/leaflet) over OpenStreetMap tiles, and asks this
// server for everything else, so each outside service is called in one
// place, politely, and cached:
//
//   - Flood zones: FEMA's National Flood Hazard Layer, drawn by the browser
//     straight from FEMA's map-image service (images only, no data comes here).
//   - Population density: 2020 Census tracts from TIGERweb, people per
//     square mile from POP100 and AREALAND.
//   - Public schools: NCES EDGE school locations.
//   - Places (grocery, shopping, health, parks, and more): OpenStreetMap
//     through the Overpass API.
//   - Crime: the FBI Crime Data Explorer, reported by police agency (a city
//     police department or county sheriff), not by neighborhood. Needs a free
//     api.data.gov key; DEMO_KEY works a few times an hour.
//   - Homes for sale: the Housing feed, which carries coordinates.
//
// Nothing is fetched until you open the map, and only for the area in view.

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var mapClient = &http.Client{Timeout: 25 * time.Second}

type cached struct {
	at   time.Time
	body []byte
}

var (
	mapMu    sync.Mutex
	mapCache = map[string]cached{}
)

// getJSON fetches a URL with a cache in front of it.
func getJSON(u string, ttl time.Duration) ([]byte, error) {
	mapMu.Lock()
	if c, ok := mapCache[u]; ok && time.Since(c.at) < ttl {
		mapMu.Unlock()
		return c.body, nil
	}
	mapMu.Unlock()
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("User-Agent", "ARW-Army-Retirement-Workbench/1.0 (+https://github.com/joshkor1982/retirement-workbench)")
	resp, err := mapClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s answered %d", hostOf(u), resp.StatusCode)
	}
	mapMu.Lock()
	mapCache[u] = cached{time.Now(), body}
	mapMu.Unlock()
	return body, nil
}

func hostOf(u string) string {
	if p, err := url.Parse(u); err == nil {
		return p.Host
	}
	return "the service"
}

type bbox struct{ W, S, E, N float64 }

// parseBBox reads "west,south,east,north", rounded out to 0.01 degree so
// small pans share a cache entry, and refuses anything larger than a metro
// area.
func parseBBox(s string, maxSpan float64) (bbox, error) {
	p := strings.Split(s, ",")
	if len(p) != 4 {
		return bbox{}, fmt.Errorf("bbox needs four numbers")
	}
	var v [4]float64
	for i := range p {
		f, err := strconv.ParseFloat(strings.TrimSpace(p[i]), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return bbox{}, fmt.Errorf("bad bbox")
		}
		v[i] = f
	}
	b := bbox{math.Floor(v[0]*100) / 100, math.Floor(v[1]*100) / 100, math.Ceil(v[2]*100) / 100, math.Ceil(v[3]*100) / 100}
	if b.W >= b.E || b.S >= b.N || b.S < -90 || b.N > 90 || b.W < -180 || b.E > 180 {
		return bbox{}, fmt.Errorf("bad bbox")
	}
	if b.E-b.W > maxSpan || b.N-b.S > maxSpan {
		return bbox{}, fmt.Errorf("zoom in to see this layer")
	}
	return b, nil
}

func (b bbox) esri() string {
	return fmt.Sprintf("%.2f,%.2f,%.2f,%.2f", b.W, b.S, b.E, b.N)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func mapErr(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadGateway)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func (s *Server) routeMap(mux *http.ServeMux) {
	mux.HandleFunc("GET /map/center", s.mapCenter)
	mux.HandleFunc("GET /map/homes", s.mapHomes)
	mux.HandleFunc("GET /map/tracts", s.mapTracts)
	mux.HandleFunc("GET /map/schools", s.mapSchools)
	mux.HandleFunc("GET /map/places", s.mapPlaces)
	mux.HandleFunc("GET /map/crime", s.mapCrime)
}

// mapCenter is the Housing ZIP code's location.
func (s *Server) mapCenter(w http.ResponseWriter, r *http.Request) {
	st := s.store.snapshot()
	q := homeZip(st)
	if q == "" {
		q = strings.TrimSpace(st.Settings.WeatherRetire)
	}
	if q == "" {
		writeJSON(w, map[string]any{"ok": false})
		return
	}
	p, err := wxGeocode(q)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "lat": p.Lat, "lon": p.Lon, "label": p.Label, "state": stateFromPlace(q)})
}

type feature struct {
	Type       string         `json:"type"`
	Geometry   any            `json:"geometry"`
	Properties map[string]any `json:"properties"`
}

func point(lon, lat float64, props map[string]any) feature {
	return feature{"Feature", map[string]any{"type": "Point", "coordinates": []float64{lon, lat}}, props}
}

func collection(fs []feature) map[string]any {
	if fs == nil {
		fs = []feature{}
	}
	return map[string]any{"type": "FeatureCollection", "features": fs}
}

// mapHomes are the listings in the Housing feed that carry a location.
func (s *Server) mapHomes(w http.ResponseWriter, r *http.Request) {
	c, _ := s.homes.snapshot()
	var fs []feature
	for _, h := range c.Homes {
		if h.Lat == 0 && h.Lon == 0 {
			continue
		}
		fs = append(fs, point(h.Lon, h.Lat, map[string]any{"url": h.URL, "price": dollars(h.Price), "address": h.Address + ", " + h.City,
			"beds": h.Beds, "baths": h.Baths, "sqft": h.Sqft, "photo": h.Photo}))
	}
	writeJSON(w, collection(fs))
}

// mapTracts returns 2020 Census tracts in view with people per square mile.
func (s *Server) mapTracts(w http.ResponseWriter, r *http.Request) {
	b, err := parseBBox(r.URL.Query().Get("bbox"), 1.2)
	if err != nil {
		mapErr(w, err)
		return
	}
	v := url.Values{"where": {"1=1"}, "geometry": {b.esri()}, "geometryType": {"esriGeometryEnvelope"}, "inSR": {"4326"},
		"spatialRel": {"esriSpatialRelIntersects"}, "outFields": {"GEOID,BASENAME,POP100,AREALAND"}, "outSR": {"4326"},
		"maxAllowableOffset": {"0.0005"}, "geometryPrecision": {"5"}, "f": {"geojson"}}
	body, err := getJSON("https://tigerweb.geo.census.gov/arcgis/rest/services/TIGERweb/tigerWMS_Census2020/MapServer/6/query?"+v.Encode(), 7*24*time.Hour)
	if err != nil {
		mapErr(w, err)
		return
	}
	var fc struct {
		Features []feature `json:"features"`
	}
	if json.Unmarshal(body, &fc) != nil {
		mapErr(w, fmt.Errorf("the Census service sent an unreadable reply"))
		return
	}
	for i := range fc.Features {
		p := fc.Features[i].Properties
		pop, _ := p["POP100"].(float64)
		land, _ := p["AREALAND"].(float64)
		d := 0.0
		if land > 0 {
			d = pop / (land / 2589988.11) // square meters in a square mile
		}
		fc.Features[i].Properties = map[string]any{"name": "Tract " + fmt.Sprint(p["BASENAME"]), "pop": int(pop), "density": int(d + 0.5)}
	}
	writeJSON(w, collection(fc.Features))
}

// mapSchools returns public schools in view from NCES.
func (s *Server) mapSchools(w http.ResponseWriter, r *http.Request) {
	b, err := parseBBox(r.URL.Query().Get("bbox"), 0.8)
	if err != nil {
		mapErr(w, err)
		return
	}
	v := url.Values{"where": {"1=1"}, "geometry": {b.esri()}, "geometryType": {"esriGeometryEnvelope"}, "inSR": {"4326"},
		"outFields": {"NAME,STREET,CITY"}, "outSR": {"4326"}, "resultRecordCount": {"500"}, "f": {"geojson"}}
	body, err := getJSON("https://nces.ed.gov/opengis/rest/services/K12_School_Locations/EDGE_GEOCODE_PUBLICSCH_2324/MapServer/0/query?"+v.Encode(), 7*24*time.Hour)
	if err != nil {
		mapErr(w, err)
		return
	}
	var fc struct {
		Features []feature `json:"features"`
	}
	if json.Unmarshal(body, &fc) != nil {
		mapErr(w, fmt.Errorf("the NCES service sent an unreadable reply"))
		return
	}
	var out []feature
	for _, f := range fc.Features {
		name := fmt.Sprint(f.Properties["NAME"])
		if name == "" || strings.Contains(name, "Child Count") {
			continue // reporting placeholders, not buildings
		}
		f.Properties = map[string]any{"name": name, "address": strings.Trim(fmt.Sprint(f.Properties["STREET"])+", "+fmt.Sprint(f.Properties["CITY"]), ", ")}
		out = append(out, f)
	}
	writeJSON(w, collection(out))
}

// placeKinds are the OpenStreetMap tags behind each place layer.
var placeKinds = map[string][]string{
	"grocery":   {`nwr["shop"~"^(supermarket|greengrocer|wholesale)$"]`},
	"shopping":  {`nwr["shop"~"^(mall|department_store|hardware|doityourself)$"]`},
	"dining":    {`nwr["amenity"~"^(restaurant|cafe)$"]`},
	"health":    {`nwr["amenity"~"^(hospital|clinic|doctors)$"]`, `nwr["healthcare"="urgent_care"]`},
	"pharmacy":  {`nwr["amenity"="pharmacy"]`},
	"parks":     {`nwr["leisure"~"^(park|dog_park|nature_reserve)$"]["name"]`},
	"fitness":   {`nwr["leisure"~"^(fitness_centre|sports_centre)$"]`},
	"fuel":      {`nwr["amenity"="fuel"]`},
	"library":   {`nwr["amenity"="library"]`},
	"safety":    {`nwr["amenity"~"^(fire_station|police)$"]`},
	"military":  {`nwr["landuse"="military"]["name"]`, `nwr["military"~"^(base|airfield)$"]["name"]`},
	"worship":   {`nwr["amenity"="place_of_worship"]["name"]`},
	"childcare": {`nwr["amenity"~"^(childcare|kindergarten)$"]`},
}

// mapPlaces asks Overpass for one kind of place in view.
func (s *Server) mapPlaces(w http.ResponseWriter, r *http.Request) {
	tags, ok := placeKinds[r.URL.Query().Get("kind")]
	if !ok {
		mapErr(w, fmt.Errorf("unknown place kind"))
		return
	}
	b, err := parseBBox(r.URL.Query().Get("bbox"), 0.5)
	if err != nil {
		mapErr(w, err)
		return
	}
	area := fmt.Sprintf("(%.2f,%.2f,%.2f,%.2f)", b.S, b.W, b.N, b.E)
	var q strings.Builder
	q.WriteString("[out:json][timeout:25];(")
	for _, t := range tags {
		q.WriteString(t + area + ";")
	}
	q.WriteString(");out center tags 400;")
	// The main Overpass server is often busy; public mirrors run the same API.
	var body []byte
	// Busy answers (429, 504) usually clear within seconds, so each server
	// gets a second try before the next.
	for _, host := range []string{"https://overpass-api.de", "https://overpass-api.de", "https://maps.mail.ru/osm/tools/overpass"} {
		if body, err = getJSON(host+"/api/interpreter?data="+url.QueryEscape(q.String()), 24*time.Hour); err == nil {
			break
		}
		time.Sleep(1500 * time.Millisecond)
	}
	if err != nil {
		err = fmt.Errorf("OpenStreetMap's free place search is busy right now; switch the layer off and on in a minute")
	}
	if err != nil {
		mapErr(w, err)
		return
	}
	var res struct {
		Elements []struct {
			Lat, Lon float64
			Center   *struct{ Lat, Lon float64 }
			Tags     map[string]string
		}
	}
	if json.Unmarshal(body, &res) != nil {
		mapErr(w, fmt.Errorf("the OpenStreetMap service sent an unreadable reply"))
		return
	}
	var fs []feature
	for _, e := range res.Elements {
		lat, lon := e.Lat, e.Lon
		if e.Center != nil {
			lat, lon = e.Center.Lat, e.Center.Lon
		}
		if lat == 0 && lon == 0 {
			continue
		}
		t := e.Tags
		what := cmpOr(t["shop"], cmpOr(t["amenity"], cmpOr(t["leisure"], cmpOr(t["healthcare"], cmpOr(t["military"], t["landuse"])))))
		fs = append(fs, point(lon, lat, map[string]any{"name": cmpOr(t["name"], strings.ReplaceAll(what, "_", " ")),
			"what": strings.ReplaceAll(what, "_", " "), "address": strings.TrimSpace(t["addr:housenumber"] + " " + t["addr:street"])}))
	}
	writeJSON(w, collection(fs))
}

// ---------- Crime (FBI Crime Data Explorer) ---------------------------------

type crimeAgency struct {
	ORI, Name, Type   string
	Lat, Lon          float64
	Miles             float64
	Violent, Property float64 // offenses per 100,000 people for the year
	StateV, StateP    float64
	USV, USP          float64
	Year              int
	Population        int
}

const cdeBase = "https://api.usa.gov/crime/fbi/cde/"

func (s *Server) dataGovKey() string {
	return cmpOr(s.store.snapshot().Settings.DataGovKey, "DEMO_KEY")
}

// yearRate sums a year of monthly rates (each per 100,000 people).
func yearRate(m map[string]float64) float64 {
	t := 0.0
	for _, v := range m {
		t += v
	}
	return math.Round(t*10) / 10
}

// agencyRates fetches one offense group for an agency and year.
func (s *Server) agencyRates(ori, offense string, year int) (agency, state, us float64, pop int, err error) {
	u := fmt.Sprintf("%ssummarized/agency/%s/%s?from=01-%d&to=12-%d&API_KEY=%s", cdeBase, ori, offense, year, year, url.QueryEscape(s.dataGovKey()))
	body, err := getJSON(u, 30*24*time.Hour)
	if err != nil {
		return
	}
	var d struct {
		Offenses struct {
			Rates map[string]map[string]float64 `json:"rates"`
		} `json:"offenses"`
		Populations struct {
			Population map[string]map[string]float64 `json:"population"`
		} `json:"populations"`
	}
	if err = json.Unmarshal(body, &d); err != nil {
		return
	}
	for k, m := range d.Offenses.Rates {
		switch {
		case !strings.HasSuffix(k, " Offenses"):
		case k == "United States Offenses":
			us = yearRate(m)
		case len(m) == 12 && agency == 0 && !strings.HasPrefix(k, "United States"):
			// The agency's key is its name; the state's is the state name.
			name := strings.TrimSuffix(k, " Offenses")
			if _, isState := d.Populations.Population[name]; isState && stateNameSet[name] {
				state = yearRate(m)
			} else {
				agency = yearRate(m)
			}
		}
	}
	for k, m := range d.Populations.Population {
		if !stateNameSet[k] && k != "United States" {
			for _, v := range m {
				pop = max(pop, int(v))
			}
		}
	}
	return
}

var stateNameSet = func() map[string]bool {
	m := map[string]bool{}
	for _, n := range stateNames {
		m[n] = true
	}
	return m
}()

func milesBetween(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 3958.8
	p1, p2 := lat1*math.Pi/180, lat2*math.Pi/180
	dp, dl := (lat2-lat1)*math.Pi/180, (lon2-lon1)*math.Pi/180
	a := math.Sin(dp/2)*math.Sin(dp/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dl/2)*math.Sin(dl/2)
	return 2 * r * math.Asin(math.Sqrt(a))
}

// mapCrime lists the police agencies nearest a point with their violent and
// property crime rates for the latest full year the FBI has.
func (s *Server) mapCrime(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lat, e1 := strconv.ParseFloat(q.Get("lat"), 64)
	lon, e2 := strconv.ParseFloat(q.Get("lon"), 64)
	st := strings.ToUpper(q.Get("state"))
	if e1 != nil || e2 != nil || stateNames[st] == "" {
		mapErr(w, fmt.Errorf("need a location and a state"))
		return
	}
	body, err := getJSON(cdeBase+"agency/byStateAbbr/"+st+"?API_KEY="+url.QueryEscape(s.dataGovKey()), 30*24*time.Hour)
	if err != nil {
		mapErr(w, fmt.Errorf("FBI crime data: %v (add a free api.data.gov key in Settings)", err))
		return
	}
	var byCounty map[string][]struct {
		ORI  string  `json:"ori"`
		Name string  `json:"agency_name"`
		Type string  `json:"agency_type_name"`
		Lat  float64 `json:"latitude"`
		Lon  float64 `json:"longitude"`
	}
	if json.Unmarshal(body, &byCounty) != nil {
		mapErr(w, fmt.Errorf("the FBI service sent an unreadable reply"))
		return
	}
	var near []crimeAgency
	seen := map[string]bool{}
	for _, list := range byCounty {
		for _, a := range list {
			if seen[a.ORI] || a.Lat == 0 || (a.Type != "City" && a.Type != "County") {
				continue // campus, airport, and state agencies do not cover a neighborhood
			}
			seen[a.ORI] = true
			if d := milesBetween(lat, lon, a.Lat, a.Lon); d <= 30 {
				near = append(near, crimeAgency{ORI: a.ORI, Name: a.Name, Type: a.Type, Lat: a.Lat, Lon: a.Lon, Miles: math.Round(d*10) / 10})
			}
		}
	}
	sort.Slice(near, func(i, j int) bool { return near[i].Miles < near[j].Miles })
	if len(near) > 8 {
		near = near[:8]
	}
	var wg sync.WaitGroup
	for i := range near {
		wg.Add(1)
		go func(a *crimeAgency) {
			defer wg.Done()
			for _, year := range []int{time.Now().Year() - 1, time.Now().Year() - 2} {
				v, sv, uv, pop, err := s.agencyRates(a.ORI, "violent-crime", year)
				if err != nil || v == 0 {
					continue
				}
				a.Violent, a.StateV, a.USV, a.Population, a.Year = v, sv, uv, pop, year
				// A failed second call (often the DEMO_KEY hourly limit) is
				// unknown, not zero: -1 tells the map to say so.
				a.Property, a.StateP, a.USP = -1, -1, -1
				if p, sp, up, _, err := s.agencyRates(a.ORI, "property-crime", year); err == nil && p > 0 {
					a.Property, a.StateP, a.USP = p, sp, up
				}
				return
			}
		}(&near[i])
	}
	wg.Wait()
	var out []crimeAgency
	for _, a := range near {
		if a.Year > 0 {
			out = append(out, a)
		}
	}
	writeJSON(w, map[string]any{"agencies": out})
}
