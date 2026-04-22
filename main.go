// Package main implements a Navidrome plugin that provides mood-based playlists
// and similar song recommendations using audio analysis from an external service.
//
//go:build wasip1

package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/metadata"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
)

// ── Types ────────────────────────────────────────────────────────

type MoodScores struct {
	MoodHappy      float64 `json:"mood_happy"`
	MoodSad        float64 `json:"mood_sad"`
	MoodRelaxed    float64 `json:"mood_relaxed"`
	MoodAggressive float64 `json:"mood_aggressive"`
	MoodParty      float64 `json:"mood_party"`
	Danceability   float64 `json:"danceability"`
	BPM            float64 `json:"bpm"`
	Energy         float64 `json:"energy"`
	Arousal        float64 `json:"arousal"`
	Valence        float64 `json:"valence"`
}

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
	Arousal        float64 `json:"arousal"`
	Valence        float64 `json:"valence"`
}

type trackWithScores struct {
	id, name, artist string
	scores           MoodScores
}

// Simple mood: single score field >= threshold, with optional exclusion conditions.
// A track is excluded if it matches ANY condition in Exclude.
type MoodDefinition struct {
	Key           string
	Label         string
	ScoreField    string
	ThresholdKey  string
	DefaultThresh float64
	Exclude       []Condition
}

// Composite mood: multiple conditions that must ALL be true
type Condition struct {
	Field string  // score field name (mood_happy, bpm, energy, etc.)
	Op    string  // ">=" or "<"
	Value float64 // threshold value
}

type CompositeMoodDefinition struct {
	Key        string
	Label      string
	Conditions []Condition
	SortField  string // which field to sort by (descending)
}

var moods = []MoodDefinition{
	{Key: "happy", Label: "Happy Mix", ScoreField: "mood_happy", ThresholdKey: "happy_threshold", DefaultThresh: 0.55,
		Exclude: []Condition{
			{Field: "mood_sad", Op: ">=", Value: 0.4},
		}},
	{Key: "chill", Label: "Chill Mix", ScoreField: "mood_relaxed", ThresholdKey: "chill_threshold", DefaultThresh: 0.55,
		Exclude: []Condition{
			{Field: "mood_aggressive", Op: ">=", Value: 0.35},
		}},
	{Key: "energetic", Label: "Energetic Mix", ScoreField: "danceability", ThresholdKey: "energetic_threshold", DefaultThresh: 0.6},
	{Key: "melancholy", Label: "Melancholy Mix", ScoreField: "mood_sad", ThresholdKey: "melancholy_threshold", DefaultThresh: 0.45,
		Exclude: []Condition{
			{Field: "mood_happy", Op: ">=", Value: 0.5},
		}},
	{Key: "party", Label: "Party Mix", ScoreField: "mood_party", ThresholdKey: "party_threshold", DefaultThresh: 0.55,
		Exclude: []Condition{
			{Field: "mood_sad", Op: ">=", Value: 0.4},
		}},
	{Key: "aggressive", Label: "Aggressive Mix", ScoreField: "mood_aggressive", ThresholdKey: "aggressive_threshold", DefaultThresh: 0.55,
		Exclude: []Condition{
			{Field: "mood_relaxed", Op: ">=", Value: 0.35},
			{Field: "mood_happy", Op: ">=", Value: 0.45},
		}},
}

