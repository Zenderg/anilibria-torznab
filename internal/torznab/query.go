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
	queryToken = regexp.MustCompile(`(?i)S[0-9]+[\p{Z}\t\r\n\f\v]+E[0-9]+|S[0-9]+E[0-9]+|[0-9]+[xX][0-9]+|Season[\p{Z}\t\r\n\f\v]+[0-9]+|Episode[\p{Z}\t\r\n\f\v]+[0-9]+|Ep[\p{Z}\t\r\n\f\v]+[0-9]+|S[0-9]+|E[0-9]+`)
	querySE    = regexp.MustCompile(`(?i)^S([0-9]+)[\p{Z}\t\r\n\f\v]*E([0-9]+)$`)
	queryX     = regexp.MustCompile(`(?i)^([0-9]+)X([0-9]+)$`)
	queryS     = regexp.MustCompile(`(?i)^(?:S|Season[\p{Z}\t\r\n\f\v]+)([0-9]+)$`)
	queryE     = regexp.MustCompile(`(?i)^(?:E|Ep[\p{Z}\t\r\n\f\v]+|Episode[\p{Z}\t\r\n\f\v]+)([0-9]+)$`)
	emptyPairs = regexp.MustCompile(`(?:\([\p{Z}\t\r\n\f\v]*\)|\[[\p{Z}\t\r\n\f\v]*\]|\{[\p{Z}\t\r\n\f\v]*\})`)
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
	hadTokens := false
	for _, index := range indexes {
		if !completeToken(query, index[0], index[1]) {
			continue
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
		}
		hadTokens = true
	}

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
	if hadTokens {
		cleanQuery = emptyPairs.ReplaceAllString(cleanQuery, "")
		cleanQuery = strings.TrimSpace(cleanQuery)
		cleanQuery = strings.TrimFunc(cleanQuery, isNowEmptySeparator)
	}
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

func isNowEmptySeparator(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune("-–—_|/,:;.", r)
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
