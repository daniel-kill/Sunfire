package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type tmdbNowPlayingResponse struct {
	Results []struct {
		ID          int     `json:"id"`
		Title       string  `json:"title"`
		Overview    string  `json:"overview"`
		ReleaseDate string  `json:"release_date"`
		VoteAverage float64 `json:"vote_average"`
		PosterPath  string  `json:"poster_path"`
	} `json:"results"`
}

var (
	moviesMu        sync.Mutex
	moviesCache     map[string]map[string][]string
	moviesCachePath string
)

func initMoviesCache() {
	moviesCachePath = dataPath("moviesThisWeekCache.json")
	moviesCache = make(map[string]map[string][]string)

	data, err := os.ReadFile(moviesCachePath)
	if err == nil {
		if err := json.Unmarshal(data, &moviesCache); err != nil {
			log.Printf("TMDB notifier: parse cache %s: %v", moviesCachePath, err)
			moviesCache = make(map[string]map[string][]string)
		}
	} else if !os.IsNotExist(err) {
		log.Printf("TMDB notifier: read cache %s: %v", moviesCachePath, err)
	}
	if moviesCache == nil {
		moviesCache = make(map[string]map[string][]string)
	}
}

func saveMoviesCache() error {
	if moviesCachePath == "" {
		return fmt.Errorf("movies cache path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(moviesCachePath), 0755); err != nil {
		return fmt.Errorf("creating movies cache directory: %w", err)
	}
	data, err := json.MarshalIndent(moviesCache, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding movies cache: %w", err)
	}
	if err := os.WriteFile(moviesCachePath, data, 0644); err != nil {
		return fmt.Errorf("writing movies cache %s: %w", moviesCachePath, err)
	}
	return nil
}

func checkMoviesUpdates(s *discordgo.Session, cfg *Config) {
	if cfg.TMDBAPIKey == "" {
		return
	}

	guilds := loadGuildSettings()
	moviesMu.Lock()
	defer moviesMu.Unlock()

	client := notifierClient(15 * time.Second)
	query := url.Values{
		"api_key":  {cfg.TMDBAPIKey},
		"language": {"en-US"},
		"page":     {"1"},
	}
	apiURL := "https://api.themoviedb.org/3/movie/now_playing?" + query.Encode()

	resp, err := client.Get(apiURL)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		log.Printf("TMDB notifier: fetch now-playing request: %v", err)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("TMDB notifier: read now-playing response: %v", err)
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("TMDB notifier: now-playing request returned HTTP %d", resp.StatusCode)
		return
	}
	var tmdbResp tmdbNowPlayingResponse
	if err := json.Unmarshal(body, &tmdbResp); err != nil {
		log.Printf("TMDB notifier: decode now-playing response: %v", err)
		return
	}

	for guildID, gCfg := range guilds {
		if gCfg.MoviesThisWeekChannelID == "" {
			continue
		}

		if moviesCache[guildID] == nil {
			moviesCache[guildID] = make(map[string][]string)
		}
		seenList := moviesCache[guildID]["seenMovieIds"]
		seenMap := make(map[string]bool)
		for _, id := range seenList {
			seenMap[id] = true
		}

		var newEmbeds []*discordgo.MessageEmbed
		var newMovieIDs []string
		for _, m := range tmdbResp.Results {
			idStr := fmt.Sprintf("%d", m.ID)
			if m.ID == 0 || seenMap[idStr] {
				continue
			}

			embed := &discordgo.MessageEmbed{
				Title:       fmt.Sprintf("🍿 %s (%s)", m.Title, m.ReleaseDate),
				Description: m.Overview,
				Color:       0xE50914,
				Fields: []*discordgo.MessageEmbedField{
					{Name: "Rating", Value: fmt.Sprintf("⭐ %.1f / 10", m.VoteAverage), Inline: true},
				},
			}
			if m.PosterPath != "" {
				embed.Thumbnail = &discordgo.MessageEmbedThumbnail{
					URL: fmt.Sprintf("https://image.tmdb.org/t/p/w500%s", m.PosterPath),
				}
			}
			newEmbeds = append(newEmbeds, embed)
			newMovieIDs = append(newMovieIDs, idStr)
			if len(newEmbeds) >= 5 {
				break
			}
		}

		if len(newEmbeds) > 0 {
			header := "**🎬 In Theaters This Week:**"
			if gCfg.MoviesThisWeekRoleID != nil && *gCfg.MoviesThisWeekRoleID != "" {
				header = fmt.Sprintf("<@&%s> %s", *gCfg.MoviesThisWeekRoleID, header)
			}
			if _, err := sendNotifierComplex(s, gCfg.MoviesThisWeekChannelID, &discordgo.MessageSend{
				Content: header,
				Embeds:  newEmbeds,
			}); err != nil {
				log.Printf("TMDB notifier: send movie announcements to guild %s: %v", guildID, err)
				continue
			}

			seenList = append(seenList, newMovieIDs...)
			moviesCache[guildID]["seenMovieIds"] = seenList
			if err := saveMoviesCache(); err != nil {
				moviesCache[guildID]["seenMovieIds"] = append([]string(nil), seenList[:len(seenList)-len(newMovieIDs)]...)
				log.Printf("TMDB notifier: persist delivered movies for guild %s: %v", guildID, err)
			}
		}
	}
}

// startMoviesNotifierDaemon runs daily TMDB theatrical/streaming movie checks
func startMoviesNotifierDaemon(ctx context.Context, s *discordgo.Session) {
	initMoviesCache()
	cfg, err := loadConfig()
	if err != nil {
		log.Printf("TMDB notifier: load config: %v", err)
		return
	}
	if cfg.TMDBAPIKey == "" {
		log.Printf("TMDB notifier not started: TMDB API key is not configured")
		return
	}

	log.Printf("TMDB movies notifier daemon started (interval 24h)")
	runPeriodic(ctx, 45*time.Second, 24*time.Hour, func() { checkMoviesUpdates(s, cfg) })
}