var compositeMoods = []CompositeMoodDefinition{
	{
		// Calm, focused — exclude aggressive and party tracks; sort by most relaxed
		Key:   "study",
		Label: "Study Mix",
		Conditions: []Condition{
			{Field: "mood_aggressive", Op: "<", Value: 0.45},
			{Field: "mood_party", Op: "<", Value: 0.50},
		},
		SortField: "mood_relaxed",
	},
	{
		// High-energy movement — exclude slow/sad tracks; sort by most danceable
		Key:   "workout",
		Label: "Workout Mix",
		Conditions: []Condition{
			{Field: "mood_relaxed", Op: "<", Value: 0.60},
			{Field: "mood_sad", Op: "<", Value: 0.50},
		},
		SortField: "danceability",
	},
	{
		// Very quiet and calm — stricter aggressive/party exclusion than Study; sort by most relaxed
		Key:   "sleep",
		Label: "Sleep Mix",
		Conditions: []Condition{
			{Field: "mood_aggressive", Op: "<", Value: 0.30},
			{Field: "mood_party", Op: "<", Value: 0.35},
		},
		SortField: "mood_relaxed",
	},
	{
		// Upbeat driving — exclude aggressive and sad; sort by happiest
		Key:   "road_trip",
		Label: "Road Trip Mix",
		Conditions: []Condition{
			{Field: "mood_aggressive", Op: "<", Value: 0.40},
			{Field: "mood_sad", Op: "<", Value: 0.50},
		},
		SortField: "mood_happy",
	},
	{
		// Light and pleasant — exclude aggressive and sad; sort by happiest
		Key:   "cooking",
		Label: "Cooking Mix",
		Conditions: []Condition{
			{Field: "mood_aggressive", Op: "<", Value: 0.45},
			{Field: "mood_sad", Op: "<", Value: 0.45},
		},
		SortField: "mood_happy",
	},
	{
		// Relaxed table atmosphere — exclude aggressive; sort by most relaxed
		Key:   "dining",
		Label: "Dining Mix",
		Conditions: []Condition{
			{Field: "mood_aggressive", Op: "<", Value: 0.40},
		},
		SortField: "mood_relaxed",
	},
	{
		// Unobtrusive ambient — exclude aggressive and high-energy party; sort by most relaxed
		Key:   "background",
		Label: "Background Mix",
		Conditions: []Condition{
			{Field: "mood_aggressive", Op: "<", Value: 0.50},
			{Field: "mood_party", Op: "<", Value: 0.55},
		},
		SortField: "mood_relaxed",
	},
}

// ── Plugin Registration ──────────────────────────────────────────

type moodPlugin struct{}

func main() {
	metadata.Register(&moodPlugin{})
}

// ── Initialization ───────────────────────────────────────────────

