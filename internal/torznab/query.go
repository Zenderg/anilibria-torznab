package torznab

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	queryToken = regexp.MustCompile(`(?i)S[0-9]+` + regexpWhitespace + `+E[0-9]+|S[0-9]+E[0-9]+|[0-9]+[xX][0-9]+|Season` + regexpWhitespace + `+[0-9]+|Episode` + regexpWhitespace + `+[0-9]+|Ep` + regexpWhitespace + `+[0-9]+|S[0-9]+|E[0-9]+`)
	querySE    = regexp.MustCompile(`(?i)^S([0-9]+)` + regexpWhitespace + `*E([0-9]+)$`)
	queryX     = regexp.MustCompile(`(?i)^([0-9]+)X([0-9]+)$`)
	queryS     = regexp.MustCompile(`(?i)^(?:S|Season` + regexpWhitespace + `+)([0-9]+)$`)
	queryE     = regexp.MustCompile(`(?i)^(?:E|Ep` + regexpWhitespace + `+|Episode` + regexpWhitespace + `+)([0-9]+)$`)
)

// Parameter identifies a canonical Torznab parameter.
type Parameter string

const (
	ParameterAPIKey   Parameter = "apikey"
	ParameterType     Parameter = "t"
	ParameterQuery    Parameter = "q"
	ParameterCategory Parameter = "cat"
	ParameterLimit    Parameter = "limit"
	ParameterOffset   Parameter = "offset"
	ParameterExtended Parameter = "extended"
	ParameterSeason   Parameter = "season"
	ParameterEpisode  Parameter = "ep"
)

// ParameterError reports a malformed or conflicting canonical parameter.
type ParameterError struct {
	Parameter Parameter
}

func (e *ParameterError) Error() string {
	return fmt.Sprintf("incorrect parameter: %s", e.Parameter)
}

// NormalizedQuery is the cleaned upstream query and the effective TV filters.
type NormalizedQuery struct {
	CleanQuery         string
	EffectiveSeason    *int
	EffectiveEpisode   *int
	HadTechnicalTokens bool
}

// NormalizeQuery validates optional explicit filters and removes complete,
// unambiguous Sonarr/Prowlarr season and episode tokens from query.
func NormalizeQuery(query string, explicitSeason, explicitEpisode *string) (NormalizedQuery, error) {
	season, err := parseExplicitFilter(explicitSeason, "S", maxSeason, ParameterSeason)
	if err != nil {
		return NormalizedQuery{}, err
	}
	episode, err := parseExplicitFilter(explicitEpisode, "E", maxEpisode, ParameterEpisode)
	if err != nil {
		return NormalizedQuery{}, err
	}

	indexes := queryToken.FindAllStringIndex(query, -1)
	remove := make([]bool, len(query))
	technical := make([]bool, len(query))
	tokenRanges := make([][2]int, 0, len(indexes))
	hadTokens := false
	for _, index := range indexes {
		if !completeToken(query, index[0], index[1]) {
			continue
		}
		if unsupportedNumericContinuation(query, index[0], index[1]) {
			return NormalizedQuery{}, &ParameterError{Parameter: ParameterQuery}
		}
		token := query[index[0]:index[1]]
		tokenSeason, tokenEpisode, valid := parseQueryToken(token)
		if !valid {
			return NormalizedQuery{}, &ParameterError{Parameter: ParameterQuery}
		}
		if tokenSeason != nil {
			if season != nil && *season != *tokenSeason {
				return NormalizedQuery{}, &ParameterError{Parameter: ParameterSeason}
			}
			season = tokenSeason
		}
		if tokenEpisode != nil {
			if episode != nil && *episode != *tokenEpisode {
				return NormalizedQuery{}, &ParameterError{Parameter: ParameterEpisode}
			}
			episode = tokenEpisode
		}
		for i := index[0]; i < index[1]; i++ {
			remove[i] = true
			technical[i] = true
		}
		tokenRanges = append(tokenRanges, [2]int{index[0], index[1]})
		hadTokens = true
	}
	for _, tokenRange := range tokenRanges {
		markStandaloneTokenSeparators(query, remove, tokenRange[0], tokenRange[1])
	}
	markEmptyTechnicalPairs(query, remove, technical)

	var cleaned strings.Builder
	for index := 0; index < len(query); {
		if remove[index] {
			index++
			continue
		}
		r, width := utf8.DecodeRuneInString(query[index:])
		if unicode.IsSpace(r) {
			if cleaned.Len() > 0 {
				cleaned.WriteByte(' ')
			}
		} else {
			cleaned.WriteRune(r)
		}
		index += width
	}
	cleanQuery := strings.TrimSpace(cleaned.String())
	cleanQuery = collapseWhitespace(cleanQuery)

	return NormalizedQuery{
		CleanQuery:         cleanQuery,
		EffectiveSeason:    season,
		EffectiveEpisode:   episode,
		HadTechnicalTokens: hadTokens,
	}, nil
}

