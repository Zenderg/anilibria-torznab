package anilibria

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLatestCapsSourceWindowBeforeTorrentValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		firstInvalid bool
		wantCount    int
	}{
		{name: "all valid", wantCount: latestLimit},
		{name: "invalid item inside source window", firstInvalid: true, wantCount: latestLimit - 1},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var body strings.Builder
			body.WriteString(`{"data":[`)
			for index := 0; index <= latestLimit; index++ {
				if index > 0 {
					body.WriteByte(',')
				}
				hash := fmt.Sprintf("%040x", index+1)
				item := validTorrentJSON(hash)
				if test.firstInvalid && index == 0 {
					item = strings.Replace(item, `"size":123456`, `"size":-1`, 1)
				}
				body.WriteString(item)
			}
			body.WriteString(`]}`)

			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("Content-Type", "application/json")
				_, _ = response.Write([]byte(body.String()))
			}))
			defer server.Close()

			client := newTestClient(t, server.URL+"/", nil)
			torrents, err := client.Latest(context.Background())
			if err != nil {
				t.Fatalf("Latest() error = %v", err)
			}
			if len(torrents) != test.wantCount {
				t.Fatalf("torrent count = %d, want %d", len(torrents), test.wantCount)
			}
			excludedHash := fmt.Sprintf("%040x", latestLimit+1)
			for _, torrent := range torrents {
				if torrent.Hash == excludedHash {
					t.Fatalf("torrent outside the first %d source positions was returned", latestLimit)
				}
			}
		})
	}
}
