# navidrome-mood-plugin

A [Navidrome](https://www.navidrome.org/) plugin that creates mood-based playlists using real audio analysis. It uses [essentia-tensorflow](https://essentia.upf.edu/) with Discogs-EffNet embeddings to analyze your music library for mood, energy, BPM, and danceability, then automatically generates and refreshes playlists like "Happy Mix", "Chill Mix", "Party Mix", and more.

Works with any Subsonic-compatible client (Symfonium, Sublime Music, etc.).

## Features

- **6 Mood Playlists** — Automatically creates and refreshes: Happy, Chill, Energetic, Melancholy, Party, Aggressive
- **Mood-Aware Instant Mix** — Replaces the default Instant Mix with mood-similarity matching (Euclidean distance across mood vectors)
- **Scheduled Analysis** — Periodically scans your library for new tracks and sends them to the analyzer
- **Scheduled Refresh** — Regenerates mood playlists on a cron schedule so they evolve as your library grows
- **Fully Configurable** — Mood thresholds, playlist sizes, analysis/refresh schedules, and analyzer URL are all configurable from Navidrome's plugin settings UI

## Quick Start

### 1. Start the Analyzer Service

The plugin needs an external service to perform audio analysis (essentia can't run inside WASM). A ready-to-use Docker image is included:

```bash
cd analyzer-service
docker build -t mood-analyzer .
docker run -d \
  --name mood-analyzer \
  -p 8000:8000 \
  -v /path/to/your/music:/music:ro \
  mood-analyzer
```

Or add it to your existing `docker-compose.yml`:

```yaml
mood-analyzer:
  build:
    context: ./analyzer-service
  container_name: mood-analyzer
  volumes:
    - /path/to/your/music:/music:ro
  restart: unless-stopped
```

The music path must match what Navidrome sees — the analyzer reads the same audio files.

### 2. Install the Plugin

1. Download `mood-playlists.ndp` from [Releases](https://github.com/craiglush/navidrome-mood-plugin/releases) (or [build from source](#building-from-source))
2. Copy it to your Navidrome plugins directory: `<navidrome-data>/plugins/`
3. Restart Navidrome (or it auto-loads if `ND_PLUGINS_AUTORELOAD=true`)
4. Go to **Settings > Plugins > Mood Playlists** and approve permissions
5. Set the **Analyzer Service URL** to your analyzer (e.g., `http://mood-analyzer:8000`)

### 3. Done

The plugin will:
- Analyze unanalyzed tracks daily at 2 AM (configurable)
- Refresh mood playlists weekly on Sunday at 3 AM (configurable)
- Return mood-similar tracks when you use Instant Mix on any analyzed track

## Requirements

- **Navidrome** `develop` branch (plugin support requires 0.61.0+)
- **Docker** (for the analyzer service)
- Navidrome config:
  ```yaml
  ND_PLUGINS_ENABLED: "true"
  ND_PLUGINS_AUTORELOAD: "true"  # optional but recommended
  ```

## Analyzer Service

The analyzer service (`analyzer-service/`) is a lightweight FastAPI app that wraps essentia-tensorflow. It exposes a single endpoint:

```
POST /api/analysis/file
Content-Type: application/json

{"file_path": "/music/Artist/Album/Track.m4a"}
```

Response:
```json
{
  "file_path": "/music/Artist/Album/Track.m4a",
  "title": "Track Name",
  "artist": "Artist Name",
  "album": "Album Name",
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

The Docker image (~500MB) includes:
- `essentia-tensorflow` with pre-trained Discogs-EffNet embedding model
- 6 mood classification heads (happy, sad, relaxed, aggressive, party) + danceability
- BPM and energy extraction

You can also use any custom service that implements this API.

## Configuration

All settings are configurable from Navidrome's plugin settings UI:

| Setting | Default | Description |
|---------|---------|-------------|
| Analyzer Service URL | `http://mood-analyzer:8000` | URL of the mood analyzer HTTP service |
| Auto-Analyze | `true` | Automatically analyze new tracks on schedule |
| Analysis Schedule | `0 2 * * *` | Cron expression (default: 2 AM daily) |
| Playlist Refresh Schedule | `0 3 * * 0` | Cron expression (default: 3 AM Sundays) |
| Tracks per Playlist | `30` | Number of tracks in each mood playlist |
| Similar Songs Count | `20` | Tracks returned for Instant Mix |
| Happy Threshold | `0.55` | Minimum score (0-1) for happy classification |
| Chill Threshold | `0.55` | Minimum score for chill/relaxed |
| Energetic Threshold | `0.6` | Minimum score for energetic/danceable |
| Party Threshold | `0.55` | Minimum score for party |
| Melancholy Threshold | `0.45` | Minimum score for sad/melancholy |
| Aggressive Threshold | `0.45` | Minimum score for aggressive |

## How It Works

```
┌─────────────────────────────┐     ┌──────────────────────────┐
│     Navidrome (+ plugin)    │     │   mood-analyzer service   │
│                             │     │   (essentia-tensorflow)   │
│  mood-playlists.ndp         │────>│                           │
│  - scheduler: analyze new   │HTTP │  POST /api/analysis/file  │
│    tracks daily             │     │  -> mood scores + BPM     │
│  - kvstore: cache scores    │     │                           │
│  - subsonicapi: create      │     └──────────────────────────┘
│    playlists                │
│  - instant mix: mood-       │
│    similar tracks           │
└─────────────────────────────┘
```

1. **Analysis** — On schedule, the plugin iterates all tracks via Subsonic API, sends unanalyzed ones to the analyzer service, and stores mood scores in its KVStore.

2. **Playlists** — On the refresh schedule, it queries stored mood data, selects tracks above each mood's threshold (sorted by score), and creates playlists via the Subsonic API.

3. **Instant Mix** — When triggered on a track, calculates Euclidean distance between the source track's mood vector and all analyzed tracks, returning the closest matches.

## Building from Source

### With Docker (no local Go/TinyGo required)

```bash
make docker-build
```

### With Go 1.25+

```bash
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
zip mood-playlists.ndp plugin.wasm manifest.json
```

### With TinyGo (smaller binary)

```bash
tinygo build -opt=2 -scheduler=none -no-debug -o plugin.wasm -target wasip1 -buildmode=c-shared .
zip mood-playlists.ndp plugin.wasm manifest.json
```

## Contributing

Contributions welcome! Some ideas:

- [ ] Web UI for triggering analysis on demand
- [ ] Per-user mood playlists
- [ ] "Mood of the day" rotating playlist
- [ ] BPM-range playlists (workout, study, etc.)
- [ ] Integration with Last.fm listening history for personalized moods

## License

GPL-3.0 — same as Navidrome.
