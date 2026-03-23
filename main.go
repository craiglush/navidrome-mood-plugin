// Package main implements a Navidrome plugin that provides mood-based playlists
// and similar song recommendations using audio analysis from an external service.
//
// The plugin:
// 1. Periodically scans the library for unanalyzed tracks
// 2. Sends them to an external analyzer service (essentia-tensorflow based)
// 3. Stores mood scores (happy, sad, relaxed, aggressive, party, danceability) in kvstore
// 4. Creates and refreshes mood-based playlists via the Subsonic API
// 5. Provides mood-similar songs for Instant Mix
//
//go:build wasip1

package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/metadata"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
)

// ── Types ────────────────────────────────────────────────────────

// MoodScores holds analysis results for a single track.
type MoodScores struct {
	MoodHappy      float64 `json:"mood_happy"`
	MoodSad        float64 `json:"mood_sad"`
	MoodRelaxed    float64 `json:"mood_relaxed"`
	MoodAggressive float64 `json:"mood_aggressive"`
	MoodParty      float64 `json:"mood_party"`
	Danceability   float64 `json:"danceability"`
	BPM            float64 `json:"bpm"`
	Energy         float64 `json:"energy"`
}

// AnalysisResponse is the JSON response from the analyzer service.
type AnalysisResponse struct {
	FilePath       string  `json:"file_path"`
	Title          string  `json:"title"`
	Artist         string  `json:"artist"`
	Album          string  `json:"album"`
	BPM            float64 `json:"bpm"`
	Danceability   float64 `json:"danceability"`
	MoodHappy      float64 `json:"mood_happy"`
	MoodSad        float64 `json:"mood_sad"`
	MoodRelaxed    float64 `json:"mood_relaxed"`
	MoodAggressive float64 `json:"mood_aggressive"`
	MoodParty      float64 `json:"mood_party"`
	Energy         float64 `json:"energy"`
}

// MoodDefinition defines a mood playlist configuration.
type MoodDefinition struct {
	Key            string
	Label          string
	ScoreField     string
	ThresholdKey   string
	DefaultThresh  float64
}

// SubsonicSearchResult represents a song from Subsonic search3 response.
type SubsonicSearchResult struct {
	SearchResult3 struct {
		Song []struct {
			ID     string `json:"id"`
			Title  string `json:"title"`
			Artist string `json:"artist"`
			Album  string `json:"album"`
		} `json:"song"`
	} `json:"searchResult3"`
}

// SubsonicResponse wraps a generic Subsonic API response.
type SubsonicResponse struct {
	SubsonicResponse struct {
		Status string          `json:"status"`
		Error  json.RawMessage `json:"error,omitempty"`
	} `json:"subsonic-response"`
}

// ── Constants ────────────────────────────────────────────────────

var moods = []MoodDefinition{
	{Key: "happy", Label: "Happy Mix", ScoreField: "mood_happy", ThresholdKey: "happy_threshold", DefaultThresh: 0.55},
	{Key: "chill", Label: "Chill Mix", ScoreField: "mood_relaxed", ThresholdKey: "chill_threshold", DefaultThresh: 0.55},
	{Key: "energetic", Label: "Energetic Mix", ScoreField: "danceability", ThresholdKey: "energetic_threshold", DefaultThresh: 0.6},
	{Key: "melancholy", Label: "Melancholy Mix", ScoreField: "mood_sad", ThresholdKey: "melancholy_threshold", DefaultThresh: 0.45},
	{Key: "party", Label: "Party Mix", ScoreField: "mood_party", ThresholdKey: "party_threshold", DefaultThresh: 0.55},
	{Key: "aggressive", Label: "Aggressive Mix", ScoreField: "mood_aggressive", ThresholdKey: "aggressive_threshold", DefaultThresh: 0.45},
}

// ── Plugin Implementation ────────────────────────────────────────

type moodPlugin struct{}

func main() {
	metadata.Register(&moodPlugin{})
}

// ── Initialization ───────────────────────────────────────────────

