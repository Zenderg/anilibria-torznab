package anilibria

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"
)

const (
	liveDefaultBase = "https://anilibria.top/api/v1/"
	liveMirrorBase  = "https://api.anilibria.app/api/v1/"
)

// TestLiveAniLibertySmoke is an explicit revalidation tool, not an ordinary
// unit test. It only performs read-only requests when the operator opts in.
func TestLiveAniLibertySmoke(t *testing.T) {
	if os.Getenv("ANILIBRIA_LIVE_SMOKE") != "1" {
		t.Skip("set ANILIBRIA_LIVE_SMOKE=1 to run read-only AniLiberty smoke requests")
	}

	observations := observeConfiguredLiveEndpoints(t, observeLiveEndpoint)
	primary, hasPrimary := observations["default"]
	mirror, hasMirror := observations["mirror"]
	if !hasPrimary && !hasMirror {
		return
	}
	if !hasPrimary || !hasMirror {
		t.Fatal("live smoke configuration must select either one custom endpoint or both official endpoints")
	}
	if !slices.Equal(primary.releaseIDs, mirror.releaseIDs) {
		t.Errorf("default and mirror release search IDs differ: default=%v mirror=%v", primary.releaseIDs, mirror.releaseIDs)
	}
	if !slices.Equal(primary.releaseHashes, mirror.releaseHashes) {
		t.Errorf("default and mirror release torrent hashes differ: default=%v mirror=%v", primary.releaseHashes, mirror.releaseHashes)
	}
}

type liveEndpoint struct {
	name string
	base string
}

type liveObservation struct {
	releaseIDs    []ReleaseID
	releaseHashes []string
}

func liveSmokeEndpoints() []liveEndpoint {
	if selectedBase := os.Getenv("ANILIBRIA_LIVE_BASE_URL"); selectedBase != "" {
		return []liveEndpoint{{name: "selected", base: selectedBase}}
	}

	defaultBase := firstEnvironmentValue("ANILIBRIA_LIVE_DEFAULT_BASE_URL", "ANILIBRIA_API_BASE_URL")
	if defaultBase == "" {
		defaultBase = liveDefaultBase
	}
	mirrorBase := os.Getenv("ANILIBRIA_LIVE_MIRROR_BASE_URL")
	if mirrorBase == "" {
		mirrorBase = liveMirrorBase
	}

	return []liveEndpoint{
		{"default", defaultBase},
		{"mirror", mirrorBase},
	}
}

func observeConfiguredLiveEndpoints(
	t *testing.T,
	observe func(*testing.T, string) liveObservation,
) map[string]liveObservation {
	t.Helper()
	endpoints := liveSmokeEndpoints()
	observations := make(map[string]liveObservation, len(endpoints))
	for _, endpoint := range endpoints {
		t.Run(endpoint.name, func(t *testing.T) {
			observations[endpoint.name] = observe(t, endpoint.base)
		})
	}

	return observations
}

func TestLiveSmokeUsesSelectedBaseURL(t *testing.T) {
	const selectedBase = "https://selected.invalid/api/v1/"
	t.Setenv("ANILIBRIA_LIVE_BASE_URL", selectedBase)
	t.Setenv("ANILIBRIA_LIVE_DEFAULT_BASE_URL", "https://default.invalid/api/v1/")
	t.Setenv("ANILIBRIA_LIVE_MIRROR_BASE_URL", "https://mirror.invalid/api/v1/")

	var contacted []string
	observations := observeConfiguredLiveEndpoints(t, func(_ *testing.T, baseURL string) liveObservation {
		contacted = append(contacted, baseURL)
		return liveObservation{}
	})

	if !slices.Equal(contacted, []string{selectedBase}) {
		t.Fatalf("contacted endpoints = %v, want only %s", contacted, selectedBase)
	}
	if _, ok := observations["selected"]; !ok {
		t.Fatal("selected endpoint observation was not recorded")
	}
}

func observeLiveEndpoint(t *testing.T, baseURL string) liveObservation {
	t.Helper()
	client, err := NewClient(Config{
		BaseURL:          baseURL,
		Version:          "live-smoke",
		HTTPTimeout:      15 * time.Second,
		RequestInterval:  2100 * time.Millisecond,
		MaxConcurrency:   1,
		MaxResponseBytes: 8 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("construct live client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	releaseIDs, err := client.SearchReleases(ctx, "Naruto")
	if err != nil {
		t.Fatalf("search releases: %v", err)
	}
	if len(releaseIDs) == 0 {
		t.Fatal("search releases returned no IDs")
	}
	if !slices.Contains(releaseIDs, ReleaseID(413)) {
		t.Fatalf("search releases no longer contains validation release 413: %v", releaseIDs)
	}

	torrents, err := client.TorrentsByRelease(ctx, 413)
	if err != nil {
		t.Fatalf("load release 413 torrents: %v", err)
	}
	if len(torrents) == 0 {
		t.Fatal("release 413 returned no valid torrents")
	}
	hashes := make([]string, len(torrents))
	for index, torrent := range torrents {
		hashes[index] = torrent.Hash
	}
	slices.Sort(hashes)

	latest, err := client.Latest(ctx)
	if err != nil {
		t.Fatalf("load latest torrents: %v", err)
	}
	if len(latest) == 0 || len(latest) > latestLimit {
		t.Fatalf("latest valid torrent count = %d, want 1..%d", len(latest), latestLimit)
	}

	return liveObservation{
		releaseIDs:    append([]ReleaseID(nil), releaseIDs...),
		releaseHashes: hashes,
	}
}

func firstEnvironmentValue(names ...string) string {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}
