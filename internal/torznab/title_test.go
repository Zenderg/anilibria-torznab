package torznab

import "testing"

func TestParseTitleNormativeExamples(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		releaseType ReleaseType
		label       string
		season      *int
		episodes    *EpisodeRange
		title       string
	}{
		{
			name: "default season and range", releaseType: ReleaseTV,
			label:  "Let's Go Kaiki-gumi - AniLiberty.TOP [WEB-DL 1080p][AVC][1-2]",
			season: intPointer(1), episodes: &EpisodeRange{Start: 1, End: 2},
			title: "Let's Go Kaiki-gumi S01E01-E02 RUS / RU / Let's Go Kaiki-gumi - AniLiberty.TOP [WEB-DL 1080p][AVC][1-2]",
		},
		{
			name: "explicit season", releaseType: ReleaseTV,
			label:  "Example Season 2 - AniLiberty.TOP [WEB-DL 1080p][13]",
			season: intPointer(2), episodes: &EpisodeRange{Start: 13, End: 13},
			title: "Example Season 2 S02E13 RUS / RU / Example Season 2 - AniLiberty.TOP [WEB-DL 1080p][13]",
		},
		{
			name: "correct ordinal", releaseType: ReleaseTV,
			label:  "Example 2nd Season - AniLiberty.TOP [WEB-DL 1080p][1-12]",
			season: intPointer(2), episodes: &EpisodeRange{Start: 1, End: 12},
			title: "Example 2nd Season S02E01-E12 RUS / RU / Example 2nd Season - AniLiberty.TOP [WEB-DL 1080p][1-12]",
		},
		{
			name: "roman", releaseType: ReleaseTV,
			label:  "Example II - AniLiberty.TOP [WEB-DL 1080p][03]",
			season: intPointer(2), episodes: &EpisodeRange{Start: 3, End: 3},
			title: "Example II S02E03 RUS / RU / Example II - AniLiberty.TOP [WEB-DL 1080p][03]",
		},
		{
			name: "part is not season", releaseType: ReleaseTV,
			label:  "Example Part 2 - AniLiberty.TOP [WEB-DL 1080p][04]",
			season: intPointer(1), episodes: &EpisodeRange{Start: 4, End: 4},
			title: "Example Part 2 S01E04 RUS / RU / Example Part 2 - AniLiberty.TOP [WEB-DL 1080p][04]",
		},
		{
			name: "bare title number is not season", releaseType: ReleaseTV,
			label:  "86 - Eighty Six - AniLiberty.TOP [WEB-DL 1080p][01]",
			season: intPointer(1), episodes: &EpisodeRange{Start: 1, End: 1},
			title: "86 - Eighty Six S01E01 RUS / RU / 86 - Eighty Six - AniLiberty.TOP [WEB-DL 1080p][01]",
		},
		{
			name: "year is ignored", releaseType: ReleaseTV,
			label:  "Example (2024) - AniLiberty.TOP [WEB-DL 1080p][1]",
			season: intPointer(1), episodes: &EpisodeRange{Start: 1, End: 1},
			title: "Example (2024) S01E01 RUS / RU / Example (2024) - AniLiberty.TOP [WEB-DL 1080p][1]",
		},
		{
			name: "mixed episode group rejected", releaseType: ReleaseTV,
			label:  "Example - AniLiberty.TOP [WEB-DL 1080p][1-12 + OVA]",
			season: intPointer(1), episodes: nil,
			title: "Example S01 RUS / RU / Example - AniLiberty.TOP [WEB-DL 1080p][1-12 + OVA]",
		},
		{
			name: "movie does not synthesize metadata", releaseType: ReleaseMovie,
			label:  "Movie Season 2 - AniLiberty.TOP [WEB-DL 1080p]",
			season: nil, episodes: nil,
			title: "Movie Season 2 RUS / RU / Movie Season 2 - AniLiberty.TOP [WEB-DL 1080p]",
		},
		{
			name: "movie preserves explicit episode", releaseType: ReleaseMovie,
			label:  "Movie - AniLiberty.TOP [WEB-DL 1080p][Episode 123]",
			season: intPointer(1), episodes: &EpisodeRange{Start: 123, End: 123},
			title: "Movie S01E123 RUS / RU / Movie - AniLiberty.TOP [WEB-DL 1080p][Episode 123]",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			metadata, err := ParseTitle(test.releaseType, " RU ", " \t"+test.label+"\n")
			if err != nil {
				t.Fatalf("ParseTitle() error = %v", err)
			}
			assertOptionalInt(t, metadata.Season, test.season)
			if metadata.Episodes == nil || test.episodes == nil {
				if metadata.Episodes != test.episodes {
					t.Fatalf("Episodes = %#v, want %#v", metadata.Episodes, test.episodes)
				}
			} else if *metadata.Episodes != *test.episodes {
				t.Fatalf("Episodes = %#v, want %#v", metadata.Episodes, test.episodes)
			}
			if metadata.Title != test.title {
				t.Fatalf("Title = %q, want %q", metadata.Title, test.title)
			}
		})
	}
}

