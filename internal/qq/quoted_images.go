package qq

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/supertx/qq-tavern/internal/domain"
)

var quoteAttachmentLine = regexp.MustCompile(`(?m)^[\t ]*\[附件[0-9]+\][^\r\n]*`)
var quoteImageType = regexp.MustCompile(`类型[:：]\s*图片(?:\s|$)`)
var quoteAttachmentURL = regexp.MustCompile(`(?i)URL[:：]\s*(https?://[^\s<>"\]]+)`)

// NormalizeQuotedImages also runs in the worker: older gateways already emit
// quoted content but do not extract image descriptors embedded in that content.
// Never scan the current user message or accept arbitrary model-provided URLs.
func NormalizeQuotedImages(event domain.Event) domain.Event {
	event.Quotes = append([]domain.Quote(nil), event.Quotes...)
	for i, q := range event.Quotes {
		event.Quotes[i] = normalizeQuotedImage(q)
	}
	return event
}

func normalizeQuotedImage(q domain.Quote) domain.Quote {
	q.Images = append([]string(nil), q.Images...)
	seen := make(map[string]bool)
	for _, image := range q.Images {
		seen[image] = true
	}
	// Bound parsing separately from display clipping, so long signed URLs are
	// extracted before the quoted text is shortened for model context.
	if len(q.Text) > 64<<10 {
		q.Text = q.Text[:64<<10]
	}
	q.Text = quoteAttachmentLine.ReplaceAllStringFunc(q.Text, func(line string) string {
		if !quoteImageType.MatchString(line) {
			return quoteAttachmentURL.ReplaceAllString(line, "URL:[已隐藏]")
		}
		match := quoteAttachmentURL.FindStringSubmatch(line)
		if len(match) != 2 || !IsQQMediaURL(match[1]) {
			return "[引用图片：地址缺失或不可用]"
		}
		if !seen[match[1]] && len(q.Images) < 6 {
			q.Images = append(q.Images, match[1])
			seen[match[1]] = true
		}
		return "[引用图片附件]"
	})
	q.Text = clipQuote(q.Text, 1200)
	return q
}

// Descriptor text is untrusted. Only known QQ image CDN hosts are accepted;
// the existing downloader still enforces DNS, redirect and response-size limits.
func IsQQMediaURL(raw string) bool {
	if len(raw) > 8192 || strings.Contains(raw, "…") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Fragment != "" {
		return false
	}
	if p := u.Port(); p != "" && p != "443" && p != "80" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "multimedia.nt.qq.com.cn" || h == "multimedia.nt.qq.com" || h == "qpic.cn" || strings.HasSuffix(h, ".qpic.cn")
}

func clipQuote(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