//go:wasmexport nd_on_init
func onInit() int32 {
	pdk.Log(pdk.LogInfo, "Mood Playlists plugin initializing...")

	autoAnalyze := configBool("auto_analyze", true)
	if autoAnalyze {
		schedule := configString("analyze_schedule", "0 2 * * *")
		_, err := host.SchedulerScheduleRecurring(schedule, "analyze", "mood-analyze")
		if err != nil {
			pdk.Log(pdk.LogError, "Failed to schedule analysis: "+err.Error())
		} else {
			pdk.Log(pdk.LogInfo, "Scheduled analysis task: "+schedule)
		}
	}

	refreshSchedule := configString("playlist_refresh_schedule", "0 3 * * 0")
	_, err := host.SchedulerScheduleRecurring(refreshSchedule, "refresh-playlists", "mood-refresh")
	if err != nil {
		pdk.Log(pdk.LogError, "Failed to schedule playlist refresh: "+err.Error())
	} else {
		pdk.Log(pdk.LogInfo, "Scheduled playlist refresh: "+refreshSchedule)
	}

	pdk.Log(pdk.LogInfo, "Mood Playlists plugin initialized")
	return 0
}

// ── Scheduled Task Handler ───────────────────────────────────────

//go:wasmexport nd_on_schedule
func onSchedule() int32 {
	payload := string(pdk.Input())
	pdk.Log(pdk.LogInfo, "Scheduled task triggered: "+payload)

	switch payload {
	case "analyze":
		return runAnalysis()
	case "refresh-playlists":
		return refreshPlaylists()
	default:
		pdk.Log(pdk.LogWarn, "Unknown schedule payload: "+payload)
		return 0
	}
}

// ── Similar Songs (Instant Mix) ──────────────────────────────────

// GetSimilarSongsByTrack returns tracks with similar mood profiles.
func (p *moodPlugin) GetSimilarSongsByTrack(req metadata.SimilarSongsByTrackRequest) (*metadata.SimilarSongsResponse, error) {
	count := int(req.Count)
	if count <= 0 {
		count = configInt("similar_songs_count", 20)
	}

	// Get the source track's mood scores from kvstore
	sourceKey := "mood:" + req.ID
	sourceData, err := host.KVStoreGet(sourceKey)
	if err != nil || len(sourceData) == 0 {
		pdk.Log(pdk.LogDebug, "No mood data for track "+req.ID+", cannot find similar songs")
		return &metadata.SimilarSongsResponse{}, nil
	}

	var sourceScores MoodScores
	if err := json.Unmarshal(sourceData, &sourceScores); err != nil {
		return nil, fmt.Errorf("failed to parse mood data: %w", err)
	}

	// Get all analyzed track IDs from the index
	indexData, err := host.KVStoreGet("mood:index")
	if err != nil || len(indexData) == 0 {
		return &metadata.SimilarSongsResponse{}, nil
	}

	var trackIndex map[string]string // id -> "title|artist"
	if err := json.Unmarshal(indexData, &trackIndex); err != nil {
		return &metadata.SimilarSongsResponse{}, nil
	}

	// Score similarity for each tracked song
	type scoredTrack struct {
		id       string
		name     string
		artist   string
		distance float64
	}

	var candidates []scoredTrack
	for id, info := range trackIndex {
		if id == req.ID {
			continue
		}

		data, err := host.KVStoreGet("mood:" + id)
		if err != nil || len(data) == 0 {
			continue
		}

		var scores MoodScores
		if err := json.Unmarshal(data, &scores); err != nil {
			continue
		}

		dist := moodDistance(sourceScores, scores)
		parts := strings.SplitN(info, "|", 2)
		name := parts[0]
		artist := ""
		if len(parts) > 1 {
			artist = parts[1]
		}

		candidates = append(candidates, scoredTrack{id: id, name: name, artist: artist, distance: dist})
	}

	// Sort by distance (most similar first)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].distance < candidates[j].distance
	})

	// Return top N
	limit := count
	if limit > len(candidates) {
		limit = len(candidates)
	}

	songs := make([]metadata.SongRef, limit)
	for i := 0; i < limit; i++ {
		songs[i] = metadata.SongRef{
			ID:     candidates[i].id,
			Name:   candidates[i].name,
			Artist: candidates[i].artist,
		}
	}

	return &metadata.SimilarSongsResponse{Songs: songs}, nil
}

// ── Analysis Logic ───────────────────────────────────────────────