//go:wasmexport nd_on_init
func onInit() int32 {
	pdk.Log(pdk.LogInfo, "Mood Playlists plugin initializing...")

	if configBool("auto_analyze", true) {
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

	if reanalyzePct := configInt("reanalyze_percent", 0); reanalyzePct > 0 || configBool("reanalyze_uncertain", true) {
		reanalyzeSchedule := configString("reanalyze_schedule", "0 4 1 * *")
		_, err := host.SchedulerScheduleRecurring(reanalyzeSchedule, "reanalyze", "mood-reanalyze")
		if err != nil {
			pdk.Log(pdk.LogError, "Failed to schedule re-analysis: "+err.Error())
		} else {
			pdk.Log(pdk.LogInfo, "Scheduled re-analysis task: "+reanalyzeSchedule)
		}
	}

	// Clear any stale tasks from previous runs, then ensure the queue exists
	host.TaskClearQueue("mood-analysis")
	if err := host.TaskCreateQueue("mood-analysis", host.QueueConfig{
		Concurrency: 4,
		MaxRetries:  3,
	}); err != nil {
		pdk.Log(pdk.LogDebug, "Task queue init: "+err.Error())
	}

	pdk.Log(pdk.LogInfo, "Mood Playlists plugin initialized")
	return 0
}

// ── Scheduled Task Handler ───────────────────────────────────────

//go:wasmexport nd_scheduler_callback
func onSchedule() int32 {
	raw := string(pdk.Input())
	pdk.Log(pdk.LogInfo, "Scheduled task triggered: "+raw)

	// Navidrome passes a JSON object: {"scheduleId":"...","payload":"...","isRecurring":true}
	var envelope struct {
		Payload string `json:"payload"`
	}
	payload := raw
	if err := json.Unmarshal([]byte(raw), &envelope); err == nil && envelope.Payload != "" {
		payload = envelope.Payload
	}

	switch payload {
	case "analyze":
		return runAnalysis()
	case "refresh-playlists":
		return refreshPlaylists()
	case "reanalyze":
		return runScheduledReanalysis()
	default:
		pdk.Log(pdk.LogWarn, "Unknown schedule payload: "+payload)
		return 0
	}
}

// ── Similar Songs (Instant Mix) ──────────────────────────────────

func (p *moodPlugin) GetSimilarSongsByTrack(req metadata.SimilarSongsByTrackRequest) (*metadata.SimilarSongsResponse, error) {
	count := int(req.Count)
	if count <= 0 {
		count = configInt("similar_songs_count", 20)
	}

	sourceKey := "mood:" + req.ID
	sourceData, ok, err := host.KVStoreGet(sourceKey)
	if err != nil || !ok || len(sourceData) == 0 {
		pdk.Log(pdk.LogDebug, "No mood data for track "+req.ID)
		return &metadata.SimilarSongsResponse{}, nil
	}

	var sourceScores MoodScores
	if err := json.Unmarshal(sourceData, &sourceScores); err != nil {
		return nil, fmt.Errorf("failed to parse mood data: %w", err)
	}

	indexData, ok, err := host.KVStoreGet("mood:index")
	if err != nil || !ok || len(indexData) == 0 {
		return &metadata.SimilarSongsResponse{}, nil
	}

	var trackIndex map[string]string
	if err := json.Unmarshal(indexData, &trackIndex); err != nil {
		return &metadata.SimilarSongsResponse{}, nil
	}

	type scoredTrack struct {
		id, name, artist string
		distance         float64
	}

	var candidates []scoredTrack
	for id, info := range trackIndex {
		if id == req.ID {
			continue
		}
		data, ok, err := host.KVStoreGet("mood:" + id)
		if err != nil || !ok || len(data) == 0 {
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

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].distance < candidates[j].distance
	})

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

type analysisTask struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Force  bool   `json:"force,omitempty"` // bypass the skip-if-already-analyzed check
}

// isUncertain returns true when no mood score is clearly dominant — the model
// could not confidently classify the track and a re-analysis may improve it.
func isUncertain(scores *MoodScores) bool {
	max := math.Max(scores.MoodHappy, math.Max(scores.MoodSad,
		math.Max(scores.MoodRelaxed, math.Max(scores.MoodAggressive, scores.MoodParty))))
	return max < 0.45 || scores.BPM == 0
}

// markUncertain records a track ID for future re-analysis.
// Stored as a JSON map in KV under "mood:reanalyze". Capped at 1000 entries.
// Concurrent writes from multiple workers are accepted — occasional lost IDs
// are harmless; they will be caught in a future analysis run.
func markUncertain(id string) {
	data, ok, _ := host.KVStoreGet("mood:reanalyze")
	var ids map[string]bool
	if ok && len(data) > 0 {
		json.Unmarshal(data, &ids)
	}
	if ids == nil {
		ids = make(map[string]bool)
	}
	if len(ids) >= 1000 {
		return
	}
	if ids[id] {
		return
	}
	ids[id] = true
	encoded, _ := json.Marshal(ids)
	host.KVStoreSet("mood:reanalyze", encoded)
}