func parseExplicitFilter(raw *string, prefix string, maximum int, parameter Parameter) (*int, error) {
	if raw == nil {
		return nil, nil
	}
	value := strings.TrimSpace(*raw)
	if len(value) > 0 && strings.EqualFold(value[:1], prefix) {
		value = value[1:]
	}
	parsed, valid := boundedPositiveInteger(value, maximum)
	if !valid {
		return nil, &ParameterError{Parameter: parameter}
	}
	return intPointer(parsed), nil
}

func parseQueryToken(token string) (*int, *int, bool) {
	if match := querySE.FindStringSubmatch(token); len(match) == 3 {
		season, seasonOK := boundedPositiveInteger(match[1], maxSeason)
		episode, episodeOK := boundedPositiveInteger(match[2], maxEpisode)
		return intPointer(season), intPointer(episode), seasonOK && episodeOK
	}
	if match := queryX.FindStringSubmatch(token); len(match) == 3 {
		season, seasonOK := boundedPositiveInteger(match[1], maxSeason)
		episode, episodeOK := boundedPositiveInteger(match[2], maxEpisode)
		return intPointer(season), intPointer(episode), seasonOK && episodeOK
	}
	if match := queryS.FindStringSubmatch(token); len(match) == 2 {
		season, valid := boundedPositiveInteger(match[1], maxSeason)
		return intPointer(season), nil, valid
	}
	if match := queryE.FindStringSubmatch(token); len(match) == 2 {
		episode, valid := boundedPositiveInteger(match[1], maxEpisode)
		return nil, intPointer(episode), valid
	}
	return nil, nil, false
}

func collapseWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func unsupportedNumericContinuation(value string, start, end int) bool {
	return numericContinuationAfter(value[end:]) || numericContinuationBefore(value[:start])
}

func numericContinuationAfter(value string) bool {
	index := skipSpaceForward(value, 0)
	spacedBeforeSeparator := index > 0
	separator, width := firstRune(value[index:])
	if width == 0 || !strings.ContainsRune(".-–—/", separator) {
		return false
	}
	separatorEnd := index + width
	index = skipSpaceForward(value, separatorEnd)
	spacedAfterSeparator := index > separatorEnd
	digitStart := index
	for index < len(value) && value[index] >= '0' && value[index] <= '9' {
		index++
	}
	if index == digitStart {
		return false
	}
	if index-digitStart == 4 && spacedBeforeSeparator && spacedAfterSeparator {
		return false
	}
	if index == len(value) {
		return true
	}
	next, _ := firstRune(value[index:])
	if next == 'e' || next == 'E' || next == 'x' || next == 'X' {
		return true
	}
	return !unicode.IsLetter(next) && !unicode.IsDigit(next)
}

func numericContinuationBefore(value string) bool {
	index := skipSpaceBackward(value, len(value))
	spacedAfterSeparator := index < len(value)
	separator, width := lastRune(value[:index])
	if width == 0 || !strings.ContainsRune(".-–—/", separator) {
		return false
	}
	separatorStart := index - width
	index = skipSpaceBackward(value, separatorStart)
	spacedBeforeSeparator := index < separatorStart
	digitEnd := index
	for index > 0 && value[index-1] >= '0' && value[index-1] <= '9' {
		index--
	}
	if index == digitEnd {
		return false
	}
	if digitEnd-index == 4 && spacedBeforeSeparator && spacedAfterSeparator {
		return false
	}
	if index == 0 {
		return true
	}
	previous, _ := lastRune(value[:index])
	if previous == 's' || previous == 'S' || previous == 'e' || previous == 'E' {
		return true
	}
	return !unicode.IsLetter(previous) && !unicode.IsDigit(previous)
}

