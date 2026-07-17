package torznab

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// ReleaseType is an AniLiberty release type used by the Torznab mapping.
type ReleaseType string

const (
	ReleaseTV      ReleaseType = "TV"
	ReleaseONA     ReleaseType = "ONA"
	ReleaseWEB     ReleaseType = "WEB"
	ReleaseOVA     ReleaseType = "OVA"
	ReleaseOAD     ReleaseType = "OAD"
	ReleaseMovie   ReleaseType = "MOVIE"
	ReleaseDorama  ReleaseType = "DORAMA"
	ReleaseSpecial ReleaseType = "SPECIAL"
)

const (
	maxSeason        = 999
	maxEpisode       = 9999
	regexpWhitespace = `[\p{Z}\t\r\n\f\v\x{0085}]`
)

var (
	distributorSuffix = regexp.MustCompile(`(?i)` + regexpWhitespace + `*-` + regexpWhitespace + `*(?:AniLiberty|AniLibria)(?:\.(?:TOP|TV))?` + regexpWhitespace + `*$`)
	explicitSeason    = regexp.MustCompile(`(?i)(?:Season|Series)` + regexpWhitespace + `+[0-9]+|S` + regexpWhitespace + `*[0-9]+`)
	seasonDigits      = regexp.MustCompile(`[0-9]+`)
	ordinalSeason     = regexp.MustCompile(`(?i)[0-9]+(?:st|nd|rd|th)` + regexpWhitespace + `+Season`)
	ordinalParts      = regexp.MustCompile(`(?i)^([0-9]+)(st|nd|rd|th)`)
	trailingYear      = regexp.MustCompile(regexpWhitespace + `*(?:\([0-9]{4}\)|[0-9]{4})` + regexpWhitespace + `*$`)
	trailingRoman     = regexp.MustCompile(`(?i)(?:^|` + regexpWhitespace + `+)([IVXLCDM]+)$`)
	bracketGroup      = regexp.MustCompile(`\[([^\[\]]*)\]`)
	bareEpisode       = regexp.MustCompile(`^([0-9]+)(?:` + regexpWhitespace + `*[-–—]` + regexpWhitespace + `*([0-9]+))?$`)
	prefixedEpisode   = regexp.MustCompile(`(?i)^(?:E|EP)` + regexpWhitespace + `*([0-9]+)(?:` + regexpWhitespace + `*[-–—]` + regexpWhitespace + `*([0-9]+))?$`)
	singleEpisodeWord = regexp.MustCompile(`(?i)^Episode` + regexpWhitespace + `+([0-9]+)$`)
	rangeEpisodeWord  = regexp.MustCompile(`(?i)^Episodes` + regexpWhitespace + `+([0-9]+)` + regexpWhitespace + `*[-–—]` + regexpWhitespace + `*([0-9]+)$`)
)

// EpisodeRange is an inclusive episode range. Equal bounds represent one episode.
type EpisodeRange struct {
	Start int
	End   int
}

// TitleMetadata is the shared parsed representation used for title rendering
// and season/episode filtering.
type TitleMetadata struct {
	Season   *int
	Episodes *EpisodeRange
	Title    string
}

// ParseTitle parses a torrent label and renders its deterministic RSS title.
func ParseTitle(releaseType ReleaseType, mainName, label string) (TitleMetadata, error) {
	mainName = strings.TrimSpace(mainName)
	label = strings.TrimSpace(label)
	if mainName == "" {
		return TitleMetadata{}, errors.New("main name is required")
	}
	if label == "" {
		return TitleMetadata{}, errors.New("label is required")
	}
	if !releaseType.known() {
		return TitleMetadata{}, fmt.Errorf("unsupported release type %q", releaseType)
	}

	semantic := label
	if bracket := strings.IndexByte(semantic, '['); bracket >= 0 {
		semantic = semantic[:bracket]
	}
	semantic = strings.TrimSpace(distributorSuffix.ReplaceAllString(semantic, ""))
	if semantic == "" {
		return TitleMetadata{}, errors.New("semantic label title is required")
	}

	season := parseSeason(semantic)
	episodes := parseEpisodes(label)
	if releaseType == ReleaseMovie {
		if episodes == nil {
			season = nil
		} else if season == nil {
			season = intPointer(1)
		}
	} else if season == nil {
		season = intPointer(1)
	}

	title := semantic
	if season != nil {
		title += fmt.Sprintf(" S%02d", *season)
	}
	if episodes != nil {
		title += fmt.Sprintf("E%02d", episodes.Start)
		if episodes.End != episodes.Start {
			title += fmt.Sprintf("-E%02d", episodes.End)
		}
	}
	title += " RUS / " + mainName + " / " + label

	return TitleMetadata{Season: season, Episodes: episodes, Title: title}, nil
}