func runAnalysis() int32 {
	// Queue as many batches as possible within the time budget.
	// Progress (offset) is persisted in KV store so each run continues where
	// the last one left off. Resets to 0 when the end of the library is reached.
	const batchSize = 500
	const deadline = 20 * time.Second

	offsetData, _, _ := host.KVStoreGet("analysis:offset")
	offset := 0
	if len(offsetData) > 0 {
		offset, _ = strconv.Atoi(string(offsetData))
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("Queuing tracks for mood analysis (offset %d)...", offset))

	start := time.Now()
	totalQueued := 0

	for {
		uri := fmt.Sprintf("search3?query=&songCount=%d&songOffset=%d&albumCount=0&artistCount=0", batchSize, offset)
		respStr, err := subsonicCall(uri)
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
						Path   string `json:"path"`
					} `json:"song"`
				} `json:"searchResult3"`
			} `json:"subsonic-response"`
		}

		if err := json.Unmarshal([]byte(respStr), &result); err != nil {
			pdk.Log(pdk.LogError, "Failed to parse search results: "+err.Error())
			break
		}

		songs := result.SubsonicResponse.SearchResult3.Song
		endOfLibrary := len(songs) == 0 || len(songs) < batchSize

		for _, song := range songs {
			// Skip tracks already analyzed — avoids unnecessary work when library is fully analyzed
			if existing, ok, _ := host.KVStoreGet("mood:" + song.ID); ok && len(existing) > 0 {
				continue
			}
			taskData, _ := json.Marshal(analysisTask{
				ID:     song.ID,
				Title:  song.Title,
				Artist: song.Artist,
			})
			if _, err := host.TaskEnqueue("mood-analysis", taskData); err != nil {
				pdk.Log(pdk.LogWarn, "Failed to queue "+song.Title+": "+err.Error())
				continue
			}
			totalQueued++
		}

		if endOfLibrary {
			// Reset offset for next full pass
			offset = 0
			host.KVStoreSet("analysis:offset", []byte("0"))
			pdk.Log(pdk.LogInfo, fmt.Sprintf("Reached end of library, queued %d new tracks — offset reset to 0", totalQueued))

			// Re-analysis phase — only runs when no new tracks were found (library fully analyzed)
			if totalQueued == 0 {
				queueReanalysis(start, deadline)
			}
			return 0
		}

		host.KVStoreSet("analysis:offset", []byte(strconv.Itoa(offset)))

		// Stop fetching if we are approaching the deadline
		if time.Since(start) >= deadline {
			break
		}
	}

	host.KVStoreSet("analysis:offset", []byte(strconv.Itoa(offset)))
	pdk.Log(pdk.LogInfo, fmt.Sprintf("Queued %d tracks (next offset: %d)", totalQueued, offset))
	return 0
}

// runScheduledReanalysis is the handler for the dedicated re-analysis schedule.
// Unlike the inline re-analysis in runAnalysis() (which only fires when there
// are no new tracks), this runs on its own cron and always processes uncertain
// tracks and the configured random percentage — ensuring scores improve even
// in libraries that receive frequent additions.
func runScheduledReanalysis() int32 {
	pdk.Log(pdk.LogInfo, "Running scheduled re-analysis...")
	const deadline = 20 * time.Second
	start := time.Now()
	queueReanalysis(start, deadline)
	return 0
}

