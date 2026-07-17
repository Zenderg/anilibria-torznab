package anilibria

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// ReleaseID is a positive AniLiberty release identifier.
type ReleaseID int64

// ReleaseType is an AniLiberty release kind.
type ReleaseType string

const (
	ReleaseTypeTV      ReleaseType = "TV"
	ReleaseTypeONA     ReleaseType = "ONA"
	ReleaseTypeWEB     ReleaseType = "WEB"
	ReleaseTypeOVA     ReleaseType = "OVA"
	ReleaseTypeOAD     ReleaseType = "OAD"
	ReleaseTypeMovie   ReleaseType = "MOVIE"
	ReleaseTypeDorama  ReleaseType = "DORAMA"
	ReleaseTypeSpecial ReleaseType = "SPECIAL"
)

// ReleaseSummary contains the release data needed by the service layer.
type ReleaseSummary struct {
	ID       ReleaseID
	Type     ReleaseType
	Year     int
	MainName string
	Alias    string
}

// Torrent contains a validated AniLiberty torrent variant. Hash is always a
// lowercase BitTorrent v1 infohash.
type Torrent struct {
	Hash           string
	Size           int64
	Label          string
	Magnet         string
	Seeders        int64
	Leechers       int64
	CompletedTimes int64
	UpdatedAt      time.Time
	Release        ReleaseSummary
}

type rawReleaseID struct {
	ID json.Number `json:"id"`
}

type rawTorrent struct {
	Hash           string      `json:"hash"`
	Size           json.Number `json:"size"`
	Label          string      `json:"label"`
	Magnet         string      `json:"magnet"`
	Seeders        json.Number `json:"seeders"`
	Leechers       json.Number `json:"leechers"`
	CompletedTimes json.Number `json:"completed_times"`
	UpdatedAt      string      `json:"updated_at"`
	Release        rawRelease  `json:"release"`
	invalidString  string
}

type rawRelease struct {
	ID    json.Number `json:"id"`
	Type  rawType     `json:"type"`
	Year  json.Number `json:"year"`
	Name  rawName     `json:"name"`
	Alias string      `json:"alias"`
}

type rawType struct {
	Value string `json:"value"`
}

type rawName struct {
	Main string `json:"main"`
}

func (raw *rawTorrent) UnmarshalJSON(data []byte) error {
	type torrentWithoutMethods rawTorrent
	var decoded torrentWithoutMethods
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*raw = rawTorrent(decoded)
	raw.invalidString = invalidTorrentStringField(data)
	return nil
}

func validateTorrent(raw rawTorrent) (Torrent, string, error) {
	if raw.invalidString != "" {
		return Torrent{}, raw.invalidString, fmt.Errorf("invalid source string encoding")
	}
	hash, err := normalizeInfoHash(raw.Hash)
	if err != nil {
		return Torrent{}, "hash", err
	}
	size, err := nonNegativeInteger(raw.Size)
	if err != nil {
		return Torrent{}, "size", err
	}
	seeders, err := nonNegativeInteger(raw.Seeders)
	if err != nil {
		return Torrent{}, "seeders", err
	}
	leechers, err := nonNegativeInteger(raw.Leechers)
	if err != nil {
		return Torrent{}, "leechers", err
	}
	completed, err := nonNegativeInteger(raw.CompletedTimes)
	if err != nil {
		return Torrent{}, "completed_times", err
	}

	releaseID, err := positiveInteger(raw.Release.ID)
	if err != nil {
		return Torrent{}, "release.id", err
	}
	year, err := optionalNonNegativeInteger(raw.Release.Year)
	if err != nil || year > int64(^uint(0)>>1) {
		return Torrent{}, "release.year", fmt.Errorf("invalid release year")
	}
	releaseType := ReleaseType(raw.Release.Type.Value)
	if !releaseType.valid() {
		return Torrent{}, "release.type.value", fmt.Errorf("unsupported release type")
	}

	updatedAt, err := time.Parse(time.RFC3339, raw.UpdatedAt)
	if err != nil {
		return Torrent{}, "updated_at", fmt.Errorf("invalid RFC 3339 timestamp")
	}
	magnet, err := url.Parse(raw.Magnet)
	if err != nil || !strings.EqualFold(magnet.Scheme, "magnet") || !magnetContainsInfoHash(magnet, hash) {
		return Torrent{}, "magnet", fmt.Errorf("invalid magnet URI")
	}

	stringsForXML := []struct {
		name  string
		value string
	}{
		{"label", raw.Label},
		{"magnet", raw.Magnet},
		{"release.name.main", raw.Release.Name.Main},
		{"release.alias", raw.Release.Alias},
	}
	for _, field := range stringsForXML {
		if field.value == "" || !validXMLString(field.value) {
			return Torrent{}, field.name, fmt.Errorf("invalid XML text")
		}
	}

	return Torrent{
		Hash:           hash,
		Size:           size,
		Label:          raw.Label,
		Magnet:         raw.Magnet,
		Seeders:        seeders,
		Leechers:       leechers,
		CompletedTimes: completed,
		UpdatedAt:      updatedAt,
		Release: ReleaseSummary{
			ID:       ReleaseID(releaseID),
			Type:     releaseType,
			Year:     int(year),
			MainName: raw.Release.Name.Main,
			Alias:    raw.Release.Alias,
		},
	}, "", nil
}

