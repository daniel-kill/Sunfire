package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearchGeniusFiltersAndValidatesResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("q"); got != "Radiohead OK Computer" {
			t.Errorf("query = %q, want searched text", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"response": {
				"sections": [
					{"type":"album","hits":[
						{"result":{"title":"OK Computer","primary_artist_names":"Radiohead","url":"https://genius.com/albums/Radiohead/Ok-computer"}},
						{"result":{"title":"Unsafe URL","url":"https://example.com/album"}}
					]},
					{"type":"song","hits":[
						{"result":{"title":"Paranoid Android","artist_names":"Radiohead","url":"https://genius.com/Radiohead-paranoid-android-lyrics"}}
					]}
				]
			}
		}`))
	}))
	defer server.Close()

	results, err := searchGeniusWithClient(server.Client(), server.URL, "album", " Radiohead OK Computer ")
	if err != nil {
		t.Fatalf("searchGeniusWithClient returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want one valid album result", len(results))
	}
	if results[0].Title != "OK Computer" || results[0].PrimaryArtistNames != "Radiohead" {
		t.Errorf("unexpected result metadata: %+v", results[0])
	}
	if !strings.HasPrefix(results[0].URL, "https://genius.com/") {
		t.Errorf("result URL was not retained as a Genius HTTPS URL: %q", results[0].URL)
	}
}

func TestSearchGeniusRejectsBadInputsAndHTTPFailures(t *testing.T) {
	if _, err := searchGeniusWithClient(http.DefaultClient, "://bad-url", "song", "title"); err == nil {
		t.Fatal("expected invalid endpoint error")
	}
	if _, err := searchGeniusWithClient(http.DefaultClient, "https://genius.com", "lyrics", "title"); err == nil {
		t.Fatal("expected unsupported search type error")
	}
	if _, err := searchGeniusWithClient(http.DefaultClient, "https://genius.com", "song", " "); err == nil {
		t.Fatal("expected empty query error")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	if _, err := searchGeniusWithClient(server.Client(), server.URL, "song", "title"); err == nil {
		t.Fatal("expected HTTP status error")
	}
}

func TestFormatGeniusSearchResultsContainsMetadataAndNoLyrics(t *testing.T) {
	result := geniusSearchResult{
		Title:                 "Example Song",
		PrimaryArtistNames:    "Example Artist",
		ReleaseDateForDisplay: "January 1, 2020",
		URL:                   "https://genius.com/example-song-lyrics",
	}
	formatted := formatGeniusSearchResults("song", "Example Song", []geniusSearchResult{result})
	for _, want := range []string{"Example Song", "Example Artist", "January 1, 2020", result.URL, "metadata and links only"} {
		if !strings.Contains(formatted, want) {
			t.Errorf("formatted results %q do not contain %q", formatted, want)
		}
	}

	empty := formatGeniusSearchResults("album", "No Such Album", nil)
	if !strings.Contains(empty, "No albums found") {
		t.Errorf("empty result message = %q, want no-albums message", empty)
	}
}

func TestFormatGeniusSearchResultsFitsDiscordLimit(t *testing.T) {
	results := make([]geniusSearchResult, geniusResultLimit)
	for index := range results {
		results[index] = geniusSearchResult{
			Title:                 strings.Repeat("t", 300),
			PrimaryArtistNames:    strings.Repeat("a", 300),
			ReleaseDateForDisplay: strings.Repeat("d", 100),
			URL:                   "https://genius.com/" + strings.Repeat("x", 400),
		}
	}

	formatted := formatGeniusSearchResults("album", strings.Repeat("q", 300), results)
	if length := len([]rune(formatted)); length > geniusReplyLimit {
		t.Fatalf("formatted results have %d runes, maximum is %d", length, geniusReplyLimit)
	}
	if !strings.Contains(formatted, "Additional results omitted") {
		t.Fatal("expected oversized results to be clearly marked as omitted")
	}
}

func TestLyricsCommandIsPublicAndDefinesBothSearches(t *testing.T) {
	buildCommandSpecs()
	if !isPublicCommand("lyrics") {
		t.Fatal("/lyrics is not marked as a public command")
	}

	for _, spec := range commandSpecs {
		if spec.Command.Name != "lyrics" {
			continue
		}
		if len(spec.Command.Options) != 2 || spec.Command.Options[0].Name != "fetch-song" || spec.Command.Options[1].Name != "fetch-album" {
			t.Fatalf("/lyrics options do not define fetch-song and fetch-album: %+v", spec.Command.Options)
		}
		if !spec.Command.Options[0].Options[0].Required {
			t.Fatal("/lyrics fetch-song must require its search term")
		}
		albumOptions := spec.Command.Options[1].Options
		if len(albumOptions) != 2 || !albumOptions[0].Required || albumOptions[1].Required {
			t.Fatal("/lyrics fetch-album must require artist and make album optional")
		}
		return
	}
	t.Fatal("/lyrics was not registered")
}

func TestValidGeniusResultURL(t *testing.T) {
	for _, test := range []struct {
		url  string
		want bool
	}{
		{"https://genius.com/song", true},
		{"https://www.genius.com/song", true},
		{"http://genius.com/song", false},
		{"https://notgenius.com/song", false},
		{"https://genius.com.example.org/song", false},
	} {
		if got := validGeniusResultURL(test.url) != ""; got != test.want {
			t.Errorf("validGeniusResultURL(%q) accepted = %t, want %t", test.url, got, test.want)
		}
	}
}
