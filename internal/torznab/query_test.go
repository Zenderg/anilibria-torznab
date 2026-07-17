package torznab

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestNormalizeQueryNormativeExamples(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		query    string
		season   *string
		episode  *string
		clean    string
		wantS    *int
		wantE    *int
		hadToken bool
	}{
		{"combined", "Title S02E03", nil, nil, "Title", intPointer(2), intPointer(3), true},
		{"words and explicit episode", "Title Season 2", nil, stringPointer("E03"), "Title", intPointer(2), intPointer(3), true},
		{"agreeing explicit season", "Title S02", stringPointer("S02"), nil, "Title", intPointer(2), nil, true},
		{"part retained", "Title\u2003Part   2", nil, nil, "Title Part 2", nil, nil, false},
		{"x notation", "2x03 - Title", nil, nil, "Title", intPointer(2), intPointer(3), true},
		{"separate tokens", "Title S02 E03", nil, nil, "Title", intPointer(2), intPointer(3), true},
		{"next-line whitespace", "Title Season\u00852", nil, nil, "Title", intPointer(2), nil, true},
		{"meaningful leading punctuation", ".hack//SIGN S01", nil, nil, ".hack//SIGN", intPointer(1), nil, true},
		{"meaningful punctuation after leading token", "S01 .hack//SIGN", nil, nil, ".hack//SIGN", intPointer(1), nil, true},
		{"meaningful underscore after leading token", "Season 2 _summer", nil, nil, "_summer", intPointer(2), nil, true},
		{"title number before spaced token separator", "Show 2 - S02", nil, nil, "Show 2", intPointer(2), nil, true},
		{"title number after spaced token separator", "S02 - 2 Show", nil, nil, "2 Show", intPointer(2), nil, true},
		{"pre-existing empty pair", "Title () S02", nil, nil, "Title ()", intPointer(2), nil, true},
		{"pair emptied by token", "Title (S02)", nil, nil, "Title", intPointer(2), nil, true},
		{"year and internal separator retained", "Title S02E03 - 2026", nil, nil, "Title - 2026", intPointer(2), intPointer(3), true},
		{"empty after cleanup", "S02E03", nil, nil, "", intPointer(2), intPointer(3), true},
		{"embedded text retained", "MyS02 ShowE03", nil, nil, "MyS02 ShowE03", nil, nil, false},
		{"trailing hyphen", "Title-S02", nil, nil, "Title", intPointer(2), nil, true},
		{"leading hyphen", "S02-Title", nil, nil, "Title", intPointer(2), nil, true},
		{"trailing dot", "Title.S02", nil, nil, "Title", intPointer(2), nil, true},
		{"leading dot", "S02.Title", nil, nil, "Title", intPointer(2), nil, true},
		{"trailing slash", "Title/S02", nil, nil, "Title", intPointer(2), nil, true},
		{"leading slash", "S02/Title", nil, nil, "Title", intPointer(2), nil, true},
		{"parenthesized token before trailing separator", "Title (S02)-", nil, nil, "Title", intPointer(2), nil, true},
		{"parenthesized token after leading separator", "-(S02) Title", nil, nil, "Title", intPointer(2), nil, true},
		{"nested technical pairs", "Title ([S02])", nil, nil, "Title", intPointer(2), nil, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result, err := NormalizeQuery(test.query, test.season, test.episode)
			if err != nil {
				t.Fatal(err)
			}
			if result.CleanQuery != test.clean || result.HadTechnicalTokens != test.hadToken {
				t.Fatalf("result = %#v", result)
			}
			assertOptionalInt(t, result.EffectiveSeason, test.wantS)
			assertOptionalInt(t, result.EffectiveEpisode, test.wantE)
		})
	}
}

func TestNormalizeQueryRejectsMalformedAndConflictingValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		query   string
		season  *string
		episode *string
		param   Parameter
	}{
		{"Title", stringPointer("0"), nil, ParameterSeason},
		{"Title", nil, stringPointer("2/12"), ParameterEpisode},
		{"Title S02E03", stringPointer("1"), stringPointer("3"), ParameterSeason},
		{"Title S02E03", stringPointer("2"), stringPointer("4"), ParameterEpisode},
		{"Title S000", nil, nil, ParameterQuery},
		{"Title S1000", nil, nil, ParameterQuery},
		{"Title S02E03.5", nil, nil, ParameterQuery},
		{"Title S02E03-04", nil, nil, ParameterQuery},
		{"Title Episode 1-2", nil, nil, ParameterQuery},
		{"Title Season 2.5", nil, nil, ParameterQuery},
		{"Title S02.5E03", nil, nil, ParameterQuery},
		{"Title 2.5x03", nil, nil, ParameterQuery},
		{"Title S02.5th", nil, nil, ParameterQuery},
		{"Title Episode 1-2nd", nil, nil, ParameterQuery},
		{"Title S02/3abc", nil, nil, ParameterQuery},
		{"Title S02E03.5th", nil, nil, ParameterQuery},
	}
	for _, test := range tests {
		_, err := NormalizeQuery(test.query, test.season, test.episode)
		var parameterError *ParameterError
		if !errors.As(err, &parameterError) || parameterError.Parameter != test.param {
			t.Errorf("NormalizeQuery(%q) error = %v, want ParameterError(%s)", test.query, err, test.param)
		}
	}
}

func TestNormalizeQueryBoundsWorkBeforeTokenScanning(t *testing.T) {
	t.Parallel()

	query := strings.Repeat("S01 ", 1000) + "Title"
	result, err := NormalizeQuery(query, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.CleanQuery != "Title" || result.EffectiveSeason == nil || *result.EffectiveSeason != 1 {
		t.Fatalf("result = %#v", result)
	}

	_, err = NormalizeQuery(strings.Repeat("x", maxQueryBytes+1), nil, nil)
	var parameterError *ParameterError
	if !errors.As(err, &parameterError) || parameterError.Parameter != ParameterQuery {
		t.Fatalf("oversized query error = %v", err)
	}
}

func BenchmarkNormalizeQueryRepeatedTokens(b *testing.B) {
	for _, count := range []int{100, 500, 1000} {
		query := strings.Repeat("S01 ", count) + "Title"
		b.Run(fmt.Sprintf("tokens_%d", count), func(b *testing.B) {
			for b.Loop() {
				if _, err := NormalizeQuery(query, nil, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func stringPointer(value string) *string {
	return &value
}
