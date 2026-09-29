package main

// Sidebar navigation: grouped, in the order a soldier works through the
// transition. Page keys match the names passed to page().

import "html/template"

type navItem struct{ Page, Label, Href, Icon string }
type navGroup struct {
	Label string
	Items []navItem
}

var sideNav = []navGroup{
	{"", []navItem{
		{"dashboard", "Dashboard", "/", "home"},
	}},
	{"Plan", []navItem{
		{"timeline", "Timeline", "/timeline", "timeline"},
		{"todos", "To-Dos", "/todos", "check"},
		{"appointments", "Appointments", "/appointments", "calendar"},
		{"notes", "Notes", "/notes", "note"},
	}},
	{"Records", []navItem{
		{"docs", "Documents", "/docs", "file"},
		{"medical", "Medical / VA", "/medical", "heart"},
		{"packet", "Submit Packet", "/packet", "send"},
	}},
	{"Money", []navItem{
		{"budget", "Budget", "/budget", "wallet"},
	}},
	{"Career", []navItem{
		{"resume", "Resume", "/resume", "resume"},
		{"itp", "ITP", "/itp", "compass"},
		{"jobs", "Jobs", "/jobs", "briefcase"},
		{"skillbridge", "SkillBridge", "/skillbridge", "bridge"},
	}},
	{"Next Home", []navItem{
		{"housing", "Housing", "/housing", "house"},
		{"resources", "Resources", "/resources", "link"},
	}},
}

// navIcons are 24px stroke icons drawn for this app (stroke comes from CSS).
var navIcons = map[string]string{
	"home":      `<path d="M3 11l9-7 9 7"/><path d="M5 10v10h14V10"/>`,
	"timeline":  `<path d="M4 6h10M4 12h16M4 18h7"/><circle cx="17" cy="6" r="2"/><circle cx="14" cy="18" r="2"/>`,
	"check":     `<rect x="4" y="4" width="16" height="16" rx="3"/><path d="M8 12l3 3 5-6"/>`,
	"calendar":  `<rect x="3.5" y="5" width="17" height="15" rx="2"/><path d="M3.5 10h17M8 3v4M16 3v4"/>`,
	"note":      `<path d="M6 3h9l4 4v14H6z"/><path d="M14 3v5h5M9 13h7M9 17h5"/>`,
	"file":      `<path d="M4 7a2 2 0 0 1 2-2h4l2 2h6a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2z"/>`,
	"heart":     `<path d="M12 20s-7-4.4-7-10a4 4 0 0 1 7-2.6A4 4 0 0 1 19 10c0 5.6-7 10-7 10z"/>`,
	"send":      `<path d="M21 3L10 14"/><path d="M21 3l-7 18-4-7-7-4z"/>`,
	"wallet":    `<rect x="3" y="6" width="18" height="13" rx="2"/><path d="M3 10h18M16 14.5h2"/>`,
	"resume":    `<rect x="5" y="3" width="14" height="18" rx="2"/><circle cx="12" cy="9" r="2.5"/><path d="M8.5 16c.7-1.8 2-2.7 3.5-2.7s2.8.9 3.5 2.7"/>`,
	"compass":   `<circle cx="12" cy="12" r="9"/><path d="M15.5 8.5l-2 5-5 2 2-5z"/>`,
	"briefcase": `<rect x="3" y="7" width="18" height="13" rx="2"/><path d="M9 7V5h6v2M3 12h18"/>`,
	"chart":     `<path d="M4 20V10M10 20V4M16 20v-7M22 20H2"/>`,
	"dollar":    `<path d="M12 3v18M16.5 7.5c0-1.9-2-3-4.5-3s-4.5 1.2-4.5 3.1c0 4.4 9 2.4 9 6.8 0 2-2 3.1-4.5 3.1s-4.5-1.1-4.5-3"/>`,
	"sparkle":   `<path d="M12 3l1.8 5.2L19 10l-5.2 1.8L12 17l-1.8-5.2L5 10l5.2-1.8z"/><path d="M19 16l.7 1.8 1.8.7-1.8.7L19 21l-.7-1.8-1.8-.7 1.8-.7z"/>`,
	"user":      `<circle cx="12" cy="8" r="4"/><path d="M4 21c1.2-4 4.3-6 8-6s6.8 2 8 6"/>`,
	"clock":     `<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>`,
	"bridge":    `<path d="M3 17h18M5 17V9M19 17V9M3 9c3 3 6 4.5 9 4.5S18 12 21 9M9 17v-4M15 17v-4"/>`,
	"house":     `<path d="M4 10.5L12 4l8 6.5V20H4z"/><path d="M10 20v-5h4v5"/>`,
	"link":      `<path d="M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1"/><path d="M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1"/>`,
	"settings":  `<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"/>`,
}

// navIcon returns trusted, compile-time SVG markup; never user input.
func navIcon(name string) template.HTML { return template.HTML(navIcons[name]) }

// navIconsJSON hands the icon set to shell.js for card headers.
func navIconsJSON() map[string]string { return navIcons }
