package anilibria

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateTorrentRejectsInvalidRequiredFields(t *testing.T) {
	t.Parallel()

	valid := rawTorrent{
		Hash:           testHash,
		Size:           json.Number("1"),
		Label:          "label",
		Magnet:         "magnet:?xt=urn:btih:" + testHash,
		Seeders:        json.Number("0"),
		Leechers:       json.Number("0"),
		CompletedTimes: json.Number("0"),
		UpdatedAt:      "2026-07-16T10:11:12Z",
		Release: rawRelease{
			ID:    json.Number("1"),
			Type:  rawType{Value: "TV"},
			Year:  json.Number("2026"),
			Name:  rawName{Main: "name"},
			Alias: "alias",
		},
	}
	tests := []struct {
		name  string
		field string
		alter func(*rawTorrent)
	}{
		{"short hash", "hash", func(value *rawTorrent) { value.Hash = "abc" }},
		{"non-hex hash", "hash", func(value *rawTorrent) { value.Hash = "z" + value.Hash[1:] }},
		{"fractional size", "size", func(value *rawTorrent) { value.Size = "1.5" }},
		{"negative seeders", "seeders", func(value *rawTorrent) { value.Seeders = "-1" }},
		{"negative leechers", "leechers", func(value *rawTorrent) { value.Leechers = "-1" }},
		{"negative completions", "completed_times", func(value *rawTorrent) { value.CompletedTimes = "-1" }},
		{"fractional release id", "release.id", func(value *rawTorrent) { value.Release.ID = "1.5" }},
		{"fractional year", "release.year", func(value *rawTorrent) { value.Release.Year = "2026.5" }},
		{"unknown type", "release.type.value", func(value *rawTorrent) { value.Release.Type.Value = "FUTURE" }},
		{"invalid timestamp", "updated_at", func(value *rawTorrent) { value.UpdatedAt = "today" }},
		{"HTTP URI", "magnet", func(value *rawTorrent) { value.Magnet = "https://example.test/file" }},
		{"empty magnet", "magnet", func(value *rawTorrent) { value.Magnet = "magnet:" }},
		{"magnet without exact topic", "magnet", func(value *rawTorrent) { value.Magnet = "magnet:?dn=Example" }},
		{"mismatched exact topic", "magnet", func(value *rawTorrent) { value.Magnet = "magnet:?xt=urn:btih:" + strings.Repeat("1", 40) }},
		{"empty label", "label", func(value *rawTorrent) { value.Label = "" }},
		{"XML control in name", "release.name.main", func(value *rawTorrent) { value.Release.Name.Main = "bad\x01name" }},
		{"empty alias", "release.alias", func(value *rawTorrent) { value.Release.Alias = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			test.alter(&value)
			_, field, err := validateTorrent(value)
			if err == nil || field != test.field {
				t.Fatalf("validateTorrent field=%q err=%v, want field=%q", field, err, test.field)
			}
		})
	}
}

func TestValidateTorrentAcceptsMatchingInfoHashCaseInsensitively(t *testing.T) {
	t.Parallel()
	raw := rawTorrent{
		Hash:           strings.ToUpper(testHash),
		Size:           "1",
		Label:          "label",
		Magnet:         "magnet:?dn=Example&xt=URN:BTIH:" + strings.ToUpper(testHash) + "&tr=https%3A%2F%2Ftracker.example",
		Seeders:        "0",
		Leechers:       "0",
		CompletedTimes: "0",
		UpdatedAt:      "2026-07-16T10:11:12Z",
		Release: rawRelease{
			ID:    "413",
			Type:  rawType{Value: "TV"},
			Year:  "2026",
			Name:  rawName{Main: "name"},
			Alias: "alias",
		},
	}
	torrent, field, err := validateTorrent(raw)
	if err != nil {
		t.Fatalf("validateTorrent rejected %s: %v", field, err)
	}
	if torrent.Hash != testHash {
		t.Fatalf("normalized hash = %q", torrent.Hash)
	}
}

func TestValidateTorrentAcceptsEveryDeclaredReleaseTypeAndOptionalYear(t *testing.T) {
	t.Parallel()

	expectedTypes := []ReleaseType{
		ReleaseTypeTV, ReleaseTypeONA, ReleaseTypeWEB, ReleaseTypeOVA,
		ReleaseTypeOAD, ReleaseTypeMovie, ReleaseTypeDorama, ReleaseTypeSpecial,
	}
	var fixtures []rawTorrent
	if err := decodeJSON(context.Background(), []byte(fixtureString(t, "torrents_release_types.json")), &fixtures); err != nil {
		t.Fatalf("decode release-type fixture: %v", err)
	}
	if len(fixtures) != len(expectedTypes) {
		t.Fatalf("fixture item count = %d, want %d", len(fixtures), len(expectedTypes))
	}
	for index, raw := range fixtures {
		torrent, field, err := validateTorrent(raw)
		if err != nil {
			t.Fatalf("type fixture %d rejected at %s: %v", index, field, err)
		}
		if torrent.Release.Type != expectedTypes[index] {
			t.Fatalf("normalized torrent = %+v", torrent)
		}
	}

	withoutYear := fixtures[0]
	withoutYear.Release.Year = ""
	torrent, field, err := validateTorrent(withoutYear)
	if err != nil || torrent.Release.Year != 0 {
		t.Fatalf("optional year rejected at %s: torrent=%+v err=%v", field, torrent, err)
	}
}

func TestValidateTorrentAcceptsIntegerValuedReleaseNumbers(t *testing.T) {
	t.Parallel()

	raw := rawTorrent{
		Hash:           testHash,
		Size:           "1",
		Label:          "label",
		Magnet:         "magnet:?xt=urn:btih:" + testHash,
		Seeders:        "0",
		Leechers:       "0",
		CompletedTimes: "0",
		UpdatedAt:      "2026-07-16T10:11:12Z",
		Release: rawRelease{
			ID:    "4.13e2",
			Type:  rawType{Value: "TV"},
			Year:  "2002.0",
			Name:  rawName{Main: "name"},
			Alias: "alias",
		},
	}
	torrent, field, err := validateTorrent(raw)
	if err != nil {
		t.Fatalf("validateTorrent rejected %s: %v", field, err)
	}
	if torrent.Release.ID != 413 || torrent.Release.Year != 2002 {
		t.Fatalf("release = %+v", torrent.Release)
	}
}