// Matches reports whether metadata satisfies the optional effective filters.
func (m TitleMetadata) Matches(season, episode *int) bool {
	if season != nil && (m.Season == nil || *m.Season != *season) {
		return false
	}
	if episode != nil {
		return m.Episodes != nil && *episode >= m.Episodes.Start && *episode <= m.Episodes.End
	}
	return true
}

func (r ReleaseType) known() bool {
	switch r {
	case ReleaseTV, ReleaseONA, ReleaseWEB, ReleaseOVA, ReleaseOAD, ReleaseMovie, ReleaseDorama, ReleaseSpecial:
		return true
	default:
		return false
	}
}

func parseSeason(semantic string) *int {
	for _, index := range explicitSeason.FindAllStringIndex(semantic, -1) {
		if !completeToken(semantic, index[0], index[1]) {
			continue
		}
		digits := seasonDigits.FindString(semantic[index[0]:index[1]])
		value, valid := boundedPositiveInteger(digits, maxSeason)
		if valid {
			return intPointer(value)
		}
		// A recognized but unsafe explicit season must not be replaced with a
		// lower-precedence guess.
		return nil
	}

	for _, index := range ordinalSeason.FindAllStringIndex(semantic, -1) {
		if !completeToken(semantic, index[0], index[1]) {
			continue
		}
		parts := ordinalParts.FindStringSubmatch(semantic[index[0]:index[1]])
		value, valid := boundedPositiveInteger(parts[1], maxSeason)
		if !valid {
			return nil
		}
		if strings.EqualFold(parts[2], englishOrdinalSuffix(value)) {
			return intPointer(value)
		}
	}

	withoutYear := strings.TrimSpace(trailingYear.ReplaceAllString(semantic, ""))
	match := trailingRoman.FindStringSubmatch(withoutYear)
	if len(match) == 2 {
		if value, ok := romanSeason(strings.ToUpper(match[1])); ok {
			return intPointer(value)
		}
	}
	return nil
}

func parseEpisodes(label string) *EpisodeRange {
	groups := bracketGroup.FindAllStringSubmatch(label, -1)
	for i := len(groups) - 1; i >= 0; i-- {
		content := strings.TrimSpace(groups[i][1])
		var parts []string
		switch {
		case bareEpisode.MatchString(content):
			parts = bareEpisode.FindStringSubmatch(content)
		case prefixedEpisode.MatchString(content):
			parts = prefixedEpisode.FindStringSubmatch(content)
		case singleEpisodeWord.MatchString(content):
			parts = singleEpisodeWord.FindStringSubmatch(content)
		case rangeEpisodeWord.MatchString(content):
			parts = rangeEpisodeWord.FindStringSubmatch(content)
		default:
			continue
		}

		start, valid := boundedPositiveInteger(parts[1], maxEpisode)
		if !valid {
			continue
		}
		end := start
		if len(parts) > 2 && parts[2] != "" {
			end, valid = boundedPositiveInteger(parts[2], maxEpisode)
			if !valid || end < start {
				continue
			}
		}
		return &EpisodeRange{Start: start, End: end}
	}
	return nil
}

func completeToken(value string, start, end int) bool {
	if start > 0 {
		previous, _ := lastRune(value[:start])
		if unicode.IsLetter(previous) || unicode.IsDigit(previous) {
			return false
		}
	}
	if end < len(value) {
		next, _ := firstRune(value[end:])
		if unicode.IsLetter(next) || unicode.IsDigit(next) {
			return false
		}
	}
	return true
}

func boundedPositiveInteger(raw string, maximum int) (int, bool) {
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 || value > uint64(maximum) {
		return 0, false
	}
	return int(value), true
}

func englishOrdinalSuffix(value int) string {
	if lastTwo := value % 100; lastTwo >= 11 && lastTwo <= 13 {
		return "th"
	}
	switch value % 10 {
	case 1:
		return "st"
	case 2:
		return "nd"
	case 3:
		return "rd"
	default:
		return "th"
	}
}

func romanSeason(value string) (int, bool) {
	canonical := [...]string{
		"", "I", "II", "III", "IV", "V", "VI", "VII", "VIII", "IX", "X",
		"XI", "XII", "XIII", "XIV", "XV", "XVI", "XVII", "XVIII", "XIX", "XX",
	}
	for number := 1; number < len(canonical); number++ {
		if value == canonical[number] {
			return number, true
		}
	}
	return 0, false
}

func intPointer(value int) *int {
	return &value
}
