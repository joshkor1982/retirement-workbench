package main

// Weather for where you are now and where you plan to retire, from
// Open-Meteo (open-meteo.com): free, no API key, CC BY 4.0, so the card
// credits it. A US ZIP code is placed with zippopotam.us; anything else,
// such as "Wiesbaden", goes through Open-Meteo's worldwide place search.
// Nothing is fetched until you enter a place, and each place is cached for
// 30 minutes.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type wxPlace struct {
	Label    string  `json:"label"` // "Colorado Springs, CO"
	Lat, Lon float64 `json:"-"`
}

type wxDay struct {
	Day  string `json:"day"` // "Wed"
	Code int    `json:"code"`
	Hi   int    `json:"hi"`
	Lo   int    `json:"lo"`
}

type wxReport struct {
	Role     string  `json:"role"` // "Where you are now" / "Where you plan to retire"
	Query    string  `json:"query"`
	Place    wxPlace `json:"place"`
	Temp     int     `json:"temp"`
	Feels    int     `json:"feels"`
	Code     int     `json:"code"`
	IsDay    bool    `json:"is_day"`
	Wind     int     `json:"wind"`
	Humidity int     `json:"humidity"`
	Hi       int     `json:"hi"`
	Lo       int     `json:"lo"`
	Days     []wxDay `json:"days"`
	Err      string  `json:"err,omitempty"`
}

var (
	wxMu    sync.Mutex
	wxCache = map[string]struct {
		at  time.Time
		rep wxReport
	}{}
)

