package main

// A small Markdown renderer for handbooks and company profiles. It handles
// the subset those files are written in (headings, paragraphs, one-level
// lists, fenced code, tables, callouts, bold, italic, code, links) and
// nothing else. Every piece of text is HTML-escaped and links must be
// http(s) or a path on this app, so a handbook, even one the Advisor wrote
// from a pasted job posting, can never run script in the page.

import (
	"html"
	"html/template"
	"regexp"
	"strconv"
	"strings"
)

type mdHeading struct {
	Level int
	Text  string
	ID    string
}

type mdRef struct {
	Title, URL, Note string
}

var (
	mdLink   = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	mdCode   = regexp.MustCompile("`([^`]+)`")
	mdBold   = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	mdItal   = regexp.MustCompile(`(^|[^*\w])\*([^*\s][^*]*)\*`)
	mdOL     = regexp.MustCompile(`^\d+\.\s+`)
	mdRefRow = regexp.MustCompile(`^[-*]\s+\[([^\]]+)\]\(([^)\s]+)\)\s*(?:[-:]\s*(.*))?$`)
	mdSlug   = regexp.MustCompile(`[^a-z0-9]+`)
)

func safeURL(u string) string {
	if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") || (strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "//")) {
		return u
	}
	return ""
}

// mdInline escapes a line, then applies code, links, bold, and italic.
// Code spans are cut out first so their contents stay literal.
func mdInline(s string) string {
	var codes []string
	s = mdCode.ReplaceAllStringFunc(s, func(m string) string {
		codes = append(codes, "<code>"+html.EscapeString(m[1:len(m)-1])+"</code>")
		return "\x00" + strconv.Itoa(len(codes)-1) + "\x00"
	})
	var links []string
	s = mdLink.ReplaceAllStringFunc(s, func(m string) string {
		p := mdLink.FindStringSubmatch(m)
		text := html.EscapeString(p[1])
		u := safeURL(p[2])
		if u == "" {
			links = append(links, text)
		} else {
			ext := ""
			if !strings.HasPrefix(u, "/") {
				ext = ` target="_blank" rel="noopener"`
			}
			links = append(links, `<a href="`+html.EscapeString(u)+`"`+ext+`>`+text+`</a>`)
		}
		return "\x01" + strconv.Itoa(len(links)-1) + "\x01"
	})
	s = html.EscapeString(s)
	s = mdBold.ReplaceAllString(s, "<strong>$1</strong>")
	s = mdItal.ReplaceAllString(s, "$1<em>$2</em>")
	for i, l := range links {
		s = strings.Replace(s, "\x01"+strconv.Itoa(i)+"\x01", l, 1)
	}
	for i, c := range codes {
		s = strings.Replace(s, "\x00"+strconv.Itoa(i)+"\x00", c, 1)
	}
	return s
}

