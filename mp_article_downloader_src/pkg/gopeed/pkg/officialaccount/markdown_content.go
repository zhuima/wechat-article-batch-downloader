package officialaccountdownload

import (
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// ConvertArticleHTMLToMarkdown converts a WeChat article body without fetching
// any resources. Callers repairing an existing export can pass the inner HTML
// of .rich_media_content from its saved HTML file.
func ConvertArticleHTMLToMarkdown(content string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(content))
	if err != nil {
		return "", err
	}

	normalizeMarkdownTables(doc)
	normalizeWeChatCodeSnippets(doc)
	normalizeWeChatBoldText(doc)
	normalizeWeChatParagraphs(doc)
	// Saved HTML may inline images as data URIs. Those make an unreadably large
	// Markdown document and cannot be used by the local reader. The download
	// path replaces article images with relative files before reaching here.
	doc.Find("img[src^='data:']").Remove()

	for _, node := range doc.Nodes {
		preserveArticleTextNewlines(node)
	}
	processed, err := doc.Html()
	if err != nil {
		return "", err
	}
	const brPlaceholder = "WECHATBRHOLDER"
	processed = strings.ReplaceAll(processed, "WECHATNEWLINEHOLDER", brPlaceholder)
	processed = strings.NewReplacer("<br/>", brPlaceholder, "<br>", brPlaceholder, "<br />", brPlaceholder).Replace(processed)
	markdown, err := md_convert.ConvertString(processed)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(markdown, brPlaceholder, "  \n"), nil
}

func preserveArticleTextNewlines(node *html.Node) {
	if node.Type == html.TextNode {
		// Pretty-printed HTML can put a newline between block elements. That
		// whitespace is source formatting, not an article line break.
		if strings.TrimSpace(node.Data) != "" {
			node.Data = strings.ReplaceAll(node.Data, "\n", "WECHATNEWLINEHOLDER")
		}
		return
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && (child.Data == "pre" || child.Data == "code" || child.Data == "script" || child.Data == "style") {
			continue
		}
		preserveArticleTextNewlines(child)
	}
}

// WeChat's highlighted code snippets store each source line in a separate
// <code> element. A generic HTML-to-Markdown converter joins those elements
// into one line; rebuild a conventional pre/code block before conversion.
func normalizeWeChatCodeSnippets(doc *goquery.Document) {
	doc.Find("section.code-snippet__fix, div.code-snippet__fix").Each(func(_ int, snippet *goquery.Selection) {
		oldPre := snippet.Find("pre").First()
		if oldPre.Length() == 0 {
			return
		}
		var lines []string
		oldPre.ChildrenFiltered("code").Each(func(_ int, line *goquery.Selection) {
			lines = append(lines, normalizeCodeWhitespace(line.Text()))
		})
		if len(lines) == 0 || len(snippet.Nodes) == 0 || snippet.Nodes[0].Parent == nil {
			return
		}
		pre := &html.Node{Type: html.ElementNode, Data: "pre", DataAtom: atom.Pre}
		code := &html.Node{Type: html.ElementNode, Data: "code", DataAtom: atom.Code}
		if language := strings.TrimSpace(oldPre.AttrOr("data-lang", "")); language != "" {
			code.Attr = append(code.Attr, html.Attribute{Key: "class", Val: "language-" + language})
		}
		code.AppendChild(&html.Node{Type: html.TextNode, Data: strings.Join(lines, "\n")})
		pre.AppendChild(code)
		parent := snippet.Nodes[0].Parent
		parent.InsertBefore(pre, snippet.Nodes[0])
		parent.RemoveChild(snippet.Nodes[0])
	})
}

func normalizeCodeWhitespace(line string) string {
	return strings.NewReplacer("\u00a0", " ", "\u2007", " ", "\u202f", " ").Replace(line)
}

// WeChat uses CSS font-weight on spans instead of semantic <strong> tags.
func normalizeWeChatBoldText(doc *goquery.Document) {
	doc.Find("span[style]").Each(func(_ int, span *goquery.Selection) {
		if len(span.Nodes) == 0 || !hasBoldFontWeight(span.AttrOr("style", "")) {
			return
		}
		span.Nodes[0].Data = "strong"
		span.Nodes[0].DataAtom = atom.Strong
	})
}

func hasBoldFontWeight(style string) bool {
	for _, declaration := range strings.Split(style, ";") {
		parts := strings.SplitN(declaration, ":", 2)
		if len(parts) != 2 || strings.TrimSpace(strings.ToLower(parts[0])) != "font-weight" {
			continue
		}
		value := strings.TrimSpace(strings.ToLower(parts[1]))
		if value == "bold" || value == "bolder" {
			return true
		}
		if weight, err := strconv.Atoi(value); err == nil && weight >= 600 {
			return true
		}
	}
	return false
}

// The current WeChat editor writes ordinary paragraphs as leaf <section>
// elements. Converting only leaf sections avoids flattening its layout groups.
func normalizeWeChatParagraphs(doc *goquery.Document) {
	doc.Find("section").Each(func(_ int, section *goquery.Selection) {
		if len(section.Nodes) == 0 || section.ParentsFiltered("table, pre, code").Length() != 0 {
			return
		}
		if section.Find("section, p, div, ul, ol, table, pre, blockquote, h1, h2, h3, h4, h5, h6, hr").Length() != 0 {
			return
		}
		if strings.TrimSpace(section.Text()) == "" && section.Find("img, video, audio, iframe").Length() == 0 {
			return
		}
		section.Nodes[0].Data = "p"
		section.Nodes[0].DataAtom = atom.P
	})
}
