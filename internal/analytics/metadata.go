package analytics

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/url"
	"strings"
)

func VisitorHash(secret, ip string) string {
	normalized := strings.TrimSpace(ip)
	if parsed := net.ParseIP(normalized); parsed != nil {
		normalized = parsed.String()
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(normalized))
	return hex.EncodeToString(mac.Sum(nil))
}

func Metadata(userAgent, referer string) (referrerHost, device, browser string) {
	return referrerDomain(referer), classifyDevice(userAgent), classifyBrowser(userAgent)
}

func referrerDomain(referer string) string {
	if strings.TrimSpace(referer) == "" {
		return ""
	}
	u, err := url.Parse(referer)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func classifyDevice(userAgent string) string {
	ua := strings.ToLower(userAgent)
	switch {
	case containsAny(ua, "bot", "crawler", "spider", "slurp"):
		return "bot"
	case containsAny(ua, "ipad", "tablet"):
		return "tablet"
	case containsAny(ua, "mobile", "iphone", "android"):
		return "mobile"
	default:
		return "desktop"
	}
}

func classifyBrowser(userAgent string) string {
	ua := strings.ToLower(userAgent)
	switch {
	case strings.Contains(ua, "edg/"):
		return "Edge"
	case strings.Contains(ua, "chrome/") || strings.Contains(ua, "crios/"):
		return "Chrome"
	case strings.Contains(ua, "firefox/") || strings.Contains(ua, "fxios/"):
		return "Firefox"
	case strings.Contains(ua, "safari/"):
		return "Safari"
	default:
		return "Other"
	}
}

func containsAny(value string, fragments ...string) bool {
	for _, fragment := range fragments {
		if strings.Contains(value, fragment) {
			return true
		}
	}
	return false
}
