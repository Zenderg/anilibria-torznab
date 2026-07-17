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

const maxQueryBytes = 4096

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
	if len(query) > maxQueryBytes {
		return NormalizedQuery{}, &ParameterError{Parameter: ParameterQuery}
	}

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
		hadTokens = true
	}
	markEmptyTechnicalPairs(query, remove, technical)
	markTechnicalEdgeSeparators(query, remove, technical)

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
	return true
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
	return true
}

func markEmptyTechnicalPairs(value string, remove, technical []bool) {
	type frame struct {
		open         int
		close        rune
		hasTechnical bool
		hasVisible   bool
	}

	var stack []frame
	for index := 0; index < len(value); {
		r, width := firstRune(value[index:])
		if remove[index] {
			if len(stack) > 0 {
				stack[len(stack)-1].hasTechnical = true
			}
			index += width
			continue
		}
		if unicode.IsSpace(r) {
			index += width
			continue
		}
		if close, ok := technicalPairClose(r); ok {
			stack = append(stack, frame{open: index, close: close})
			index += width
			continue
		}
		if len(stack) > 0 && r == stack[len(stack)-1].close {
			current := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if current.hasTechnical && !current.hasVisible {
				markBytes(remove, current.open, index+width)
				markBytes(technical, current.open, index+width)
				if len(stack) > 0 {
					stack[len(stack)-1].hasTechnical = true
				}
			} else if len(stack) > 0 {
				stack[len(stack)-1].hasVisible = true
				stack[len(stack)-1].hasTechnical = stack[len(stack)-1].hasTechnical || current.hasTechnical
			}
			index += width
			continue
		}
		if len(stack) > 0 {
			stack[len(stack)-1].hasVisible = true
		}
		index += width
	}
}

func technicalPairClose(r rune) (rune, bool) {
	switch r {
	case '(':
		return ')', true
	case '[':
		return ']', true
	case '{':
		return '}', true
	default:
		return 0, false
	}
}

func markTechnicalEdgeSeparators(value string, remove, technical []bool) {
	var leadingSeparators [][2]int
	sawTechnical := false
	for index := 0; index < len(value); {
		if remove[index] {
			sawTechnical = sawTechnical || technical[index]
			index++
			continue
		}
		r, width := firstRune(value[index:])
		switch {
		case unicode.IsSpace(r):
		case isTokenSeparator(r):
			leadingSeparators = append(leadingSeparators, [2]int{index, index + width})
		default:
			index = len(value)
			continue
		}
		index += width
	}
	if sawTechnical {
		for _, separator := range leadingSeparators {
			markBytes(remove, separator[0], separator[1])
		}
	}

	var trailingSeparators [][2]int
	sawTechnical = false
	for index := len(value); index > 0; {
		if remove[index-1] {
			sawTechnical = sawTechnical || technical[index-1]
			index--
			continue
		}
		r, width := lastRune(value[:index])
		switch {
		case unicode.IsSpace(r):
		case isTokenSeparator(r):
			trailingSeparators = append(trailingSeparators, [2]int{index - width, index})
		default:
			index = 0
			continue
		}
		index -= width
	}
	if sawTechnical {
		for _, separator := range trailingSeparators {
			markBytes(remove, separator[0], separator[1])
		}
	}
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