func wxGeocode(q string) (wxPlace, error) {
	q = strings.TrimSpace(q)
	if len(q) == 5 && strings.Trim(q, "0123456789") == "" {
		city := zipCity(q)
		if city == "" {
			return wxPlace{}, fmt.Errorf("ZIP code %s not found", q)
		}
		// zipCity gives the name; ask zippopotam again for coordinates.
		resp, err := (&http.Client{Timeout: 6 * time.Second}).Get("https://api.zippopotam.us/us/" + q)
		if err != nil {
			return wxPlace{}, err
		}
		defer resp.Body.Close()
		var body struct {
			Places []struct {
				Lat string `json:"latitude"`
				Lon string `json:"longitude"`
			} `json:"places"`
		}
		if json.NewDecoder(resp.Body).Decode(&body) != nil || len(body.Places) == 0 {
			return wxPlace{}, fmt.Errorf("ZIP code %s not found", q)
		}
		lat, _ := strconv.ParseFloat(body.Places[0].Lat, 64)
		lon, _ := strconv.ParseFloat(body.Places[0].Lon, 64)
		return wxPlace{Label: city, Lat: lat, Lon: lon}, nil
	}
	v := url.Values{"name": {q}, "count": {"1"}, "language": {"en"}, "format": {"json"}}
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Get("https://geocoding-api.open-meteo.com/v1/search?" + v.Encode())
	if err != nil {
		return wxPlace{}, err
	}
	defer resp.Body.Close()
	var body struct {
		Results []struct {
			Name    string  `json:"name"`
			Admin1  string  `json:"admin1"`
			Country string  `json:"country"`
			CC      string  `json:"country_code"`
			Lat     float64 `json:"latitude"`
			Lon     float64 `json:"longitude"`
		} `json:"results"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil || len(body.Results) == 0 {
		return wxPlace{}, fmt.Errorf("could not find %q", q)
	}
	r := body.Results[0]
	label := r.Name
	if r.CC == "US" && r.Admin1 != "" {
		label += ", " + r.Admin1
	} else if r.Country != "" {
		label += ", " + r.Country
	}
	return wxPlace{Label: label, Lat: r.Lat, Lon: r.Lon}, nil
}

func wxFetch(role, q string) wxReport {
	rep := wxReport{Role: role, Query: q}
	key := strings.ToLower(strings.TrimSpace(q))
	wxMu.Lock()
	if c, ok := wxCache[key]; ok && time.Since(c.at) < 30*time.Minute {
		wxMu.Unlock()
		c.rep.Role = role
		return c.rep
	}
	wxMu.Unlock()

	place, err := wxGeocode(q)
	if err != nil {
		rep.Err = err.Error()
		return rep
	}
	rep.Place = place
	v := url.Values{
		"latitude":         {strconv.FormatFloat(place.Lat, 'f', 4, 64)},
		"longitude":        {strconv.FormatFloat(place.Lon, 'f', 4, 64)},
		"current":          {"temperature_2m,apparent_temperature,relative_humidity_2m,weather_code,wind_speed_10m,is_day"},
		"daily":            {"weather_code,temperature_2m_max,temperature_2m_min"},
		"temperature_unit": {"fahrenheit"}, "wind_speed_unit": {"mph"},
		"timezone": {"auto"}, "forecast_days": {"4"},
	}
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Get("https://api.open-meteo.com/v1/forecast?" + v.Encode())
	if err != nil {
		rep.Err = "weather service unreachable"
		return rep
	}
	defer resp.Body.Close()
	var body struct {
		Current struct {
			Temp     float64 `json:"temperature_2m"`
			Feels    float64 `json:"apparent_temperature"`
			Humidity float64 `json:"relative_humidity_2m"`
			Code     int     `json:"weather_code"`
			Wind     float64 `json:"wind_speed_10m"`
			IsDay    int     `json:"is_day"`
		} `json:"current"`
		Daily struct {
			Time []string  `json:"time"`
			Code []int     `json:"weather_code"`
			Max  []float64 `json:"temperature_2m_max"`
			Min  []float64 `json:"temperature_2m_min"`
		} `json:"daily"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&body) != nil {
		rep.Err = "weather service sent an unreadable reply"
		return rep
	}
	round := func(f float64) int { return int(f + 0.5*sign(f)) }
	c := body.Current
	rep.Temp, rep.Feels, rep.Humidity, rep.Code, rep.Wind, rep.IsDay = round(c.Temp), round(c.Feels), round(c.Humidity), c.Code, round(c.Wind), c.IsDay == 1
	d := body.Daily
	for i := range d.Time {
		if i >= len(d.Code) || i >= len(d.Max) || i >= len(d.Min) {
			break
		}
		if i == 0 {
			rep.Hi, rep.Lo = round(d.Max[0]), round(d.Min[0])
			continue
		}
		day := d.Time[i]
		if t, err := time.Parse("2006-01-02", d.Time[i]); err == nil {
			day = t.Format("Mon")
		}
		rep.Days = append(rep.Days, wxDay{Day: day, Code: d.Code[i], Hi: round(d.Max[i]), Lo: round(d.Min[i])})
	}
	wxMu.Lock()
	wxCache[key] = struct {
		at  time.Time
		rep wxReport
	}{time.Now(), rep}
	wxMu.Unlock()
	return rep
}

func sign(f float64) float64 {
	if f < 0 {
		return -1
	}
	return 1
}

// weatherPlaces returns the two places to show, in order. The retirement
// place falls back to the Housing ZIP code.
func weatherPlaces(st State) [][2]string {
	var out [][2]string
	if q := strings.TrimSpace(st.Settings.WeatherHere); q != "" {
		out = append(out, [2]string{"Where you are now", q})
	}
	dest := strings.TrimSpace(st.Settings.WeatherRetire)
	if dest == "" && st.Housing != nil {
		dest = st.Housing.Zip
	}
	if dest != "" {
		out = append(out, [2]string{"Where you plan to retire", dest})
	}
	return out
}

func (s *Server) weatherJSON(w http.ResponseWriter, r *http.Request) {
	places := weatherPlaces(s.store.snapshot())
	reps := make([]wxReport, len(places))
	var wg sync.WaitGroup
	for i, p := range places {
		wg.Add(1)
		go func() { defer wg.Done(); reps[i] = wxFetch(p[0], p[1]) }()
	}
	wg.Wait()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(reps)
}
