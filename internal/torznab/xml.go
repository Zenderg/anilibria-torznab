package torznab

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	feedTitle        = "AniLiberty Torznab"
	feedDescription  = "AniLiberty torrent results"
	newznabNamespace = "http://www.newznab.com/DTD/2010/feeds/attributes/"
	torznabNamespace = "http://torznab.com/schemas/2015/feed"
)

// Item contains the validated domain values needed to render one RSS item.
type Item struct {
	Title          string
	InfoHash       string
	MagnetURI      string
	ReleaseAlias   string
	UpdatedAt      time.Time
	Category       Category
	SizeBytes      int64
	Seeders        int64
	Leechers       int64
	CompletedTimes int64
	Year           *int
}

// Feed contains already filtered and paged RSS results. Total is the result
// count before paging; Offset is the validated requested offset.
type Feed struct {
	SiteBaseURL string
	Offset      uint64
	Total       int
	Items       []Item
}

// ErrorCode is a Newznab/Torznab protocol error code supported by v1.
type ErrorCode int

const (
	ErrorIncorrectCredentials ErrorCode = 100
	ErrorMissingParameter     ErrorCode = 200
	ErrorIncorrectParameter   ErrorCode = 201
	ErrorNoSuchFunction       ErrorCode = 202
	ErrorFunctionUnavailable  ErrorCode = 203
	ErrorUpstreamFailed       ErrorCode = 900
)

// RenderCaps renders the fixed first-release capabilities document.
func RenderCaps() ([]byte, error) {
	document := capsDocument{
		Server: capsServer{Version: "1.3", Title: feedTitle},
		Limits: capsLimits{Max: 50, Default: 50},
		Searching: capsSearching{
			Search:   capsSearch{Available: "yes", SupportedParams: "q"},
			TVSearch: capsSearch{Available: "yes", SupportedParams: "q,season,ep"},
		},
		Categories: capsCategories{Category: capsCategory{
			ID: CategoryTVID, Name: "TV", Subcategory: capsSubcategory{ID: CategoryAnimeID, Name: "Anime"},
		}},
	}
	return marshalDocument(document)
}

// RenderRSS renders a Torznab RSS 2.0 document. Invalid items are omitted so
// that one XML-unsafe upstream value cannot make the entire feed malformed.
func RenderRSS(feed Feed) ([]byte, error) {
	return RenderRSSContext(context.Background(), feed)
}

// RenderRSSContext renders RSS while observing the overall request context
// during item preparation and XML encoding.
func RenderRSSContext(ctx context.Context, feed Feed) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if feed.Total < 0 {
		return nil, errors.New("feed total must be non-negative")
	}
	baseURL, err := validateSiteBaseURL(feed.SiteBaseURL)
	if err != nil {
		return nil, err
	}

	document := rssDocument{
		Version:      "2.0",
		XMLNSNewznab: newznabNamespace,
		XMLNSTorznab: torznabNamespace,
		Channel: rssChannel{
			Title:       feedTitle,
			Description: feedDescription,
			Link:        baseURL,
			Response:    rssResponse{Offset: feed.Offset, Total: feed.Total},
		},
	}
	for _, item := range feed.Items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rendered, ok := prepareItem(item, baseURL)
		if ok {
			document.Channel.Items = append(document.Channel.Items, rendered)
		}
	}
	return marshalDocumentContext(ctx, document)
}