// queueReanalysis handles two types of re-analysis after a full library pass:
//  1. Uncertain tracks — previously analyzed but with low-confidence scores
//  2. Random percentage — a configurable fraction of all tracks, for gradual score refresh
func queueReanalysis(start time.Time, deadline time.Duration) {
	// 1. Re-analyze uncertain tracks
	if configBool("reanalyze_uncertain", true) {
		data, ok, _ := host.KVStoreGet("mood:reanalyze")
		if ok && len(data) > 0 {
			var ids map[string]bool
			if err := json.Unmarshal(data, &ids); err == nil && len(ids) > 0 {
				requeued := 0
				for id := range ids {
					if time.Since(start) >= deadline {
						break
					}
					taskData, _ := json.Marshal(analysisTask{ID: id, Force: true})
					if _, err := host.TaskEnqueue("mood-analysis", taskData); err == nil {
						requeued++
					}
				}
				// Clear queue — successfully re-queued tracks will update their own status
				host.KVStoreSet("mood:reanalyze", []byte("{}"))
				pdk.Log(pdk.LogInfo, fmt.Sprintf("Queued %d uncertain tracks for re-analysis", requeued))
			}
		}
	}

	// 2. Random re-analysis percentage
	reanalyzePct := configInt("reanalyze_percent", 0)
	if reanalyzePct <= 0 || time.Since(start) >= deadline {
		return
	}

	indexData, ok, _ := host.KVStoreGet("mood:index")
	if !ok || len(indexData) == 0 {
		return
	}
	var index map[string]string // id → "title|artist"
	if err := json.Unmarshal(indexData, &index); err != nil {
		return
	}

	// Collect and shuffle all IDs
	allIDs := make([]string, 0, len(index))
	for id := range index {
		allIDs = append(allIDs, id)
	}
	rand.Shuffle(len(allIDs), func(i, j int) { allIDs[i], allIDs[j] = allIDs[j], allIDs[i] })

	sampleSize := len(allIDs) * reanalyzePct / 100
	requeued := 0
	for _, id := range allIDs[:sampleSize] {
		if time.Since(start) >= deadline {
			break
		}
		info := index[id]
		parts := strings.SplitN(info, "|", 2)
		title, artist := parts[0], ""
		if len(parts) > 1 {
			artist = parts[1]
		}
		taskData, _ := json.Marshal(analysisTask{ID: id, Title: title, Artist: artist, Force: true})
		if _, err := host.TaskEnqueue("mood-analysis", taskData); err == nil {
			requeued++
		}
	}
	pdk.Log(pdk.LogInfo, fmt.Sprintf("Queued %d tracks for random re-analysis (%d%% of library)", requeued, reanalyzePct))
}

// ── Task Executor ─────────────────────────────────────────────────

//go:wasmexport nd_task_execute
func onTaskExecute() int32 {
	raw := pdk.Input()

	// Navidrome wraps the payload in a JSON envelope: {"taskId":"...","payload":"base64...","attempt":1}
	var envelope struct {
		Payload []byte `json:"payload"`
	}
	taskData := raw
	if err := json.Unmarshal(raw, &envelope); err == nil && len(envelope.Payload) > 0 {
		taskData = envelope.Payload
	}

	var task analysisTask
	if err := json.Unmarshal(taskData, &task); err != nil {
		pdk.Log(pdk.LogError, "Failed to parse task payload: "+err.Error())
		return 1
	}

	// Skip if already analyzed, unless this is a forced re-analysis task
	key := "mood:" + task.ID
	existing, ok, _ := host.KVStoreGet(key)
	if ok && len(existing) > 0 && !task.Force {
		return 0
	}

	analyzerURL := configString("analyzer_url", "http://mood-analyzer:8000")
	ndURL := configString("navidrome_url", "http://navidrome:4533")
	user := configString("navidrome_user", "")
	pass := configString("navidrome_password", "")
	streamURL := subsonicStreamURL(ndURL, user, pass, task.ID)

	scores, err := callAnalyzerURL(analyzerURL, streamURL)
	if err != nil {
		pdk.Log(pdk.LogWarn, fmt.Sprintf("Analysis failed for %s: %s", task.Title, err.Error()))
		return 1
	}

	data, _ := json.Marshal(scores)
	if err := host.KVStoreSet(key, data); err != nil {
		pdk.Log(pdk.LogWarn, "Failed to store mood data for "+task.Title)
		return 1
	}

	updateIndex(task.ID, task.Title, task.Artist)

	// Flag uncertain results for re-analysis on the next cycle
	if isUncertain(scores) {
		markUncertain(task.ID)
	}

	pdk.Log(pdk.LogInfo, "Analyzed: "+task.Title)
	return 0
}

