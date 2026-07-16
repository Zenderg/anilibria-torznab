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

func validateTorrent(raw rawTorrent) (Torrent, string, error) {
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
	if err != nil || !strings.EqualFold(magnet.Scheme, "magnet") {
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
