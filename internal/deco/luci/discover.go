package luci

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Form discovery (CLAUDE.md §4.1.7): the Deco serves its own web UI, whose
// scripts name every form the firmware supports. Discovery is read-only and
// needs no session.

const (
	maxDiscoverFiles = 400
	maxRedirects     = 5
	opWindow         = 600 // chars either side of a form URL searched for operations
	maxSourcesShown  = 3
)

var (
	refREs = []*regexp.Regexp{
		regexp.MustCompile(`(?i)<script[^>]+src\s*=\s*["']([^"']+)["']`),
		regexp.MustCompile(`["']([^"'\s<>(){}+]+\.(?:js|html?)(?:\?[^"'\s<>]*)?)["']`),
	}
	formRE      = regexp.MustCompile(`admin/([A-Za-z0-9_]+)\?form=([A-Za-z0-9_]+)`)
	looseFormRE = regexp.MustCompile(`[?&]form=([A-Za-z0-9_]+)`)
	opRE        = regexp.MustCompile(`operation["']?\s*[:=]\s*["']([A-Za-z_]+)["']`)
)

// FormRef is one form found in the device web UI.
type FormRef struct {
	Area       string // "?" when the URL did not name an admin area
	Form       string
	Operations []string // heuristic: operation names found near the URL
	Sources    []string // up to maxSourcesShown file paths
}

// FormsReport is the result of DiscoverForms.
type FormsReport struct {
	Forms   []FormRef
	Fetched int
	Failed  int
}

// DiscoverForms crawls the device web UI from "/" and lists the forms it references.
func (c *Client) DiscoverForms(ctx context.Context) (*FormsReport, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	queue := []string{"/"}
	seen := map[string]bool{"/": true}
	agg := map[string]*FormRef{}
	rep := &FormsReport{}
	for len(queue) > 0 && rep.Fetched < maxDiscoverFiles {
		p := queue[0]
		queue = queue[1:]
		body, final, err := c.fetchPage(ctx, p)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			rep.Failed++
			continue
		}
		rep.Fetched++
		text := string(body)
		for _, ref := range extractRefs(text) {
			next, ok := c.resolveSameHost(final, ref)
			if !ok {
				continue
			}
			key, _, _ := strings.Cut(next, "?")
			if !seen[key] {
				seen[key] = true
				queue = append(queue, next)
			}
		}
		src, _, _ := strings.Cut(final, "?")
		for _, h := range extractForms(text) {
			k := h.Area + "/" + h.Form
			ref := agg[k]
			if ref == nil {
				ref = &FormRef{Area: h.Area, Form: h.Form}
				agg[k] = ref
			}
			ref.Operations = mergeSorted(ref.Operations, h.Operations)
			if len(ref.Sources) < maxSourcesShown && !contains(ref.Sources, src) {
				ref.Sources = append(ref.Sources, src)
			}
		}
	}
	if rep.Fetched == 0 {
		return nil, errors.New("luci: could not fetch the device web UI")
	}
	for _, r := range agg {
		rep.Forms = append(rep.Forms, *r)
	}
	sort.Slice(rep.Forms, func(i, j int) bool {
		if rep.Forms[i].Area != rep.Forms[j].Area {
			return rep.Forms[i].Area < rep.Forms[j].Area
		}
		return rep.Forms[i].Form < rep.Forms[j].Form
	})
	return rep, nil
}

// fetchPage GETs path, following same-host redirects. It returns the body and
// the final request URI. Caller holds c.mu.
func (c *Client) fetchPage(ctx context.Context, path string) ([]byte, string, error) {
	for i := 0; i < maxRedirects; i++ {
		resp, err := c.do(ctx, http.MethodGet, path, "", "", "", maxPageResponseBytes)
		if err != nil {
			return nil, "", err
		}
		if resp.status >= 300 && resp.status < 400 {
			loc := resp.header.Get("Location")
			if u, err := url.Parse(loc); err == nil && u.Scheme == "https" && c.base.Scheme == "http" {
				if err := c.followHTTPS(loc); err != nil {
					return nil, "", err
				}
			}
			next, ok := c.resolveSameHost(path, loc)
			if !ok {
				return nil, "", fmt.Errorf("luci: off-host redirect from %s", path)
			}
			path = next
			continue
		}
		if resp.status != http.StatusOK {
			return nil, "", fmt.Errorf("luci: GET %s returned HTTP %d", path, resp.status)
		}
		return resp.body, path, nil
	}
	return nil, "", errors.New("luci: too many redirects")
}