func callAnalyzerURL(baseURL, streamURL string) (*MoodScores, error) {
	reqBody, _ := json.Marshal(map[string]string{"url": streamURL})

	resp, err := host.HTTPSend(host.HTTPRequest{
		URL:       baseURL + "/api/analysis/url",
		Method:    "POST",
		Body:      reqBody,
		Headers:   map[string]string{"Content-Type": "application/json"},
		TimeoutMs: 300000, // 5 min — download + TF analysis
	})
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("analyzer returned status %d", resp.StatusCode)
	}

	var analysis AnalysisResponse
	if err := json.Unmarshal(resp.Body, &analysis); err != nil {
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
		Arousal:        analysis.Arousal,
		Valence:        analysis.Valence,
	}, nil
}

func callAnalyzer(baseURL, filePath string) (*MoodScores, error) {
	reqBody, _ := json.Marshal(map[string]string{"file_path": filePath})

	resp, err := host.HTTPSend(host.HTTPRequest{
		URL:       baseURL + "/api/analysis/file",
		Method:    "POST",
		Body:      reqBody,
		Headers:   map[string]string{"Content-Type": "application/json"},
		TimeoutMs: 120000,
	})
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("analyzer returned status %d", resp.StatusCode)
	}

	var analysis AnalysisResponse
	if err := json.Unmarshal(resp.Body, &analysis); err != nil {
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
		Arousal:        analysis.Arousal,
		Valence:        analysis.Valence,
	}, nil
}

// ── Playlist Generation ──────────────────────────────────────────

// selectTracks picks up to limit tracks from a sorted candidate list,
// allowing at most maxPerArtist tracks from any single artist.
// An empty or blank artist name is treated as "unknown" and counted together.
// poolMultiplier controls weekly variation: the top limit*poolMultiplier tracks
// are shuffled before selection so each run draws from the same quality pool
// but produces a different playlist. Set to 1 to disable shuffling.
func selectTracks(sorted []trackWithScores, limit, maxPerArtist, poolMultiplier int) []string {
	// Build candidate pool from top-scoring tracks and shuffle for variation
	pool := sorted
	if poolMultiplier > 1 && len(sorted) > limit {
		poolSize := limit * poolMultiplier
		if poolSize > len(sorted) {
			poolSize = len(sorted)
		}
		candidates := make([]trackWithScores, poolSize)
		copy(candidates, sorted[:poolSize])
		rand.Shuffle(len(candidates), func(i, j int) {
			candidates[i], candidates[j] = candidates[j], candidates[i]
		})
		pool = candidates
	}

	artistCount := make(map[string]int)
	seen := make(map[string]bool) // dedup by normalised title+artist
	var ids []string
	for _, t := range pool {
		if len(ids) >= limit {
			break
		}
		artist := strings.ToLower(strings.TrimSpace(t.artist))
		if artist == "" {
			artist = "__unknown__"
		}
		title := strings.ToLower(strings.TrimSpace(t.name))
		dupKey := title + "\x00" + artist
		if seen[dupKey] {
			continue
		}
		if maxPerArtist > 0 && artistCount[artist] >= maxPerArtist {
			continue
		}
		ids = append(ids, t.id)
		artistCount[artist]++
		seen[dupKey] = true
	}
	return ids
}

