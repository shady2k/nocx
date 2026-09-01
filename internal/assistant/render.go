package assistant

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"strings"

	"github.com/shady2k/nocx/internal/apifetch"
	htmlnode "golang.org/x/net/html"
)

const renderedFeedItemLimit = 10

type feedDocument struct {
	XMLName xml.Name
	Title   string      `xml:"title"`
	Channel feedChannel `xml:"channel"`
	Entries []atomEntry `xml:"entry"`
}

type feedChannel struct {
	Title string     `xml:"title"`
	Items []feedItem `xml:"item"`
}

type feedItem struct {
	Title       string `xml:"title"`
	PubDate     string `xml:"pubDate"`
	Description string `xml:"description"`
	Link        string `xml:"link"`
}

type atomEntry struct {
	Title     string     `xml:"title"`
	Updated   string     `xml:"updated"`
	Published string     `xml:"published"`
	Summary   string     `xml:"summary"`
	Content   string     `xml:"content"`
	Links     []atomLink `xml:"link"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}

// Feed, JSON and HTML shapes are presentation, so the assistant owns these
// decisions; apifetch owns acquisition and charset decoding and must not grow
// a second shape rule.
func renderFetchedDocumentWithLossy(doc apifetch.TextDocument) (string, bool) {
	if shouldRenderFeed(doc.ContentType, doc.Text) {
		return renderFeedOrRaw(doc.Text), false
	}
	if isJSONContentType(doc.ContentType) || startsWithJSONContainer(doc.Text) {
		return renderJSONOrRaw(doc.Text), false
	}
	if isHTMLContentType(doc.ContentType) {
		return extractHTMLText(doc.Text), true
	}
	return doc.Text, false
}

func isJSONContentType(contentType string) bool {
	mediaType := strings.TrimSpace(strings.SplitN(strings.ToLower(contentType), ";", 2)[0])
	return mediaType == "application/json" || mediaType == "text/json" || strings.HasSuffix(mediaType, "+json")
}

func isHTMLContentType(contentType string) bool {
	mediaType := strings.TrimSpace(strings.SplitN(strings.ToLower(contentType), ";", 2)[0])
	return mediaType == "text/html" || mediaType == "application/xhtml+xml"
}

func startsWithJSONContainer(body string) bool {
	for i := range len(body) {
		switch body[i] {
		case ' ', '\t', '\r', '\n':
			continue
		case '{', '[':
			return true
		default:
			return false
		}
	}
	return false
}

func renderJSONOrRaw(body string) string {
	var rendered bytes.Buffer
	if err := json.Indent(&rendered, []byte(body), "", "  "); err != nil {
		return body
	}
	return strings.TrimSpace(rendered.String())
}

var htmlBlockElements = map[string]struct{}{
	"address": {}, "article": {}, "aside": {}, "blockquote": {}, "br": {},
	"dd": {}, "div": {}, "dl": {}, "dt": {}, "fieldset": {}, "figcaption": {},
	"figure": {}, "footer": {}, "form": {}, "h1": {}, "h2": {}, "h3": {},
	"h4": {}, "h5": {}, "h6": {}, "header": {}, "hr": {}, "li": {},
	"main": {}, "nav": {}, "ol": {}, "p": {}, "pre": {}, "section": {},
	"table": {}, "tbody": {}, "td": {}, "tfoot": {}, "th": {}, "thead": {},
	"tr": {}, "ul": {},
}

var htmlIgnoredElements = map[string]struct{}{
	"noscript": {}, "script": {}, "style": {},
}

type htmlTextBuilder struct {
	strings.Builder
	last byte
}

func (b *htmlTextBuilder) writeText(text string) {
	if text == "" {
		return
	}
	_, _ = b.WriteString(text)
	b.last = text[len(text)-1]
}

func (b *htmlTextBuilder) breakLine() {
	if b.Len() == 0 || b.last == '\n' {
		return
	}
	_ = b.WriteByte('\n')
	b.last = '\n'
}

func extractHTMLText(body string) string {
	root, err := htmlnode.Parse(strings.NewReader(body))
	if err != nil {
		return body
	}
	var rendered htmlTextBuilder
	var visit func(*htmlnode.Node)
	visit = func(node *htmlnode.Node) {
		if node.Type == htmlnode.CommentNode {
			return
		}
		if node.Type == htmlnode.ElementNode {
			if _, ignored := htmlIgnoredElements[node.Data]; ignored {
				return
			}
			if _, block := htmlBlockElements[node.Data]; block {
				rendered.breakLine()
			}
		}
		if node.Type == htmlnode.TextNode {
			rendered.writeText(node.Data)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
		if node.Type == htmlnode.ElementNode {
			if _, block := htmlBlockElements[node.Data]; block {
				rendered.breakLine()
			}
		}
	}
	visit(root)

	var lines []string
	for _, line := range strings.Split(rendered.String(), "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func shouldRenderFeed(contentType, body string) bool {
	lowerType := strings.ToLower(contentType)
	if strings.Contains(lowerType, "html") || strings.Contains(lowerType, "xhtml") {
		return false
	}
	if strings.Contains(lowerType, "rss") || strings.Contains(lowerType, "atom") || strings.Contains(lowerType, "feed") {
		return true
	}
	return strings.Contains(lowerType, "xml") &&
		(strings.Contains(strings.ToLower(body), "<rss") || strings.Contains(strings.ToLower(body), "<feed"))
}

func renderFeedOrRaw(body string) string {
	var feed feedDocument
	decoder := xml.NewDecoder(strings.NewReader(body))
	// apifetch already converted the bytes to UTF-8. The declaration still
	// names the historical wire encoding, so preserve the text and accept it.
	decoder.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) {
		return input, nil
	}
	if err := decoder.Decode(&feed); err != nil {
		return body
	}
	var trailing feedDocument
	if err := decoder.Decode(&trailing); err != io.EOF {
		return body
	}
	root := strings.ToLower(feed.XMLName.Local)
	var title string
	var items []renderedFeedItem
	switch root {
	case "rss":
		title = cleanFeedText(feed.Channel.Title)
		items = make([]renderedFeedItem, 0, len(feed.Channel.Items))
		for _, item := range feed.Channel.Items {
			items = append(items, renderedFeedItem{
				Title:       cleanFeedText(item.Title),
				Date:        cleanFeedText(item.PubDate),
				Description: cleanFeedText(item.Description),
				Link:        cleanFeedText(item.Link),
			})
		}
	case "feed":
		title = cleanFeedText(feed.Title)
		items = make([]renderedFeedItem, 0, len(feed.Entries))
		for _, entry := range feed.Entries {
			date := entry.Updated
			if date == "" {
				date = entry.Published
			}
			description := entry.Summary
			if description == "" {
				description = entry.Content
			}
			link := ""
			for _, candidate := range entry.Links {
				if candidate.Rel == "" || candidate.Rel == "alternate" {
					link = candidate.Href
					break
				}
			}
			items = append(items, renderedFeedItem{
				Title:       cleanFeedText(entry.Title),
				Date:        cleanFeedText(date),
				Description: cleanFeedText(description),
				Link:        cleanFeedText(link),
			})
		}
	default:
		return body
	}

	if title == "" {
		title = "Feed"
	}
	var rendered strings.Builder
	fmt.Fprintf(&rendered, "# %s\n", title)
	for i, item := range items {
		if i >= renderedFeedItemLimit {
			break
		}
		rendered.WriteString("\n## ")
		rendered.WriteString(item.Title)
		rendered.WriteByte('\n')
		if item.Date != "" {
			rendered.WriteString("Date: ")
			rendered.WriteString(item.Date)
			rendered.WriteByte('\n')
		}
		if item.Description != "" {
			rendered.WriteString("Description: ")
			rendered.WriteString(item.Description)
			rendered.WriteByte('\n')
		}
		if item.Link != "" {
			rendered.WriteString("Link: ")
			rendered.WriteString(item.Link)
			rendered.WriteByte('\n')
		}
	}
	if len(items) > renderedFeedItemLimit {
		fmt.Fprintf(&rendered, "\n_Showing %d of %d feed items; %d more omitted._\n", renderedFeedItemLimit, len(items), len(items)-renderedFeedItemLimit)
	}
	return rendered.String()
}

func cleanFeedText(value string) string {
	var cleaned strings.Builder
	inTag := false
	for _, r := range value {
		switch {
		case r == '<':
			inTag = true
		case inTag && r == '>':
			inTag = false
		case !inTag:
			cleaned.WriteRune(r)
		}
	}
	return strings.TrimSpace(html.UnescapeString(cleaned.String()))
}

type renderedFeedItem struct {
	Title       string
	Date        string
	Description string
	Link        string
}
