package torznab

import (
	"context"
	"encoding/xml"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func TestRenderCaps(t *testing.T) {
	t.Parallel()
	document, err := RenderCaps()
	if err != nil {
		t.Fatal(err)
	}
	text := string(document)
	for _, expected := range []string{
		xml.Header,
		`<server version="1.3" title="AniLiberty Torznab"></server>`,
		`<limits max="50" default="50"></limits>`,
		`<search available="yes" supportedParams="q"></search>`,
		`<tv-search available="yes" supportedParams="q,season,ep"></tv-search>`,
		`<category id="5000" name="TV">`,
		`<subcat id="5070" name="Anime"></subcat>`,
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("caps missing %q:\n%s", expected, text)
		}
	}
	for _, unsupported := range []string{"movie-search", "audio-search", "book-search", "imdbid", "tvdbid"} {
		if strings.Contains(text, unsupported) {
			t.Errorf("caps advertises unsupported capability %q", unsupported)
		}
	}
	if err := xml.Unmarshal(document, new(any)); err != nil {
		t.Fatalf("caps is not XML: %v", err)
	}
}

func TestRenderRSSExactMapping(t *testing.T) {
	t.Parallel()
	year := 2026
	document, err := RenderRSS(Feed{
		SiteBaseURL: "https://aniliberty.top/",
		Offset:      7,
		Total:       12,
		Items: []Item{{
			Title:          "Название / A & B S02E03 RUS",
			InfoHash:       "ABCDEF0123456789ABCDEF0123456789ABCDEF01",
			MagnetURI:      "magnet:?xt=urn:btih:ABC&dn=A+B",
			ReleaseAlias:   "alias/слэш",
			UpdatedAt:      time.Date(2026, 7, 16, 22, 33, 58, 0, time.FixedZone("MSK", 3*60*60)),
			Category:       CategoryAnime,
			SizeBytes:      123456789,
			Seeders:        7,
			Leechers:       5,
			CompletedTimes: 42,
			Year:           &year,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(document)
	for _, expected := range []string{
		xml.Header,
		`<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/" xmlns:torznab="http://torznab.com/schemas/2015/feed">`,
		`<newznab:response offset="7" total="12"></newznab:response>`,
		`<title>Название / A &amp; B S02E03 RUS</title>`,
		`<guid isPermaLink="false">urn:btih:abcdef0123456789abcdef0123456789abcdef01</guid>`,
		`<link>magnet:?xt=urn:btih:ABC&amp;dn=A+B</link>`,
		`<comments>https://aniliberty.top/anime/releases/release/alias%2F%D1%81%D0%BB%D1%8D%D1%88</comments>`,
		`<pubDate>Thu, 16 Jul 2026 19:33:58 +0000</pubDate>`,
		`<category>TV &gt; Anime</category>`,
		`<enclosure url="magnet:?xt=urn:btih:ABC&amp;dn=A+B" length="123456789" type="application/x-bittorrent"></enclosure>`,
		`<torznab:attr name="category" value="5070"></torznab:attr>`,
		`<torznab:attr name="size" value="123456789"></torznab:attr>`,
		`<torznab:attr name="seeders" value="7"></torznab:attr>`,
		`<torznab:attr name="leechers" value="5"></torznab:attr>`,
		`<torznab:attr name="peers" value="12"></torznab:attr>`,
		`<torznab:attr name="grabs" value="42"></torznab:attr>`,
		`<torznab:attr name="infohash" value="abcdef0123456789abcdef0123456789abcdef01"></torznab:attr>`,
		`<torznab:attr name="magneturl" value="magnet:?xt=urn:btih:ABC&amp;dn=A+B"></torznab:attr>`,
		`<torznab:attr name="downloadvolumefactor" value="0"></torznab:attr>`,
		`<torznab:attr name="uploadvolumefactor" value="1"></torznab:attr>`,
		`<torznab:attr name="year" value="2026"></torznab:attr>`,
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("RSS missing %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, " UTC") {
		t.Error("pubDate used UTC abbreviation")
	}
	if err := xml.Unmarshal(document, new(any)); err != nil {
		t.Fatalf("RSS is not XML: %v", err)
	}
}

func TestRenderRSSPreservesEncodedSlashInSitePrefix(t *testing.T) {
	t.Parallel()
	document, err := RenderRSS(Feed{
		SiteBaseURL: "https://example.test/proxy%2Ftenant/",
		Total:       1,
		Items: []Item{{
			Title:          "valid",
			InfoHash:       "abcdef0123456789abcdef0123456789abcdef01",
			MagnetURI:      "magnet:?xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01",
			ReleaseAlias:   "release",
			UpdatedAt:      time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC),
			Category:       CategoryAnime,
			SizeBytes:      1,
			Seeders:        1,
			Leechers:       1,
			CompletedTimes: 1,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(document)
	for _, expected := range []string{
		"<link>https://example.test/proxy%2Ftenant/</link>",
		"<comments>https://example.test/proxy%2Ftenant/anime/releases/release/release</comments>",
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("RSS missing %q:\n%s", expected, text)
		}
	}
}

func TestRenderRSSFiltersXMLUnsafeAndOverflowItems(t *testing.T) {
	t.Parallel()
	valid := Item{
		Title:          "valid",
		InfoHash:       "abcdef0123456789abcdef0123456789abcdef01",
		MagnetURI:      "magnet:?xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01",
		ReleaseAlias:   "valid",
		UpdatedAt:      time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC),
		Category:       CategoryTV,
		SizeBytes:      1,
		Seeders:        1,
		Leechers:       2,
		CompletedTimes: 3,
	}
	unsafe := valid
	unsafe.Title = "bad\x01title"
	overflow := valid
	overflow.ReleaseAlias = "overflow"
	overflow.Seeders = math.MaxInt64
	overflow.Leechers = 1

	filtered := FilterValidItems([]Item{valid, unsafe, overflow}, "https://example.test/")
	if len(filtered) != 1 || filtered[0].Title != "valid" {
		t.Fatalf("FilterValidItems() = %#v", filtered)
	}
	document, err := RenderRSS(Feed{SiteBaseURL: "https://example.test/", Total: 1, Items: []Item{valid, unsafe, overflow}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(document), "<item>") != 1 || strings.Contains(string(document), "overflow") {
		t.Fatalf("invalid item was rendered:\n%s", document)
	}

	for _, value := range []string{"plain", "tab\tline\n", "emoji 😀", "replacement �"} {
		if !ValidXML10String(value) {
			t.Errorf("ValidXML10String(%q) = false", value)
		}
	}
	for _, value := range []string{"nul\x00", "control\x1f", string([]byte{0xff})} {
		if ValidXML10String(value) {
			t.Errorf("ValidXML10String(%q) = true", value)
		}
	}
}

func TestRenderRSSContextRejectsExpiredRequest(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := RenderRSSContext(ctx, Feed{SiteBaseURL: "https://example.test/"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("RenderRSSContext() error = %v, want context.Canceled", err)
	}
}

func TestRenderErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		code        ErrorCode
		parameter   Parameter
		description string
	}{
		{ErrorIncorrectCredentials, "", "Incorrect user credentials"},
		{ErrorMissingParameter, ParameterType, "Missing parameter: t"},
		{ErrorIncorrectParameter, ParameterLimit, "Incorrect parameter: limit"},
		{ErrorNoSuchFunction, "", "No such function"},
		{ErrorFunctionUnavailable, "", "Function not available"},
		{ErrorUpstreamFailed, "", "Upstream request failed"},
	}
	for _, test := range tests {
		document, err := RenderError(test.code, test.parameter)
		if err != nil {
			t.Fatal(err)
		}
		expected := `<error code="` + decimalString(int64(test.code)) + `" description="` + test.description + `"></error>`
		if !strings.Contains(string(document), expected) {
			t.Errorf("RenderError(%d) = %s, want %s", test.code, document, expected)
		}
	}
	if _, err := RenderError(ErrorIncorrectParameter, Parameter("attacker-input")); err == nil {
		t.Fatal("RenderError accepted a non-canonical parameter")
	}
}