func TestParseTitleSonarrNarutoRegression(t *testing.T) {
	t.Parallel()
	const (
		mainName = "Наруто Ураганные хроники"
		label    = "Naruto- Shippuuden - AniLiberty.TOP [HDTVRip 720p][AVC][370-500]"
		want     = "Naruto- Shippuuden S01E370-E500 RUS / Наруто Ураганные хроники / Naruto- Shippuuden - AniLiberty.TOP [HDTVRip 720p][AVC][370-500]"
	)

	metadata, err := ParseTitle(ReleaseTV, mainName, label)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Title != want {
		t.Fatalf("Title = %q, want %q", metadata.Title, want)
	}
	if metadata.Season == nil || *metadata.Season != 1 || metadata.Episodes == nil || *metadata.Episodes != (EpisodeRange{Start: 370, End: 500}) {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestParseTitleRequiresSemanticLabelTitle(t *testing.T) {
	t.Parallel()
	for _, label := range []string{
		"- AniLiberty.TOP [WEB-DL 1080p][1]",
		" [WEB-DL 1080p][1]",
	} {
		if _, err := ParseTitle(ReleaseTV, "RU", label); err == nil {
			t.Errorf("ParseTitle(%q) accepted a blank semantic label title", label)
		}
	}
}

func TestParseTitleSeasonAndEpisodeSafeguards(t *testing.T) {
	t.Parallel()
	tests := []struct {
		label    string
		season   int
		episodes *EpisodeRange
	}{
		{"Title 11th Season [Episodes 7–9]", 11, &EpisodeRange{7, 9}},
		{"Title 12nd Season [EP 9—9]", 1, &EpisodeRange{9, 9}},
		{"Title XX (2024) [E 9999]", 20, &EpisodeRange{9999, 9999}},
		{"Title IIII [10000]", 1, nil},
		{"Title Season 0 II [0]", 1, nil},
		{"Title S1000 [12-1]", 1, nil},
	}
	for _, test := range tests {
		metadata, err := ParseTitle(ReleaseTV, "RU", test.label)
		if err != nil {
			t.Fatal(err)
		}
		if metadata.Season == nil || *metadata.Season != test.season {
			t.Errorf("ParseTitle(%q) season = %v, want %d", test.label, metadata.Season, test.season)
		}
		if metadata.Episodes == nil || test.episodes == nil {
			if metadata.Episodes != test.episodes {
				t.Errorf("ParseTitle(%q) episodes = %#v, want %#v", test.label, metadata.Episodes, test.episodes)
			}
		} else if *metadata.Episodes != *test.episodes {
			t.Errorf("ParseTitle(%q) episodes = %#v, want %#v", test.label, metadata.Episodes, test.episodes)
		}
	}
}

func TestTitleMetadataMatches(t *testing.T) {
	t.Parallel()
	metadata := TitleMetadata{Season: intPointer(2), Episodes: &EpisodeRange{Start: 3, End: 8}}
	if !metadata.Matches(intPointer(2), intPointer(5)) {
		t.Fatal("range should match")
	}
	if metadata.Matches(intPointer(1), nil) || metadata.Matches(nil, intPointer(9)) {
		t.Fatal("mismatched filters should not match")
	}
	if (TitleMetadata{}).Matches(nil, intPointer(1)) {
		t.Fatal("missing episodes should not match an episode filter")
	}
}

func assertOptionalInt(t *testing.T, got, want *int) {
	t.Helper()
	if got == nil || want == nil {
		if got != want {
			t.Fatalf("value = %v, want %v", got, want)
		}
		return
	}
	if *got != *want {
		t.Fatalf("value = %d, want %d", *got, *want)
	}
}