func mdID(s string) string {
	return strings.Trim(mdSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// renderMarkdown turns a handbook body into HTML and its table of contents.
func renderMarkdown(src string) (template.HTML, []mdHeading) {
	var b strings.Builder
	var toc []mdHeading
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	var para []string
	list := ""
	flushPara := func() {
		if len(para) > 0 {
			b.WriteString("<p>" + mdInline(strings.Join(para, " ")) + "</p>\n")
			para = nil
		}
	}
	closeList := func() {
		if list != "" {
			b.WriteString("</" + list + ">\n")
			list = ""
		}
	}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "```"):
			flushPara()
			closeList()
			lang := mdID(strings.TrimPrefix(t, "```"))
			var code []string
			for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```"); i++ {
				code = append(code, lines[i])
			}
			cls := ""
			if lang != "" {
				cls = ` class="lang-` + lang + `"`
			}
			b.WriteString("<pre><code" + cls + ">" + html.EscapeString(strings.Join(code, "\n")) + "</code></pre>\n")
		case t == "":
			flushPara()
			closeList()
		case strings.HasPrefix(t, "#"):
			flushPara()
			closeList()
			level := len(t) - len(strings.TrimLeft(t, "#"))
			text := strings.TrimSpace(t[level:])
			if level < 1 || level > 3 || text == "" {
				para = append(para, t)
				continue
			}
			if level == 1 {
				continue // the page title comes from the file, not the body
			}
			id := mdID(text)
			toc = append(toc, mdHeading{level, text, id})
			tag := "h" + string(rune('0'+level+1)) // ## is h3, ### is h4 under the page's h2
			b.WriteString("<" + tag + ` id="` + id + `">` + mdInline(text) + "</" + tag + ">\n")
		case strings.HasPrefix(t, ">"):
			flushPara()
			closeList()
			body := strings.TrimSpace(strings.TrimPrefix(t, ">"))
			kind := "note"
			for _, k := range []string{"Tip", "Warning", "Note"} {
				if strings.HasPrefix(body, "**"+k+":**") {
					kind = strings.ToLower(k)
				}
			}
			b.WriteString(`<aside class="md-callout md-` + kind + `">` + mdInline(body) + "</aside>\n")
		case strings.HasPrefix(t, "|"):
			flushPara()
			closeList()
			var rows [][]string
			for ; i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "|"); i++ {
				r := strings.Trim(strings.TrimSpace(lines[i]), "|")
				if strings.Trim(r, "-:| ") == "" {
					continue // the separator row
				}
				var cells []string
				for _, c := range strings.Split(r, "|") {
					cells = append(cells, strings.TrimSpace(c))
				}
				rows = append(rows, cells)
			}
			i--
			b.WriteString(`<div class="md-table"><table>`)
			for n, r := range rows {
				tag := "td"
				if n == 0 {
					tag = "th"
					b.WriteString("<thead>")
				}
				b.WriteString("<tr>")
				for _, c := range r {
					b.WriteString("<" + tag + ">" + mdInline(c) + "</" + tag + ">")
				}
				b.WriteString("</tr>")
				if n == 0 {
					b.WriteString("</thead><tbody>")
				}
			}
			b.WriteString("</tbody></table></div>\n")
		case strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ") || mdOL.MatchString(t):
			flushPara()
			want, item := "ul", strings.TrimSpace(t[2:])
			if mdOL.MatchString(t) {
				want, item = "ol", mdOL.ReplaceAllString(t, "")
			}
			if list != want {
				closeList()
				b.WriteString("<" + want + ">\n")
				list = want
			}
			b.WriteString("<li>" + mdInline(item) + "</li>\n")
		default:
			if list != "" && strings.HasPrefix(line, "  ") {
				// A wrapped list item continues the last one; reopen it.
				s := b.String()
				if k := strings.LastIndex(s, "</li>"); k >= 0 {
					b.Reset()
					b.WriteString(s[:k] + " " + mdInline(t) + s[k:])
				}
				continue
			}
			closeList()
			para = append(para, t)
		}
	}
	flushPara()
	closeList()
	return template.HTML(b.String()), toc
}

// splitSection cuts a "## Name" section out of a Markdown document and
// returns the rest and the section's lines.
func splitSection(src, name string) (rest string, section []string) {
	lines := strings.Split(src, "\n")
	var out []string
	in := false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "## ") {
			in = strings.EqualFold(strings.TrimSpace(t[3:]), name)
			if in {
				continue
			}
		}
		if in {
			section = append(section, l)
		} else {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n"), section
}

// parseRefs reads "- [Title](url) - note" lines.
func parseRefs(lines []string) []mdRef {
	var out []mdRef
	for _, l := range lines {
		if m := mdRefRow.FindStringSubmatch(strings.TrimSpace(l)); m != nil && safeURL(m[2]) != "" {
			out = append(out, mdRef{Title: m[1], URL: m[2], Note: strings.TrimSpace(m[3])})
		}
	}
	return out
}

// mdTitle returns the "# Title" line and the first paragraph after it.
func mdTitle(src string) (title, summary string) {
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "# ") {
			title = strings.TrimSpace(t[2:])
			var p []string
			for _, n := range lines[i+1:] {
				n = strings.TrimSpace(n)
				if n == "" && len(p) > 0 {
					break
				}
				if strings.HasPrefix(n, "#") {
					break
				}
				if n != "" {
					p = append(p, n)
				}
			}
			return title, strings.Join(p, " ")
		}
	}
	return "", ""
}
