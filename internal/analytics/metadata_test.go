package analytics

import (
	"strings"
	"testing"
)

func TestVisitorHashIsStableAndDoesNotExposeIP(t *testing.T) {
	first := VisitorHash("visitor-secret", "192.0.2.10")
	second := VisitorHash("visitor-secret", "192.0.2.10")
	other := VisitorHash("visitor-secret", "192.0.2.11")
	if first != second || first == other {
		t.Fatal("visitor hash is not stable and distinct")
	}
	if len(first) != 64 || strings.Contains(first, "192.0.2.10") {
		t.Fatalf("unexpected visitor hash %q", first)
	}
}

func TestMetadataClassification(t *testing.T) {
	tests := []struct {
		name        string
		userAgent   string
		referer     string
		wantRef     string
		wantDevice  string
		wantBrowser string
	}{
		{
			name:        "desktop chrome",
			userAgent:   "Mozilla/5.0 Chrome/131.0 Safari/537.36",
			referer:     "https://News.Example.com:8443/article",
			wantRef:     "news.example.com",
			wantDevice:  "desktop",
			wantBrowser: "Chrome",
		},
		{
			name:        "mobile firefox",
			userAgent:   "Mozilla/5.0 (iPhone; Mobile) Firefox/130.0",
			wantDevice:  "mobile",
			wantBrowser: "Firefox",
		},
		{
			name:        "crawler",
			userAgent:   "ExampleBot/1.0 crawler",
			wantDevice:  "bot",
			wantBrowser: "Other",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			referrer, device, browser := Metadata(test.userAgent, test.referer)
			if referrer != test.wantRef || device != test.wantDevice || browser != test.wantBrowser {
				t.Fatalf("Metadata() = %q, %q, %q", referrer, device, browser)
			}
		})
	}
}
