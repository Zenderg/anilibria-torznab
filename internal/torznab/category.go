package torznab

import (
	"strconv"
	"strings"
)

const (
	CategoryTVID    = 5000
	CategoryAnimeID = 5070
)

// Category is the standard Torznab category ID and its RSS display text.
type Category struct {
	ID   int
	Text string
}

var (
	CategoryTV    = Category{ID: CategoryTVID, Text: "TV"}
	CategoryAnime = Category{ID: CategoryAnimeID, Text: "TV > Anime"}
)

// CategoryFor maps a supported AniLiberty release type to Torznab.
func CategoryFor(releaseType ReleaseType) (Category, bool) {
	if releaseType == ReleaseDorama {
		return CategoryTV, true
	}
	if releaseType.known() {
		return CategoryAnime, true
	}
	return Category{}, false
}

// CategoryFilter represents Torznab parent-category selection semantics.
type CategoryFilter struct {
	all     bool
	tv      bool
	anime   bool
	present bool
}

// ParseCategoryFilter parses a comma-separated cat parameter. Nil means no
// filter; supported and unknown positive IDs may coexist.
func ParseCategoryFilter(raw *string) (CategoryFilter, error) {
	if raw == nil {
		return CategoryFilter{all: true}, nil
	}
	filter := CategoryFilter{present: true}
	parts := strings.Split(*raw, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || strings.HasPrefix(part, "+") || strings.HasPrefix(part, "-") {
			return CategoryFilter{}, &ParameterError{Parameter: ParameterCategory}
		}
		id, err := strconv.ParseUint(part, 10, 31)
		if err != nil || id == 0 {
			return CategoryFilter{}, &ParameterError{Parameter: ParameterCategory}
		}
		switch int(id) {
		case CategoryTVID:
			filter.tv = true
		case CategoryAnimeID:
			filter.anime = true
		}
	}
	return filter, nil
}

// Matches reports whether a result category is included by the filter.
func (f CategoryFilter) Matches(category Category) bool {
	if f.all {
		return true
	}
	switch category.ID {
	case CategoryTVID:
		return f.tv
	case CategoryAnimeID:
		return f.tv || f.anime
	default:
		return false
	}
}

// Present reports whether cat was supplied, including an all-unknown list.
func (f CategoryFilter) Present() bool {
	return f.present
}
