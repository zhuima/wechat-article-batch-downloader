package officialaccountdownload

import (
	"strings"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// MarkdownTable pairs a GFM table with the text produced by the previous
// converter. It lets callers upgrade saved Markdown from its stored HTML
// without downloading the article again or rewriting unrelated paragraphs.
type MarkdownTable struct {
	Markdown          string
	FlattenedMarkdown string
}

// ExtractMarkdownTablesFromHTML reads tables in document order. Both fields
// are derived from the supplied HTML; no network access is performed.
func ExtractMarkdownTablesFromHTML(content string) ([]MarkdownTable, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(content))
	if err != nil {
		return nil, err
	}
	var tables []MarkdownTable
	var conversionErr error
	doc.Find("table").EachWithBreak(func(_ int, table *goquery.Selection) bool {
		if table.ParentsFiltered("table").Length() != 0 {
			return true
		}
		raw, err := goquery.OuterHtml(table)
		if err != nil {
			conversionErr = err
			return false
		}
		legacy, err := legacyMarkdownForTable(raw)
		if err != nil {
			conversionErr = err
			return false
		}
		isolated, err := goquery.NewDocumentFromReader(strings.NewReader(raw))
		if err != nil {
			conversionErr = err
			return false
		}
		normalizeMarkdownTables(isolated)
		processed, err := isolated.Html()
		if err != nil {
			conversionErr = err
			return false
		}
		markdown, err := md_convert.ConvertString(processed)
		if err != nil {
			conversionErr = err
			return false
		}
		tables = append(tables, MarkdownTable{
			Markdown:          strings.TrimSpace(markdown),
			FlattenedMarkdown: strings.TrimSpace(legacy),
		})
		return true
	})
	return tables, conversionErr
}

func legacyMarkdownForTable(raw string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(raw))
	if err != nil {
		return "", err
	}
	// This reproduces the former exporter, including its treatment of <br>.
	for _, node := range doc.Nodes {
		preserveLegacyTextNewlines(node)
	}
	oldHTML, err := doc.Html()
	if err != nil {
		return "", err
	}
	oldHTML = strings.ReplaceAll(oldHTML, "WECHATNEWLINEHOLDER", "WECHATBRHOLDER")
	oldHTML = strings.ReplaceAll(oldHTML, "<br/>", "WECHATBRHOLDER")
	oldHTML = strings.ReplaceAll(oldHTML, "<br>", "WECHATBRHOLDER")
	oldHTML = strings.ReplaceAll(oldHTML, "<br />", "WECHATBRHOLDER")
	markdown, err := htmltomarkdown.NewConverter("", true, nil).ConvertString(oldHTML)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(markdown, "WECHATBRHOLDER", "  \n"), nil
}

func preserveLegacyTextNewlines(node *html.Node) {
	if node.Type == html.TextNode {
		node.Data = strings.ReplaceAll(node.Data, "\n", "WECHATNEWLINEHOLDER")
		return
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && (child.Data == "pre" || child.Data == "code" || child.Data == "script" || child.Data == "style") {
			continue
		}
		preserveLegacyTextNewlines(child)
	}
}

// Table cells in WeChat articles often contain nested section/span elements.
// Flattening those elements first keeps each Markdown table row on one line.
// A visible break between text runs becomes a plain-text separator because the
// local reader deliberately strips raw HTML, including <br>.
func normalizeMarkdownTables(doc *goquery.Document) {
	doc.Find("table th, table td").Each(func(_ int, cell *goquery.Selection) {
		if len(cell.Nodes) == 0 {
			return
		}
		normalizeTableCellText(cell.Nodes[0])
		flattenTableCellBlocks(cell.Nodes[0])
		flattenTableCellBreaks(cell.Nodes[0])
	})
}

func normalizeTableCellText(node *html.Node) {
	if node.Type == html.TextNode {
		// HTML collapses source indentation. Keeping those newlines as hard
		// breaks would split a GFM row across several physical lines.
		node.Data = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ", "\t", " ").Replace(node.Data)
		return
	}
	if node.Type == html.ElementNode && (node.Data == "pre" || node.Data == "code" || node.Data == "script" || node.Data == "style") {
		return
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && child.Data == "table" {
			continue
		}
		normalizeTableCellText(child)
	}
}

func flattenTableCellBlocks(parent *html.Node) {
	for child := parent.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != html.ElementNode || child.Data == "table" {
			continue
		}
		flattenTableCellBlocks(child)
	}
	for child := parent.FirstChild; child != nil; child = child.NextSibling {
		if !isTableCellBlock(child) || !tableNodeHasContent(child) {
			continue
		}
		next := child.NextSibling
		for next != nil && next.Type == html.TextNode && strings.TrimSpace(next.Data) == "" {
			next = next.NextSibling
		}
		if isTableCellBlock(next) && tableNodeHasContent(next) {
			parent.InsertBefore(&html.Node{Type: html.TextNode, Data: " / "}, next)
		}
	}
	for child := parent.FirstChild; child != nil; child = child.NextSibling {
		if isTableCellBlock(child) {
			child.Data = "span"
			child.DataAtom = atom.Span
		}
	}
}

func isTableCellBlock(node *html.Node) bool {
	return node != nil && node.Type == html.ElementNode && (node.Data == "section" || node.Data == "p" || node.Data == "div")
}

func tableNodeHasContent(node *html.Node) bool {
	if node.Type == html.TextNode {
		return strings.TrimSpace(node.Data) != ""
	}
	if node.Type == html.ElementNode && node.Data == "img" {
		return true
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if tableNodeHasContent(child) {
			return true
		}
	}
	return false
}

type tableCellLeaf struct {
	node    *html.Node
	isBreak bool
	content bool
}

func flattenTableCellBreaks(cell *html.Node) {
	var leaves []tableCellLeaf
	var collect func(*html.Node)
	collect = func(node *html.Node) {
		if node.Type == html.TextNode {
			leaves = append(leaves, tableCellLeaf{node: node, content: strings.TrimSpace(node.Data) != ""})
			return
		}
		if node.Type == html.ElementNode {
			if node.Data == "br" {
				leaves = append(leaves, tableCellLeaf{node: node, isBreak: true})
				return
			}
			if node.Data == "img" {
				leaves = append(leaves, tableCellLeaf{node: node, content: true})
				return
			}
			if node.Data == "table" {
				return
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			collect(child)
		}
	}
	collect(cell)
	var pending []*html.Node
	seenContent := false
	for _, leaf := range leaves {
		if leaf.isBreak {
			pending = append(pending, leaf.node)
			continue
		}
		if !leaf.content {
			continue
		}
		if len(pending) != 0 {
			if seenContent {
				replaceTableBreak(pending[0], " / ")
				pending = pending[1:]
			}
			for _, node := range pending {
				replaceTableBreak(node, "")
			}
			pending = nil
		}
		seenContent = true
	}
	for _, node := range pending {
		replaceTableBreak(node, "")
	}
}

func replaceTableBreak(node *html.Node, replacement string) {
	if replacement != "" {
		node.Parent.InsertBefore(&html.Node{Type: html.TextNode, Data: replacement}, node)
	}
	node.Parent.RemoveChild(node)
}