func runAnalysis() int32 {
	analyzerURL := configString("analyzer_url", "http://music-manager:5000")
	pdk.Log(pdk.LogInfo, "Running mood analysis via "+analyzerURL)

	// Get all songs from the library via Subsonic API
	// Use getRandomSongs to sample the library in batches
	analyzed := 0
	offset := 0
	batchSize := 100

	for {
		uri := fmt.Sprintf("search3?query=&songCount=%d&songOffset=%d&albumCount=0&artistCount=0", batchSize, offset)
		respData, err := host.SubsonicAPICall(uri)
		if err != nil {
			pdk.Log(pdk.LogError, "Subsonic API search failed: "+err.Error())
			break
		}

		var result struct {
			SubsonicResponse struct {
				SearchResult3 struct {
					Song []struct {
						ID     string `json:"id"`
						Title  string `json:"title"`
						Artist string `json:"artist"`
						Album  string `json:"album"`
						Path   string `json:"path"`
					} `json:"song"`
				} `json:"searchResult3"`
			} `json:"subsonic-response"`
		}

		if err := json.Unmarshal(respData, &result); err != nil {
			pdk.Log(pdk.LogError, "Failed to parse search results: "+err.Error())
			break
		}

		songs := result.SubsonicResponse.SearchResult3.Song
		if len(songs) == 0 {
			break
		}

		for _, song := range songs {
			// Check if already analyzed
			key := "mood:" + song.ID
			existing, _ := host.KVStoreGet(key)
			if len(existing) > 0 {
				continue
			}

			// Call analyzer service
			scores, err := callAnalyzer(analyzerURL, song.Path)
			if err != nil {
				pdk.Log(pdk.LogDebug, "Analysis failed for "+song.Title+": "+err.Error())
				continue
			}

			// Store in kvstore
			data, _ := json.Marshal(scores)
			if err := host.KVStoreSet(key, data); err != nil {
				pdk.Log(pdk.LogWarn, "Failed to store mood data for "+song.Title)
				continue
			}

			// Update index
			updateIndex(song.ID, song.Title, song.Artist)
			analyzed++
		}

		offset += batchSize
		if len(songs) < batchSize {
			break
		}
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("Analysis complete: %d new tracks analyzed", analyzed))
	return 0
}

func callAnalyzer(baseURL, filePath string) (*MoodScores, error) {
	reqBody, _ := json.Marshal(map[string]string{"file_path": filePath})

	resp, err := host.HTTPSend(host.HTTPRequest{
		URL:     baseURL + "/api/analysis/file",
		Method:  "POST",
		Body:    string(reqBody),
		Headers: map[string]string{"Content-Type": "application/json"},
		Timeout: 120,
	})
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("analyzer returned status %d", resp.StatusCode)
	}

	var analysis AnalysisResponse
	if err := json.Unmarshal([]byte(resp.Body), &analysis); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &MoodScores{
		MoodHappy:      analysis.MoodHappy,
		MoodSad:        analysis.MoodSad,
		MoodRelaxed:    analysis.MoodRelaxed,
		MoodAggressive: analysis.MoodAggressive,
		MoodParty:      analysis.MoodParty,
		Danceability:   analysis.Danceability,
		BPM:            analysis.BPM,
		Energy:         analysis.Energy,
	}, nil
}

// ── Playlist Generation ──────────────────────────────────────────