func refreshPlaylists() int32 {
	pdk.Log(pdk.LogInfo, "Refreshing mood playlists...")
	trackCount := configInt("playlist_track_count", 30)
	poolMultiplier := configInt("playlist_variation_pool", 3)
	existingIDs := getExistingPlaylistIDs()

	indexData, ok, err := host.KVStoreGet("mood:index")
	if err != nil || !ok || len(indexData) == 0 {
		pdk.Log(pdk.LogWarn, "No analyzed tracks found — run analysis first")
		return 0
	}

	var trackIndex map[string]string
	if err := json.Unmarshal(indexData, &trackIndex); err != nil {
		pdk.Log(pdk.LogError, "Failed to parse track index: "+err.Error())
		return 1
	}

	var allTracks []trackWithScores
	for id, info := range trackIndex {
		data, ok, err := host.KVStoreGet("mood:" + id)
		if err != nil || !ok || len(data) == 0 {
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

	maxPerArtist := configInt("max_tracks_per_artist", 3)

	// Simple moods (single field >= threshold)
	for _, mood := range moods {
		threshold := configFloat(mood.ThresholdKey, mood.DefaultThresh)

		var matching []trackWithScores
		for _, t := range allTracks {
			if getScore(t.scores, mood.ScoreField) < threshold {
				continue
			}
			excluded := false
			for _, ex := range mood.Exclude {
				val := getScore(t.scores, ex.Field)
				if ex.Op == ">=" && val >= ex.Value {
					excluded = true
					break
				}
				if ex.Op == "<" && val < ex.Value {
					excluded = true
					break
				}
			}
			if !excluded {
				matching = append(matching, t)
			}
		}

		sort.Slice(matching, func(i, j int) bool {
			return getScore(matching[i].scores, mood.ScoreField) > getScore(matching[j].scores, mood.ScoreField)
		})

		songIDs := selectTracks(matching, trackCount, maxPerArtist, poolMultiplier)
		if len(songIDs) == 0 {
			pdk.Log(pdk.LogDebug, "No tracks for "+mood.Label)
			continue
		}
		upsertPlaylist(mood.Label, songIDs, existingIDs)
	}

	// Composite moods (multiple conditions)
	for _, mood := range compositeMoods {
		var matching []trackWithScores
		for _, t := range allTracks {
			if matchesAllConditions(t.scores, mood.Conditions) {
				matching = append(matching, t)
			}
		}

		sort.Slice(matching, func(i, j int) bool {
			return getScore(matching[i].scores, mood.SortField) > getScore(matching[j].scores, mood.SortField)
		})

		songIDs := selectTracks(matching, trackCount, maxPerArtist, poolMultiplier)
		if len(songIDs) == 0 {
			pdk.Log(pdk.LogDebug, "No tracks for "+mood.Label)
			continue
		}
		upsertPlaylist(mood.Label, songIDs, existingIDs)
	}

	pdk.Log(pdk.LogInfo, "Mood playlists refreshed")
	return 0
}

func matchesAllConditions(scores MoodScores, conditions []Condition) bool {
	for _, c := range conditions {
		val := getScore(scores, c.Field)
		switch c.Op {
		case ">=":
			if val < c.Value {
				return false
			}
		case "<":
			if val >= c.Value {
				return false
			}
		}
	}
	return true
}

// getExistingPlaylistIDs returns a map of playlist name → id for all playlists
// visible to the configured user. Used by refreshPlaylists to update rather than
// duplicate when a mood playlist already exists.
func getExistingPlaylistIDs() map[string]string {
	result := map[string]string{}
	body, err := subsonicCall("getPlaylists.view?")
	if err != nil {
		pdk.Log(pdk.LogWarn, "getPlaylists failed: "+err.Error())
		return result
	}
	// Parse just what we need: {"subsonic-response":{"playlists":{"playlist":[{"id":"...","name":"..."}]}}}
	var resp struct {
		SubsonicResponse struct {
			Playlists struct {
				Playlist []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"playlist"`
			} `json:"playlists"`
		} `json:"subsonic-response"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		pdk.Log(pdk.LogWarn, "Failed to parse getPlaylists response: "+err.Error())
		return result
	}
	for _, pl := range resp.SubsonicResponse.Playlists.Playlist {
		result[pl.Name] = pl.ID
	}
	return result
}

// upsertPlaylist creates a new playlist or replaces the tracks in an existing one.
// existingIDs maps playlist name → id; when a match is found the playlist is
// updated in-place so Navidrome never accumulates duplicates.
func upsertPlaylist(name string, songIDs []string, existingIDs map[string]string) {
	var params string
	if id, ok := existingIDs[name]; ok {
		// Update existing playlist — replace all tracks
		params = "playlistId=" + url.QueryEscape(id) + "&name=" + url.QueryEscape(name)
	} else {
		// Create brand-new playlist
		params = "name=" + url.QueryEscape(name)
	}
	for _, id := range songIDs {
		params += "&songId=" + url.QueryEscape(id)
	}
	_, err := subsonicCall("createPlaylist?" + params)
	if err != nil {
		pdk.Log(pdk.LogError, "Failed to upsert playlist '"+name+"': "+err.Error())
		return
	}
	pdk.Log(pdk.LogInfo, fmt.Sprintf("Created playlist '%s' with %d tracks", name, len(songIDs)))
}

// ── Subsonic auth helpers ─────────────────────────────────────────

// subsonicTokenAuth returns token-auth query params (t= and s=) for the Subsonic API.
// This avoids embedding the raw password in URLs — t = MD5(password + salt).
func subsonicTokenAuth(pass string) (token, salt string) {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 10)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	salt = string(b)
	h := md5.Sum([]byte(pass + salt))
	token = hex.EncodeToString(h[:])
	return
}

// subsonicStreamURL builds a Subsonic stream URL using token auth instead of plain password.
func subsonicStreamURL(ndURL, user, pass, trackID string) string {
	t, s := subsonicTokenAuth(pass)
	return fmt.Sprintf("%s/rest/stream?id=%s&u=%s&t=%s&s=%s&v=1.16.1&c=mood-plugin",
		strings.TrimRight(ndURL, "/"),
		url.QueryEscape(trackID),
		url.QueryEscape(user),
		t, s)
}

// ── Subsonic HTTP helper (for scheduler context where no user is injected) ───

func subsonicCall(uri string) (string, error) {
	ndURL := configString("navidrome_url", "http://navidrome:4533")
	user := configString("navidrome_user", "")
	pass := configString("navidrome_password", "")

	if user == "" {
		// No credentials configured — fall back to host call (works in user context)
		return host.SubsonicAPICall(uri)
	}

	sep := "?"
	if strings.Contains(uri, "?") {
		sep = "&"
	}
	fullURL := fmt.Sprintf("%s/rest/%s%su=%s&p=%s&v=1.16.1&c=mood-plugin&f=json",
		ndURL, uri, sep, url.QueryEscape(user), url.QueryEscape(pass))

	resp, err := host.HTTPSend(host.HTTPRequest{
		URL:       fullURL,
		Method:    "GET",
		TimeoutMs: 30000,
	})
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return string(resp.Body), nil
}

// ── Helpers ──────────────────────────────────────────────────────

func updateIndex(id, title, artist string) {
	indexData, ok, _ := host.KVStoreGet("mood:index")
	var index map[string]string
	if ok && len(indexData) > 0 {
		json.Unmarshal(indexData, &index)
	}
	if index == nil {
		index = make(map[string]string)
	}
	index[id] = title + "|" + artist
	data, _ := json.Marshal(index)
	host.KVStoreSet("mood:index", data)
}

func getScore(scores MoodScores, field string) float64 {
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
	case "bpm":
		return scores.BPM
	case "energy":
		return scores.Energy
	case "arousal":
		return scores.Arousal
	case "valence":
		return scores.Valence
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
			sq(a.Arousal-b.Arousal) +
			sq(a.Valence-b.Valence) +
			sq((a.BPM-b.BPM)/200),
	)
}

func sq(x float64) float64 { return x * x }

func configString(key, defaultVal string) string {
	val, ok := host.ConfigGet(key)
	if !ok || val == "" {
		return defaultVal
	}
	return val
}

func configInt(key string, defaultVal int) int {
	val, ok := host.ConfigGetInt(key)
	if !ok {
		return defaultVal
	}
	return int(val)
}

func configFloat(key string, defaultVal float64) float64 {
	val, ok := host.ConfigGet(key)
	if !ok || val == "" {
		return defaultVal
	}
	f, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return defaultVal
	}
	return f
}

func configBool(key string, defaultVal bool) bool {
	val, ok := host.ConfigGet(key)
	if !ok || val == "" {
		return defaultVal
	}
	return val == "true" || val == "1" || val == "yes"
}