// FilterValidItems returns a copy containing only items RenderRSS can safely
// emit. Callers can use it before calculating total and paging.
func FilterValidItems(items []Item, siteBaseURL string) []Item {
	baseURL, err := validateSiteBaseURL(siteBaseURL)
	if err != nil {
		return nil
	}
	filtered := make([]Item, 0, len(items))
	for _, item := range items {
		if _, ok := prepareItem(item, baseURL); ok {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// ValidXML10String reports whether value is well-formed UTF-8 containing only
// characters permitted by XML 1.0 Fifth Edition.
func ValidXML10String(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r == '\t' || r == '\n' || r == '\r' ||
			r >= 0x20 && r <= 0xD7FF ||
			r >= 0xE000 && r <= 0xFFFD ||
			r >= 0x10000 && r <= 0x10FFFF {
			continue
		}
		return false
	}
	return true
}

// RenderError renders one of the fixed safe Torznab protocol errors.
func RenderError(code ErrorCode, parameter Parameter) ([]byte, error) {
	var description string
	switch code {
	case ErrorIncorrectCredentials:
		description = "Incorrect user credentials"
	case ErrorMissingParameter:
		if !parameter.valid() {
			return nil, errors.New("missing-parameter error requires a canonical parameter")
		}
		description = "Missing parameter: " + string(parameter)
	case ErrorIncorrectParameter:
		if !parameter.valid() {
			return nil, errors.New("incorrect-parameter error requires a canonical parameter")
		}
		description = "Incorrect parameter: " + string(parameter)
	case ErrorNoSuchFunction:
		description = "No such function"
	case ErrorFunctionUnavailable:
		description = "Function not available"
	case ErrorUpstreamFailed:
		description = "Upstream request failed"
	default:
		return nil, fmt.Errorf("unsupported protocol error code %d", code)
	}
	return marshalDocument(errorDocument{Code: int(code), Description: description})
}

func (p Parameter) valid() bool {
	switch p {
	case ParameterAPIKey, ParameterType, ParameterQuery, ParameterCategory, ParameterLimit,
		ParameterOffset, ParameterExtended, ParameterSeason, ParameterEpisode:
		return true
	default:
		return false
	}
}

func prepareItem(item Item, siteBaseURL string) (rssItem, bool) {
	if !ValidXML10String(item.Title) || strings.TrimSpace(item.Title) == "" ||
		!ValidXML10String(item.MagnetURI) || strings.TrimSpace(item.MagnetURI) == "" ||
		!ValidXML10String(item.ReleaseAlias) || strings.TrimSpace(item.ReleaseAlias) == "" {
		return rssItem{}, false
	}
	hash := strings.ToLower(item.InfoHash)
	if len(hash) != 40 || !isHex(hash) || !ValidXML10String(hash) {
		return rssItem{}, false
	}
	magnet, err := url.Parse(item.MagnetURI)
	if err != nil || !strings.EqualFold(magnet.Scheme, "magnet") {
		return rssItem{}, false
	}
	if item.UpdatedAt.IsZero() || item.SizeBytes < 0 || item.Seeders < 0 || item.Leechers < 0 || item.CompletedTimes < 0 {
		return rssItem{}, false
	}
	if item.Seeders > math.MaxInt64-item.Leechers {
		return rssItem{}, false
	}
	if item.Category != CategoryTV && item.Category != CategoryAnime {
		return rssItem{}, false
	}

	comments := strings.TrimRight(siteBaseURL, "/") + "/anime/releases/release/" + url.PathEscape(item.ReleaseAlias)
	if !ValidXML10String(comments) {
		return rssItem{}, false
	}
	attrs := []rssAttribute{
		{Name: "category", Value: strconv.Itoa(item.Category.ID)},
		{Name: "size", Value: decimalString(item.SizeBytes)},
		{Name: "seeders", Value: decimalString(item.Seeders)},
		{Name: "leechers", Value: decimalString(item.Leechers)},
		{Name: "peers", Value: decimalString(item.Seeders + item.Leechers)},
		{Name: "grabs", Value: decimalString(item.CompletedTimes)},
		{Name: "infohash", Value: hash},
		{Name: "magneturl", Value: item.MagnetURI},
		{Name: "downloadvolumefactor", Value: "0"},
		{Name: "uploadvolumefactor", Value: "1"},
	}
	if item.Year != nil {
		attrs = append(attrs, rssAttribute{Name: "year", Value: strconv.Itoa(*item.Year)})
	}

	return rssItem{
		Title:    item.Title,
		GUID:     rssGUID{Value: "urn:btih:" + hash, IsPermaLink: false},
		Link:     item.MagnetURI,
		Comments: comments,
		PubDate:  item.UpdatedAt.UTC().Format("Mon, 02 Jan 2006 15:04:05 -0700"),
		Category: item.Category.Text,
		Enclosure: rssEnclosure{
			URL: item.MagnetURI, Length: item.SizeBytes, Type: "application/x-bittorrent",
		},
		Attributes: attrs,
	}, true
}

func validateSiteBaseURL(raw string) (string, error) {
	if !ValidXML10String(raw) || strings.TrimSpace(raw) == "" {
		return "", errors.New("site base URL is required and must be XML 1.0 safe")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("site base URL must be absolute")
	}
	return raw, nil
}

func isHex(value string) bool {
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func marshalDocument(value any) ([]byte, error) {
	var output bytes.Buffer
	output.WriteString(xml.Header)
	encoder := xml.NewEncoder(&output)
	encoder.Indent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func marshalDocumentContext(ctx context.Context, value any) ([]byte, error) {
	var output bytes.Buffer
	output.WriteString(xml.Header)
	encoder := xml.NewEncoder(contextWriter{ctx: ctx, output: &output})
	encoder.Indent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

type contextWriter struct {
	ctx    context.Context
	output *bytes.Buffer
}

func (w contextWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.output.Write(data)
}

type capsDocument struct {
	XMLName    xml.Name       `xml:"caps"`
	Server     capsServer     `xml:"server"`
	Limits     capsLimits     `xml:"limits"`
	Searching  capsSearching  `xml:"searching"`
	Categories capsCategories `xml:"categories"`
}

type capsServer struct {
	Version string `xml:"version,attr"`
	Title   string `xml:"title,attr"`
}

type capsLimits struct {
	Max     int `xml:"max,attr"`
	Default int `xml:"default,attr"`
}

type capsSearching struct {
	Search   capsSearch `xml:"search"`
	TVSearch capsSearch `xml:"tv-search"`
}

type capsSearch struct {
	Available       string `xml:"available,attr"`
	SupportedParams string `xml:"supportedParams,attr"`
}

type capsCategories struct {
	Category capsCategory `xml:"category"`
}

type capsCategory struct {
	ID          int             `xml:"id,attr"`
	Name        string          `xml:"name,attr"`
	Subcategory capsSubcategory `xml:"subcat"`
}

type capsSubcategory struct {
	ID   int    `xml:"id,attr"`
	Name string `xml:"name,attr"`
}

type rssDocument struct {
	XMLName      xml.Name   `xml:"rss"`
	Version      string     `xml:"version,attr"`
	XMLNSNewznab string     `xml:"xmlns:newznab,attr"`
	XMLNSTorznab string     `xml:"xmlns:torznab,attr"`
	Channel      rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title       string      `xml:"title"`
	Description string      `xml:"description"`
	Link        string      `xml:"link"`
	Response    rssResponse `xml:"newznab:response"`
	Items       []rssItem   `xml:"item"`
}

type rssResponse struct {
	Offset uint64 `xml:"offset,attr"`
	Total  int    `xml:"total,attr"`
}

type rssItem struct {
	Title      string         `xml:"title"`
	GUID       rssGUID        `xml:"guid"`
	Link       string         `xml:"link"`
	Comments   string         `xml:"comments"`
	PubDate    string         `xml:"pubDate"`
	Category   string         `xml:"category"`
	Enclosure  rssEnclosure   `xml:"enclosure"`
	Attributes []rssAttribute `xml:"torznab:attr"`
}

type rssGUID struct {
	Value       string `xml:",chardata"`
	IsPermaLink bool   `xml:"isPermaLink,attr"`
}

type rssEnclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type rssAttribute struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type errorDocument struct {
	XMLName     xml.Name `xml:"error"`
	Code        int      `xml:"code,attr"`
	Description string   `xml:"description,attr"`
}
