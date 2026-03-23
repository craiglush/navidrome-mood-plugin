# navidrome-mood-plugin

A [Navidrome](https://www.navidrome.org/) plugin that creates mood-based playlists using audio analysis. It analyzes your music library for mood, energy, BPM, and danceability, then automatically generates and refreshes playlists like "Happy Mix", "Chill Mix", "Party Mix", and more.

## Features

- **Mood Playlists** — Automatically creates playlists for 6 moods: Happy, Chill, Energetic, Melancholy, Party, Aggressive
- **Instant Mix** — When you click "Instant Mix" on a track in Navidrome, returns tracks with similar mood profiles instead of the default algorithm
- **Scheduled Analysis** — Periodically scans your library for new tracks and analyzes them
- **Configurable** — Mood thresholds, playlist sizes, and schedules are all configurable from Navidrome's plugin settings UI

## Requirements

- Navidrome `develop` branch (0.61.0+ with plugin support)
- An external mood analyzer service running [essentia-tensorflow](https://essentia.upf.edu/) (see [Analyzer Service](#analyzer-service))

## Installation

1. Download the latest `mood-playlists.ndp` from [Releases](https://github.com/craiglush/navidrome-mood-plugin/releases)
2. Copy it to your Navidrome plugins directory (`<navidrome-data>/plugins/`)
3. Navidrome auto-loads the plugin (if `ND_PLUGINS_AUTORELOAD=true`)
4. Configure the analyzer URL in Navidrome → Settings → Plugins → Mood Playlists

## Analyzer Service

This plugin requires an external HTTP service that performs audio analysis. The service must expose:

```
POST /api/analysis/file
Body: {"file_path": "/music/Artist/Album/Track.m4a"}
Response: {
  "bpm": 120.0,
  "danceability": 0.75,
  "mood_happy": 0.82,
  "mood_sad": 0.15,
  "mood_relaxed": 0.45,
  "mood_aggressive": 0.10,
  "mood_party": 0.68,
  "energy": 0.55
}
```

The [Music Stack Manager](https://github.com/craiglush/music-stack) includes this analyzer service built-in using essentia-tensorflow with Discogs-EffNet models.

## Building from Source

### With TinyGo (recommended)

```bash
make package
```

### With Docker (no local Go required)

```bash
make docker-build
```

### Manual

```bash
tinygo build -opt=2 -scheduler=none -no-debug -o plugin.wasm -target wasip1 -buildmode=c-shared .
zip mood-playlists.ndp plugin.wasm manifest.json
```

## Configuration

All settings are configurable from Navidrome's plugin settings UI:

| Setting | Default | Description |
|---------|---------|-------------|
| Analyzer Service URL | `http://music-manager:5000` | URL of the mood analyzer HTTP service |
| Auto-Analyze | `true` | Automatically analyze new tracks on schedule |
| Analysis Schedule | `0 2 * * *` | Cron expression for analysis (default: 2 AM daily) |
| Playlist Refresh | `0 3 * * 0` | Cron expression for playlist refresh (default: 3 AM weekly) |
| Tracks per Playlist | `30` | Number of tracks in each mood playlist |
| Mood Thresholds | `0.45–0.6` | Minimum scores for each mood classification |

## How It Works

1. **Analysis**: The plugin periodically calls the analyzer service for each unanalyzed track. The analyzer uses essentia-tensorflow with Discogs-EffNet embeddings to extract mood scores, BPM, and danceability from the audio.

2. **Storage**: Mood scores are stored in the plugin's KVStore (persistent SQLite) keyed by Navidrome track ID.

3. **Playlists**: On the refresh schedule, the plugin queries its stored mood data, selects tracks above each mood's threshold, and creates/updates playlists via the Subsonic API.

4. **Instant Mix**: When a user triggers Instant Mix on a track, the plugin calculates Euclidean distance between the source track's mood vector and all analyzed tracks, returning the closest matches.

## License

GPL-3.0 — same as Navidrome.