func magnetContainsInfoHash(magnet *url.URL, hash string) bool {
	query, err := url.ParseQuery(magnet.RawQuery)
	if err != nil {
		return false
	}
	const prefix = "urn:btih:"
	for _, exactTopic := range query["xt"] {
		if len(exactTopic) != len(prefix)+40 || !strings.EqualFold(exactTopic[:len(prefix)], prefix) {
			continue
		}
		candidate, err := normalizeInfoHash(exactTopic[len(prefix):])
		if err == nil && candidate == hash {
			return true
		}
	}
	return false
}

func invalidTorrentStringField(data []byte) string {
	var fields struct {
		Hash      json.RawMessage `json:"hash"`
		Label     json.RawMessage `json:"label"`
		Magnet    json.RawMessage `json:"magnet"`
		UpdatedAt json.RawMessage `json:"updated_at"`
		Release   struct {
			Type struct {
				Value json.RawMessage `json:"value"`
			} `json:"type"`
			Name struct {
				Main json.RawMessage `json:"main"`
			} `json:"name"`
			Alias json.RawMessage `json:"alias"`
		} `json:"release"`
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		return ""
	}
	strings := []struct {
		name string
		raw  json.RawMessage
	}{
		{"hash", fields.Hash},
		{"label", fields.Label},
		{"magnet", fields.Magnet},
		{"updated_at", fields.UpdatedAt},
		{"release.type.value", fields.Release.Type.Value},
		{"release.name.main", fields.Release.Name.Main},
		{"release.alias", fields.Release.Alias},
	}
	for _, field := range strings {
		if len(field.raw) > 0 && field.raw[0] == '"' && !validJSONStringEncoding(field.raw) {
			return field.name
		}
	}
	return ""
}

func validJSONStringEncoding(value []byte) bool {
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return true
	}
	for index := 1; index < len(value)-1; {
		if value[index] == '\\' {
			if index+1 >= len(value)-1 {
				return false
			}
			if value[index+1] != 'u' {
				index += 2
				continue
			}
			code, ok := jsonHexQuad(value, index+2)
			if !ok {
				return false
			}
			index += 6
			switch {
			case code >= 0xd800 && code <= 0xdbff:
				if index+6 > len(value)-1 || value[index] != '\\' || value[index+1] != 'u' {
					return false
				}
				low, ok := jsonHexQuad(value, index+2)
				if !ok || low < 0xdc00 || low > 0xdfff {
					return false
				}
				index += 6
			case code >= 0xdc00 && code <= 0xdfff:
				return false
			}
			continue
		}
		if value[index] < utf8.RuneSelf {
			index++
			continue
		}
		runeValue, size := utf8.DecodeRune(value[index : len(value)-1])
		if runeValue == utf8.RuneError && size == 1 {
			return false
		}
		index += size
	}
	return true
}

func jsonHexQuad(value []byte, start int) (uint16, bool) {
	if start+4 > len(value) {
		return 0, false
	}
	var result uint16
	for _, digit := range value[start : start+4] {
		result <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			result += uint16(digit - '0')
		case digit >= 'a' && digit <= 'f':
			result += uint16(digit-'a') + 10
		case digit >= 'A' && digit <= 'F':
			result += uint16(digit-'A') + 10
		default:
			return 0, false
		}
	}
	return result, true
}

func (releaseType ReleaseType) valid() bool {
	switch releaseType {
	case ReleaseTypeTV, ReleaseTypeONA, ReleaseTypeWEB, ReleaseTypeOVA,
		ReleaseTypeOAD, ReleaseTypeMovie, ReleaseTypeDorama, ReleaseTypeSpecial:
		return true
	default:
		return false
	}
}

func normalizeInfoHash(value string) (string, error) {
	if len(value) != 40 {
		return "", fmt.Errorf("infohash must contain 40 hexadecimal characters")
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return "", fmt.Errorf("infohash must contain only hexadecimal characters")
		}
	}
	return strings.ToLower(value), nil
}

func positiveInteger(value json.Number) (int64, error) {
	integer, err := integerValuedNumber(value)
	if err != nil || integer <= 0 {
		return 0, fmt.Errorf("expected a positive integer")
	}
	return integer, nil
}

func nonNegativeInteger(value json.Number) (int64, error) {
	integer, err := value.Int64()
	if err != nil || integer < 0 {
		return 0, fmt.Errorf("expected a non-negative integer")
	}
	return integer, nil
}

func optionalNonNegativeInteger(value json.Number) (int64, error) {
	if value == "" {
		return 0, nil
	}
	integer, err := integerValuedNumber(value)
	if err != nil || integer < 0 {
		return 0, fmt.Errorf("expected a non-negative integer-valued number")
	}
	return integer, nil
}

func integerValuedNumber(value json.Number) (int64, error) {
	if value == "" || len(value) > 64 {
		return 0, fmt.Errorf("expected an integer-valued number")
	}
	rational, ok := new(big.Rat).SetString(value.String())
	if !ok || !rational.IsInt() || !rational.Num().IsInt64() {
		return 0, fmt.Errorf("expected an integer-valued number")
	}
	return rational.Num().Int64(), nil
}

func validXMLString(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if char == '\t' || char == '\n' || char == '\r' ||
			(char >= 0x20 && char <= 0xd7ff) ||
			(char >= 0xe000 && char <= 0xfffd) ||
			(char >= 0x10000 && char <= 0x10ffff) {
			continue
		}
		return false
	}
	return true
}