func markStandaloneTokenSeparators(value string, remove []bool, start, end int) {
	if emptyAfterRemoval(value, remove, 0, start) {
		afterSpaces := skipSpaceForward(value, end)
		afterSeparators := afterSpaces
		for afterSeparators < len(value) {
			r, width := firstRune(value[afterSeparators:])
			if !isTokenSeparator(r) {
				break
			}
			afterSeparators += width
		}
		if afterSeparators > afterSpaces {
			if afterSeparators == len(value) {
				markBytes(remove, end, afterSeparators)
			} else if next, _ := firstRune(value[afterSeparators:]); unicode.IsSpace(next) {
				markBytes(remove, end, skipSpaceForward(value, afterSeparators))
			}
		}
	}

	if !emptyAfterRemoval(value, remove, end, len(value)) {
		return
	}
	beforeSpaces := skipSpaceBackward(value, start)
	beforeSeparators := beforeSpaces
	for beforeSeparators > 0 {
		r, width := lastRune(value[:beforeSeparators])
		if !isTokenSeparator(r) {
			break
		}
		beforeSeparators -= width
	}
	if beforeSeparators == beforeSpaces {
		return
	}
	if beforeSeparators == 0 {
		markBytes(remove, 0, start)
	} else if previous, _ := lastRune(value[:beforeSeparators]); unicode.IsSpace(previous) {
		markBytes(remove, skipSpaceBackward(value, beforeSeparators), start)
	}
}

func markEmptyTechnicalPairs(value string, remove, technical []bool) {
	pairs := [][2]byte{{'(', ')'}, {'[', ']'}, {'{', '}'}}
	for {
		changed := false
		for _, pair := range pairs {
			var openings []int
			for index := 0; index < len(value); index++ {
				switch value[index] {
				case pair[0]:
					openings = append(openings, index)
				case pair[1]:
					if len(openings) == 0 {
						continue
					}
					open := openings[len(openings)-1]
					openings = openings[:len(openings)-1]
					if !containsMarked(technical, open+1, index) || !emptyAfterRemoval(value, remove, open+1, index) {
						continue
					}
					if markBytes(remove, open, index+1) {
						changed = true
					}
					markStandaloneTokenSeparators(value, remove, open, index+1)
				}
			}
		}
		if !changed {
			return
		}
	}
}

func emptyAfterRemoval(value string, remove []bool, start, end int) bool {
	for index := start; index < end; {
		if remove[index] {
			index++
			continue
		}
		r, width := utf8.DecodeRuneInString(value[index:end])
		if !unicode.IsSpace(r) {
			return false
		}
		index += width
	}
	return true
}

func containsMarked(marked []bool, start, end int) bool {
	for index := start; index < end; index++ {
		if marked[index] {
			return true
		}
	}
	return false
}

func markBytes(marked []bool, start, end int) bool {
	changed := false
	for index := start; index < end; index++ {
		if !marked[index] {
			marked[index] = true
			changed = true
		}
	}
	return changed
}

func skipSpaceForward(value string, index int) int {
	for index < len(value) {
		r, width := firstRune(value[index:])
		if !unicode.IsSpace(r) {
			break
		}
		index += width
	}
	return index
}

func skipSpaceBackward(value string, index int) int {
	for index > 0 {
		r, width := lastRune(value[:index])
		if !unicode.IsSpace(r) {
			break
		}
		index -= width
	}
	return index
}

func isTokenSeparator(r rune) bool {
	return strings.ContainsRune("-–—_|/,:;.", r)
}

func firstRune(value string) (rune, int) {
	return utf8.DecodeRuneInString(value)
}

func lastRune(value string) (rune, int) {
	return utf8.DecodeLastRuneInString(value)
}

func decimalString(value int64) string {
	return strconv.FormatInt(value, 10)
}
