package assistant

import (
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"strings"

	"github.com/shady2k/nocx/internal/apifetch"
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

// Feed shape is presentation, so the assistant owns this decision; apifetch
// owns acquisition and charset decoding and must not grow a second shape rule.
func renderFetchedDocument(doc apifetch.TextDocument) string {
	if !shouldRenderFeed(doc.ContentType, doc.Text) {
		return doc.Text
	}
	return renderFeedOrRaw(doc.Text)
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