func refreshPlaylists() int32 {
	pdk.Log(pdk.LogInfo, "Refreshing mood playlists...")
	trackCount := configInt("playlist_track_count", 30)

	// Load the full index
	indexData, err := host.KVStoreGet("mood:index")
	if err != nil || len(indexData) == 0 {
		pdk.Log(pdk.LogWarn, "No analyzed tracks found — run analysis first")
		return 0
	}

	var trackIndex map[string]string
	if err := json.Unmarshal(indexData, &trackIndex); err != nil {
		pdk.Log(pdk.LogError, "Failed to parse track index: "+err.Error())
		return 1
	}

	// Load all mood scores
	type trackWithScores struct {
		id     string
		name   string
		artist string
		scores MoodScores
	}

	var allTracks []trackWithScores
	for id, info := range trackIndex {
		data, err := host.KVStoreGet("mood:" + id)
		if err != nil || len(data) == 0 {
			continue
		}
		var scores MoodScores
		if err := json.Unmarshal(data, &scores); err != nil {
			continue
		}
		parts := strings.SplitN(info, "|", 2)
		name := parts[0]
		artist := ""
		if len(parts) > 1 {
			artist = parts[1]
		}
		allTracks = append(allTracks, trackWithScores{id: id, name: name, artist: artist, scores: scores})
	}

	if len(allTracks) == 0 {
		pdk.Log(pdk.LogWarn, "No mood scores available")
		return 0
	}

	// Create a playlist for each mood
	for _, mood := range moods {
		threshold := configFloat(mood.ThresholdKey, mood.DefaultThresh)

		// Filter and sort by the mood's score field
		var matching []trackWithScores
		for _, t := range allTracks {
			score := getMoodScore(t.scores, mood.ScoreField)
			if score >= threshold {
				matching = append(matching, t)
			}
		}

		// Sort by score descending
		sort.Slice(matching, func(i, j int) bool {
			return getMoodScore(matching[i].scores, mood.ScoreField) > getMoodScore(matching[j].scores, mood.ScoreField)
		})

		limit := trackCount
		if limit > len(matching) {
			limit = len(matching)
		}

		if limit == 0 {
			pdk.Log(pdk.LogDebug, "No tracks for "+mood.Label)
			continue
		}

		// Build songId params for createPlaylist
		songIDs := make([]string, limit)
		for i := 0; i < limit; i++ {
			songIDs[i] = matching[i].id
		}

		createPlaylist(mood.Label, songIDs)
	}

	pdk.Log(pdk.LogInfo, "Mood playlists refreshed")
	return 0
}

func createPlaylist(name string, songIDs []string) {
	params := "name=" + url.QueryEscape(name)
	for _, id := range songIDs {
		params += "&songId=" + url.QueryEscape(id)
	}

	_, err := host.SubsonicAPICall("createPlaylist?" + params)
	if err != nil {
		pdk.Log(pdk.LogError, "Failed to create playlist '"+name+"': "+err.Error())
		return
	}
	pdk.Log(pdk.LogInfo, fmt.Sprintf("Created playlist '%s' with %d tracks", name, len(songIDs)))
}

// ── Helpers ──────────────────────────────────────────────────────

func updateIndex(id, title, artist string) {
	indexData, _ := host.KVStoreGet("mood:index")
	var index map[string]string
	if len(indexData) > 0 {
		json.Unmarshal(indexData, &index)
	}
	if index == nil {
		index = make(map[string]string)
	}
	index[id] = title + "|" + artist
	data, _ := json.Marshal(index)
	host.KVStoreSet("mood:index", data)
}

func getMoodScore(scores MoodScores, field string) float64 {
	switch field {
	case "mood_happy":
		return scores.MoodHappy
	case "mood_sad":
		return scores.MoodSad
	case "mood_relaxed":
		return scores.MoodRelaxed
	case "mood_aggressive":
		return scores.MoodAggressive
	case "mood_party":
		return scores.MoodParty
	case "danceability":
		return scores.Danceability
	default:
		return 0
	}
}

func moodDistance(a, b MoodScores) float64 {
	return math.Sqrt(
		sq(a.MoodHappy-b.MoodHappy) +
			sq(a.MoodSad-b.MoodSad) +
			sq(a.MoodRelaxed-b.MoodRelaxed) +
			sq(a.MoodAggressive-b.MoodAggressive) +
			sq(a.MoodParty-b.MoodParty) +
			sq(a.Danceability-b.Danceability) +
			sq((a.BPM-b.BPM)/200), // Normalize BPM difference
	)
}

func sq(x float64) float64 { return x * x }

func configString(key, defaultVal string) string {
	val, err := host.ConfigGet(key)
	if err != nil || val == "" {
		return defaultVal
	}
	return val
}

func configInt(key string, defaultVal int) int {
	val, err := host.ConfigGetInt(key)
	if err != nil {
		return defaultVal
	}
	return int(val)
}

func configFloat(key string, defaultVal float64) float64 {
	val, err := host.ConfigGet(key)
	if err != nil || val == "" {
		return defaultVal
	}
	f, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return defaultVal
	}
	return f
}

func configBool(key string, defaultVal bool) bool {
	val, err := host.ConfigGet(key)
	if err != nil || val == "" {
		return defaultVal
	}
	return val == "true" || val == "1" || val == "yes"
}
