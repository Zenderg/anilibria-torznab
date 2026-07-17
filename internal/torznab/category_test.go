package torznab

import (
	"errors"
	"testing"
)

func TestCategoryMappingAndFiltering(t *testing.T) {
	t.Parallel()
	for _, releaseType := range []ReleaseType{ReleaseTV, ReleaseONA, ReleaseWEB, ReleaseOVA, ReleaseOAD, ReleaseMovie, ReleaseSpecial} {
		category, ok := CategoryFor(releaseType)
		if !ok || category != CategoryAnime {
			t.Errorf("CategoryFor(%s) = %#v, %v", releaseType, category, ok)
		}
	}
	if category, ok := CategoryFor(ReleaseDorama); !ok || category != CategoryTV {
		t.Fatalf("CategoryFor(DORAMA) = %#v, %v", category, ok)
	}

	tests := []struct {
		raw         *string
		wantTV      bool
		wantAnime   bool
		wantPresent bool
	}{
		{nil, true, true, false},
		{stringPointer("5000"), true, true, true},
		{stringPointer("5070"), false, true, true},
		{stringPointer("5000,5070,9999"), true, true, true},
		{stringPointer("9999"), false, false, true},
		{stringPointer("2147483648"), false, false, true},
		{stringPointer("18446744073709551615"), false, false, true},
		{stringPointer("999999999999999999999999999999999999999999"), false, false, true},
	}
	for _, test := range tests {
		filter, err := ParseCategoryFilter(test.raw)
		if err != nil {
			t.Fatal(err)
		}
		if filter.Matches(CategoryTV) != test.wantTV || filter.Matches(CategoryAnime) != test.wantAnime || filter.Present() != test.wantPresent {
			t.Errorf("ParseCategoryFilter(%v) returned unexpected filter", test.raw)
		}
	}
}

func TestCategoryFilterRejectsMalformedLists(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"", ",", "5000,", "abc", "-1", "+5000"} {
		_, err := ParseCategoryFilter(&raw)
		var parameterError *ParameterError
		if !errors.As(err, &parameterError) || parameterError.Parameter != ParameterCategory {
			t.Errorf("ParseCategoryFilter(%q) error = %v", raw, err)
		}
	}
}
