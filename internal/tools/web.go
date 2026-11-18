package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const maxPageBytes = 5 << 20

var fetchTool = Tool{
	Name: "web_fetch",
	Description: "Read a web page (documentation, an issue, an API's reference) as text. The human allows each site once per conversation, " +
		"so fetch only what the task needs.",
	Schema:   schema(map[string]any{"url": str("the page's full address, http or https")}, "url"),
	ReadOnly: true,
	run: func(ctx context.Context, b *Box, input json.RawMessage) (string, error) {
		var in struct {
			URL string `json:"url"`
		}
		if err := decode(input, &in); err != nil {
			return "", err
		}
		u, err := url.Parse(strings.TrimSpace(in.URL))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "", errors.New("give a full http or https address")
		}
		if err := b.allowSite(ctx, u); err != nil {
			return "", err
		}
		return fetchPage(ctx, b, u)
	},
}

// HTTP is the client web_fetch uses; tests swap it.
var HTTP = &http.Client{Timeout: 30 * time.Second}

// allowSite asks you once per site, per conversation.
func (b *Box) allowSite(ctx context.Context, u *url.URL) error {
	host := strings.ToLower(u.Hostname())
	b.mu.Lock()
	if b.Domains == nil {
		b.Domains = &sync.Map{}
	}
	sites := b.Domains
	b.mu.Unlock()
	if _, ok := sites.Load(host); ok {
		return nil
	}
	if b.Ask == nil {
		return errors.New("nobody can allow web sites here, so none are read")
	}
	ok, reason, err := b.Ask(ctx, "web_fetch("+host+")", "read pages from "+host+" in this conversation\nfirst: "+u.String())
	if err != nil {
		return fmt.Errorf("could not ask for approval: %v", err)
	}
	if !ok {
		if reason == "" {
			reason = "denied by the human reviewer"
		}
		return errors.New(reason + ": don't read " + host + " another way")
	}
	sites.Store(host, true)
	return nil
}

func fetchPage(ctx context.Context, b *Box, u *url.URL) (string, error) {
	client := *HTTP
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if _, ok := b.Domains.Load(strings.ToLower(req.URL.Hostname())); !ok {
			return fmt.Errorf("the page moved to another site, %s: fetch that address to ask for it", req.URL)
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "tubas (terminal agent; web_fetch)")
	req.Header.Set("Accept", "text/html,text/plain,application/json,text/markdown;q=0.9,*/*;q=0.5")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("%s answered %s", u.Host, resp.Status)
	}
	kind := resp.Header.Get("Content-Type")
	text := string(body)
	switch {
	case strings.Contains(kind, "html"):
		text = htmlText(text)
	case strings.Contains(kind, "json"), strings.HasPrefix(kind, "text/"), kind == "":
	default:
		return "", fmt.Errorf("%s is %s, not text", u, kind)
	}
	return fmt.Sprintf("%s (%s)\n\n%s", u, resp.Status, strings.TrimSpace(text)), nil
}

var (
	dropBlocks = regexp.MustCompile(`(?is)<(script|style|noscript|svg|head|nav|footer)\b.*?</(script|style|noscript|svg|head|nav|footer)>`)
	breaks     = regexp.MustCompile(`(?i)<(br|/p|/div|/h[1-6]|/li|/tr|/pre|/section|/article|hr)\b[^>]*>`)
	headings   = regexp.MustCompile(`(?i)<h([1-6])\b[^>]*>`)
	items      = regexp.MustCompile(`(?i)<li\b[^>]*>`)
	tags       = regexp.MustCompile(`(?s)<[^>]*>`)
	spaces     = regexp.MustCompile(`[ \t\f\v]+`)
	blankRuns  = regexp.MustCompile(`\n\s*\n\s*\n+`)
)

// htmlText keeps a page's words and its shape (headings, lists,
// paragraphs) and drops the rest.
func htmlText(page string) string {
	page = dropBlocks.ReplaceAllString(page, "")
	page = headings.ReplaceAllStringFunc(page, func(h string) string {
		return "\n\n" + strings.Repeat("#", int(h[2]-'0')) + " "
	})
	page = items.ReplaceAllString(page, "\n- ")
	page = breaks.ReplaceAllString(page, "\n")
	page = tags.ReplaceAllString(page, "")
	page = html.UnescapeString(page)
	page = spaces.ReplaceAllString(page, " ")
	lines := strings.Split(page, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return blankRuns.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
}