// resolveSameHost resolves ref against the current request URI and returns
// the new request URI when it stays on the device's host.
func (c *Client) resolveSameHost(cur, ref string) (string, bool) {
	u, err := url.Parse(ref)
	if err != nil {
		return "", false
	}
	if u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}
	if u.Host != "" && !strings.EqualFold(u.Hostname(), c.base.Hostname()) {
		return "", false
	}
	cu, err := url.Parse(cur)
	if err != nil {
		return "", false
	}
	r := cu.ResolveReference(&url.URL{Path: u.Path, RawQuery: u.RawQuery})
	if !strings.HasPrefix(r.Path, "/") {
		return "", false
	}
	return r.RequestURI(), true
}

func extractRefs(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, re := range refREs {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				out = append(out, m[1])
			}
		}
	}
	return out
}

type formHit struct {
	Area, Form string
	Operations []string
}

func extractForms(text string) []formHit {
	var hits []formHit
	var spans [][]int
	for _, m := range formRE.FindAllStringSubmatchIndex(text, -1) {
		spans = append(spans, m[:2])
		hits = append(hits, formHit{
			Area:       text[m[2]:m[3]],
			Form:       text[m[4]:m[5]],
			Operations: opsNear(text, m[0], m[1]),
		})
	}
	for _, m := range looseFormRE.FindAllStringSubmatchIndex(text, -1) {
		if inSpans(m[0], spans) {
			continue
		}
		hits = append(hits, formHit{Area: "?", Form: text[m[2]:m[3]], Operations: opsNear(text, m[0], m[1])})
	}
	return hits
}

func opsNear(text string, start, end int) []string {
	lo, hi := max(0, start-opWindow), min(len(text), end+opWindow)
	var ops []string
	for _, m := range opRE.FindAllStringSubmatch(text[lo:hi], -1) {
		ops = mergeSorted(ops, []string{m[1]})
	}
	return ops
}

func inSpans(i int, spans [][]int) bool {
	for _, s := range spans {
		if i >= s[0] && i < s[1] {
			return true
		}
	}
	return false
}

func mergeSorted(a, b []string) []string {
	for _, s := range b {
		if !contains(a, s) {
			a = append(a, s)
		}
	}
	sort.Strings(a)
	return a
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Markdown renders the report as docs/device-forms-<model>-<fw>.md. verified
// maps "area/form" to "ok" or "failed" for the forms the probe called. The
// output names forms only: no addresses, MACs or names.
func (r *FormsReport) Markdown(model, fw string, verified map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Deco forms: %s, firmware %s\n\n", model, fw)
	fmt.Fprintf(&b, "Generated by `atlantis probe --discover-forms` on %s.\n", time.Now().Format("2006-01-02"))
	fmt.Fprintf(&b, "Crawled %d web UI files (%d failed to load).\n\n", r.Fetched, r.Failed)
	if len(verified) > 0 {
		b.WriteString("## Called by the probe\n\n| Area/form | Result |\n|---|---|\n")
		keys := make([]string, 0, len(verified))
		for k := range verified {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "| `%s` | %s |\n", k, verified[k])
		}
		b.WriteString("\n")
	}
	b.WriteString("## Referenced by the web UI\n\n")
	b.WriteString("Operations are a heuristic: names found near the form URL in the source. ")
	b.WriteString("Verify each one on the device before building on it, and never probe writes to WAN, LAN, DHCP or operation mode.\n\n")
	b.WriteString("| Area | Form | Operations (heuristic) | Seen in |\n|---|---|---|---|\n")
	for _, f := range r.Forms {
		fmt.Fprintf(&b, "| `%s` | `%s` | %s | %s |\n", f.Area, f.Form, strings.Join(f.Operations, ", "), strings.Join(f.Sources, "<br>"))
	}
	return b.String()
}
