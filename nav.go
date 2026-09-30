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
		{"debt", "Debt", "/debt", "card"},
		{"savings", "Savings", "/savings", "piggy"},
		{"retirepay", "Retired Pay", "/retired-pay", "dollar"},
	}},
	{"Career", []navItem{
		{"resume", "Resume", "/resume", "resume"},
		{"itp", "ITP", "/itp", "compass"},
		{"jobs", "Jobs", "/jobs", "briefcase"},
		{"skillbridge", "SkillBridge", "/skillbridge", "bridge"},
		{"learning", "Learning", "/learning", "book"},
	}},
	{"Next Home", []navItem{
		{"housing", "Housing", "/housing", "house"},
		{"resources", "Resources", "/resources", "link"},
	}},
}

// navIcons are 24px stroke icons drawn for this app (stroke comes from CSS).
var navIcons = map[string]string{
	"book":      `<path d="M4 5.5A2.5 2.5 0 0 1 6.5 3H20v15H6.5A2.5 2.5 0 0 0 4 20.5z"/><path d="M4 20.5A2.5 2.5 0 0 0 6.5 23H20v-5M8 7h8M8 11h6"/>`,
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
	"card":      `<rect x="2.5" y="5" width="19" height="14" rx="2.5"/><path d="M2.5 9.5h19M6 15h4"/>`,
	"piggy":     `<path d="M5 11a7 5.5 0 0 1 13.2-1.6L21 9v4l-2 .8A7 5.5 0 0 1 16 16.5V19h-3v-1.6a8 8 0 0 1-3 0V19H7v-2.8A5.4 5.4 0 0 1 5 11z"/><path d="M11 6.5h3"/><circle cx="16" cy="11" r=".6"/>`,
	"chart":     `<path d="M4 20V10M10 20V4M16 20v-7M22 20H2"/>`,
	"dollar":    `<path d="M12 3v18M16.5 7.5c0-1.9-2-3-4.5-3s-4.5 1.2-4.5 3.1c0 4.4 9 2.4 9 6.8 0 2-2 3.1-4.5 3.1s-4.5-1.1-4.5-3"/>`,
	"sparkle":   `<path d="M12 3l1.8 5.2L19 10l-5.2 1.8L12 17l-1.8-5.2L5 10l5.2-1.8z"/><path d="M19 16l.7 1.8 1.8.7-1.8.7L19 21l-.7-1.8-1.8-.7 1.8-.7z"/>`,
	"user":      `<circle cx="12" cy="8" r="4"/><path d="M4 21c1.2-4 4.3-6 8-6s6.8 2 8 6"/>`,
	"weather":   `<path d="M8 3v1.5M3.5 7.5H2M4.8 4.3l1 1M12.2 4.3l-1 1"/><path d="M5.2 10.6A3.3 3.3 0 1 1 11 7.7"/><path d="M8 20h9a3.5 3.5 0 0 0 .4-7 5 5 0 0 0-9.6 1.4A2.8 2.8 0 0 0 8 20z"/>`,
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
