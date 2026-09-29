package main

// Resume preview designs. Each target keeps its own design and light or dark
// paper, so the resume for one position can look different from another.

import "net/http"

// resumeDesigns is display order: {key, label}.
var resumeDesigns = [][2]string{
	{"classic", "Classic"},
	{"modern", "Modern"},
	{"executive", "Executive"},
	{"minimal", "Minimal"},
	{"sidebar", "Sidebar"},
}

// resumeFonts and resumeColors are {key, label}; "" means the design's own.
var resumeFonts = [][2]string{
	{"", "Design Default"},
	{"inter", "Inter"},
	{"plex", "IBM Plex Sans"},
	{"sourcesans", "Source Sans"},
	{"sourceserif", "Source Serif"},
	{"lora", "Lora"},
	{"garamond", "EB Garamond"},
}

var resumeColors = [][2]string{
	{"", "Design Default"},
	{"navy", "Navy"},
	{"gold", "Army Gold"},
	{"forest", "Forest"},
	{"burgundy", "Burgundy"},
	{"charcoal", "Charcoal"},
	{"teal", "Teal"},
}

func knownKey(list [][2]string, k string) bool {
	for _, v := range list {
		if v[0] == k {
			return true
		}
	}
	return false
}

// PaperClass is the class list the preview and the print page share.
func (t ResumeTarget) PaperClass() string {
	c := "resume-paper rp-" + t.Design
	if t.Design == "" {
		c = "resume-paper rp-classic"
	}
	if t.Dark {
		c += " rp-dark"
	}
	if t.Font != "" {
		c += " rp-f-" + t.Font
	}
	if t.Color != "" {
		c += " rp-c-" + t.Color
	}
	return c
}

func knownResumeDesign(k string) bool {
	for _, d := range resumeDesigns {
		if d[0] == k {
			return true
		}
	}
	return false
}

func (s *Server) resumeStyle(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	design := r.FormValue("design")
	if !knownResumeDesign(design) {
		http.Error(w, "unknown design", http.StatusBadRequest)
		return
	}
	_ = s.store.mutate(func(st *State) {
		if t := st.findTarget(id); t != nil {
			t.Design = design
			if design == "classic" {
				t.Design = "" // the default stays out of state.json
			}
			t.Dark = r.FormValue("dark") == "1"
			if f := r.FormValue("font"); knownKey(resumeFonts, f) {
				t.Font = f
			}
			if c := r.FormValue("color"); knownKey(resumeColors, c) {
				t.Color = c
			}
		}
	})
	w.WriteHeader(http.StatusNoContent)
}
