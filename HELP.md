# Mood Playlists Plugin — Setup & User Guide

This guide walks you through every step needed to get the Mood Playlists plugin working in Navidrome — from first install to fully populated playlists. Read it top to bottom the first time. The troubleshooting section at the end covers the most common problems and how to fix them.

---

## Table of Contents

1. [How It Works](#1-how-it-works)
2. [What You Need Before Starting](#2-what-you-need-before-starting)
3. [Step 1 — Enable Plugins in Navidrome](#3-step-1--enable-plugins-in-navidrome)
4. [Step 2 — Set Up the Analyzer Service](#4-step-2--set-up-the-analyzer-service)
5. [Step 3 — Install the Plugin](#5-step-3--install-the-plugin)
6. [Step 4 — Configure the Plugin](#6-step-4--configure-the-plugin)
7. [Step 5 — Run Your First Analysis](#7-step-5--run-your-first-analysis)
8. [Step 6 — Generate the Playlists](#8-step-6--generate-the-playlists)
9. [Understanding the Playlists](#9-understanding-the-playlists)
10. [Configuration Reference](#10-configuration-reference)
11. [Monitoring Analysis Progress](#11-monitoring-analysis-progress)
12. [Troubleshooting](#12-troubleshooting)
13. [Building from Source](#13-building-from-source)

---

## 1. How It Works

The plugin has two parts that work together:

```
Navidrome (+ Plugin)                    Analyzer Service (Docker)
┌──────────────────────────────┐        ┌─────────────────────────────┐
│  Analysis scheduler (nightly)│        │  FastAPI + essentia-         │
│  → queues new tracks         │──────> │  tensorflow                 │
│                              │  HTTP  │                             │
│  Background workers stream   │        │  1. Receives first 120s of  │
│  audio to analyzer           │        │     audio via HTTP stream   │
│                              │        │  2. Runs TensorFlow mood    │
│  Analyzer returns scores     │ <───── │     classification models   │
│  → stored in KV store        │  JSON  │  3. Returns mood scores     │
│                              │        └─────────────────────────────┘
│  Re-analysis scheduler       │
│  → re-queues uncertain tracks│
│  → re-queues random % of lib │
│                              │
│  Playlist refresh (weekly)   │
│  → reads scores from store   │
│  → builds 13 playlists       │
│  → updates via Subsonic API  │
└──────────────────────────────┘
```

**The analyzer service streams audio directly from Navidrome over HTTP — it does not need access to your music files.**

Each track's first 120 seconds is analyzed for: BPM, energy, danceability, and five mood scores (happy, sad, relaxed, aggressive, party). Results are stored inside Navidrome and used to build playlists and power Instant Mix.

The plugin runs **three independent schedules**:

| Schedule | Default | Purpose |
|---|---|---|
| Analysis | `0 2 * * *` | Queues any new unanalyzed tracks nightly |
| Re-analysis | `0 4 1 * *` | Re-queues uncertain tracks and a random % of the library monthly |
| Playlist Refresh | `0 3 * * 0` | Rebuilds all 13 playlists weekly |

---

## 2. What You Need Before Starting

| Requirement | Minimum version | Notes |
|-------------|----------------|-------|
| Navidrome | 0.61.0 | Plugin support requires this version |
| Docker | Any recent version | Required for the analyzer service |
| Docker Compose | V2 (`docker compose`) or V1 (`docker-compose`) | |

---

## 3. Step 1 — Enable Plugins in Navidrome

Add these settings to your Navidrome configuration before doing anything else.

**In docker-compose.yml (environment variables):**
```yaml
environment:
  ND_PLUGINS_ENABLED: "true"
  ND_PLUGINS_AUTORELOAD: "true"
```

**In navidrome.toml:**
```toml
PluginsEnabled = true
PluginsAutoReload = true
```

`PluginsAutoReload` makes Navidrome detect new or updated plugin files automatically without a full restart. It is optional but saves time during setup.

Restart Navidrome after making this change:
```bash
docker restart navidrome
```

---

## 4. Step 2 — Set Up the Analyzer Service

The analyzer service performs the actual audio analysis using TensorFlow models. It cannot run inside Navidrome's plugin sandbox, so it runs as a separate Docker container.

> **The analyzer does not need access to your music files.** It streams audio directly from Navidrome over HTTP.

### Add to your existing docker-compose.yml

This is the recommended approach if Navidrome is already running via Docker Compose. Open your `docker-compose.yml` and add the `mood-analyzer` service:

```yaml
services:

  navidrome:
    # ... your existing Navidrome configuration — do not change this ...

  mood-analyzer:
    build:
      context: ./analyzer-service
    container_name: mood-analyzer
    restart: unless-stopped
    logging:
      driver: "json-file"
      options:
        max-size: "20m"
        max-file: "3"
```

The `analyzer-service` folder (containing `Dockerfile` and `app.py`) must be in the same directory as your `docker-compose.yml`, or adjust the `context` path accordingly.

Then build and start the analyzer:
```bash
docker-compose up -d --build mood-analyzer
```

> **The first build takes several minutes.** The Dockerfile downloads ~500 MB of TensorFlow models. Subsequent builds use the Docker cache and are much faster.

### Verify the analyzer is running

```bash
docker exec mood-analyzer wget -qO- http://localhost:8000/health
```

Expected output:
```json
{"status": "ok", "models_available": true}
```

If `models_available` is `false`, the TensorFlow models did not download during the build. Check the build output for errors.

---

## 5. Step 3 — Install the Plugin

### Get the plugin file

Download `mood-playlists.ndp` from the [Releases page](https://github.com/RFLundgren/navidrome-mood-plugin/releases).

Or build it yourself — see [Building from Source](#13-building-from-source).

### Find your Navidrome data directory

The plugin file must go into a `plugins` folder inside your Navidrome data directory.

First, find where your Navidrome data is stored on the host:

```bash
docker inspect navidrome --format '{{ range .Mounts }}{{ .Source }} -> {{ .Destination }}{{ println }}{{ end }}'
```

Look for the mount that maps to `/data` (or wherever your Navidrome data volume points). That host path is where you need to place the plugin.

### Copy the plugin file

```bash
# Replace /path/to/navidrome/data with your actual host path
mkdir -p /path/to/navidrome/data/plugins
cp mood-playlists.ndp /path/to/navidrome/data/plugins/
```

If `PluginsAutoReload` is enabled, Navidrome detects the file automatically within a few seconds. Otherwise restart Navidrome:
```bash
docker restart navidrome
```

### Approve the plugin permissions

1. Log into Navidrome as an admin
2. Go to **Settings → Plugins**
3. Find **Mood Playlists** and expand it
4. Click **Approve** to grant permissions

The plugin needs these permissions to function:

| Permission | Why |
|------------|-----|
| HTTP | To send audio streams to the analyzer service |
| Library | To read track metadata |
| Subsonic API | To create and manage playlists |
| Scheduler | To run analysis and refresh on a schedule |
| Task Queue | To process tracks in the background without blocking |
| KV Store | To store mood scores for each track |
| Config | To read your settings |
| Users | To create playlists |

---

## 6. Step 4 — Configure the Plugin

Go to **Settings → Plugins → Mood Playlists → Configure**.

### Required settings — set these first

| Setting | What to enter |
|---------|---------------|
| **Navidrome URL** | The URL Navidrome is reachable at **from inside Docker**. Usually `http://navidrome:4533` if both containers share a Docker network. See note below. |
| **Navidrome Username** | Any Navidrome username. Admin is not required. Consider creating a dedicated account (e.g. `mood-plugin`) rather than using your personal login. |
| **Navidrome Password** | Password for the above account. Both fields are masked — the values are stored securely and never shown again after saving. |
| **Analyzer Service URL** | URL of the analyzer container. Usually `http://mood-analyzer:8000` if using the Docker Compose setup above. |

> **Important — do not use `localhost` for the Navidrome URL.**
> Inside a Docker container, `localhost` means that container itself, not your host machine. Use the container name (`http://navidrome:4533`) or your host's LAN IP address (`http://192.168.1.x:4533`) instead.

To test that Navidrome is reachable from the analyzer container:
```bash
docker exec mood-analyzer wget -qO- "http://navidrome:4533/rest/ping?v=1.16.1&c=test&f=json"
```
You should see `{"subsonic-response":{"status":"ok",...}}`.

### Optional settings

| Setting | Default | Description |
|---------|---------|-------------|
| Auto-Analyze | `true` | Whether to run analysis on the schedule below |
| Analysis Schedule | `0 2 * * *` | When to scan for and analyze unanalyzed tracks (2 AM daily) |
| Re-analyze Uncertain Tracks | `true` | Re-queue tracks with low-confidence scores on the re-analysis schedule |
| Random Re-analysis % | `0` | Percentage of library to randomly re-analyze each re-analysis run. Set to e.g. `5` to gradually refresh scores. |
| Re-analysis Schedule | `0 4 1 * *` | When to run the dedicated re-analysis pass (1st of month, 4 AM) |
| Playlist Refresh Schedule | `0 3 * * 0` | When to rebuild all playlists (3 AM every Sunday) |
| Tracks per Playlist | `50` | Maximum tracks in each playlist |
| Variation Pool | `3` | Weekly variation multiplier — playlists draw randomly from the top N × pool tracks |
| Max Tracks per Artist | `3` | Maximum tracks from any one artist in a playlist. Set to `0` for no limit. |
| Similar Songs Count | `20` | Tracks returned when using Instant Mix |

Save settings after making any changes.

---

## 7. Step 5 — Run Your First Analysis

The scheduler runs analysis automatically at 2 AM, but you will want to trigger it immediately on first setup.

### Trigger analysis manually

1. In the plugin settings, note the current server time:
   ```bash
   docker exec navidrome date
   ```
2. Set **Analysis Schedule** to fire 2 minutes from now. For example, if the time is `14:35`, set:
   ```
   37 14 * * *
   ```
3. Save settings and wait for that time to pass
4. Set the schedule back to `0 2 * * *` and save again

Alternatively, set it to `* * * * *` to fire every minute, wait one minute, then set it back.

### What happens during analysis

1. The plugin fetches all tracks from your library via the Subsonic API (in batches of 500)
2. Each track is added to a background task queue
3. Workers (4 concurrent by default) process the queue — each worker streams the first 120 seconds of the track's audio to the analyzer service
4. The analyzer extracts mood scores using TensorFlow models and returns them
5. Scores are saved in the plugin's KV store, keyed by track ID

### How long will it take?

Each track takes roughly 10–20 seconds to analyze on a typical server CPU. With 4 concurrent workers:

| Library size | Estimated time |
|-------------|----------------|
| 1,000 tracks | 45 min – 1.5 hours |
| 5,000 tracks | 4–8 hours |
| 10,000 tracks | 8–16 hours |

Analysis is **incremental** — already-analyzed tracks are skipped on every subsequent run. Once your library is fully analyzed, the nightly run only processes new additions and completes in minutes.

After each full library pass, the plugin also automatically re-queues any tracks that received low-confidence scores (where no mood exceeded 0.45). These get a second analysis attempt, which often produces better results. You can also configure a **Random Re-analysis %** to gradually refresh a portion of your library each run — useful for improving scores over time without re-analyzing everything at once.

### Check progress

```bash
docker exec navidrome sqlite3 /data/plugins/mood-playlists/kvstore.db \
  "SELECT COUNT(*) FROM kvstore WHERE key LIKE 'mood:%' AND key != 'mood:index';"
```

Replace `/data` with your Navidrome data path inside the container if different.

Watch live in logs (Linux/macOS):
```bash
docker logs navidrome -f | grep -i "Analyzed\|failed"
```

Watch live in logs (Windows PowerShell):
```powershell
docker logs navidrome -f | Select-String "Analyzed|failed"
```

Each successfully analyzed track produces a log line like:
```
Analyzed: Smoke on the Water
```

Occasional `Task execution failed` warnings are normal — the task queue retries each track up to 3 times automatically.

---

## 8. Step 6 — Generate the Playlists

Playlists are generated on a separate schedule from analysis (default: Sundays at 3 AM). You do not need to wait for full analysis to complete — the plugin builds playlists from however many tracks have been analyzed so far. Run the refresh after a few hundred tracks are done to see initial results.

### Trigger playlist refresh manually

Use the same approach as triggering analysis — set **Playlist Refresh Schedule** to fire 2 minutes from now, wait, then set it back to `0 3 * * 0`.

Or set it to `* * * * *`, wait one minute, then set it back.

### Where to find the playlists

After the refresh runs, up to 13 playlists appear in Navidrome's **Playlists** section:

**Simple moods:** Happy Mix, Chill Mix, Energetic Mix, Melancholy Mix, Party Mix, Aggressive Mix

**Scenario playlists:** Study Mix, Workout Mix, Sleep Mix, Road Trip Mix, Cooking Mix, Dining Mix, Background Mix

If a playlist has no qualifying tracks it is skipped silently — this is normal early on when few tracks have been analyzed, or if your thresholds are strict. Run analysis longer and refresh again.

### Instant Mix

Once tracks are analyzed, **Instant Mix** in any Subsonic-compatible client (Symfonium, Sublime Music, etc.) uses mood-similarity matching instead of Navidrome's default behaviour. Tracks are ranked by how closely their mood vector matches the source track.

---

## 9. Understanding the Playlists

### Simple mood playlists

Each selects tracks scoring above a threshold on a single mood dimension. Thresholds are adjustable in the plugin settings.

| Playlist | Scored by | Default threshold | Excludes |
|----------|-----------|-------------------|---------|
| **Happy Mix** | mood_happy | 0.55 | Tracks with mood_sad ≥ 0.4 |
| **Chill Mix** | mood_relaxed | 0.40 | Tracks with mood_aggressive ≥ 0.35 |
| **Energetic Mix** | danceability | 0.60 | — |
| **Melancholy Mix** | mood_sad | 0.45 | Tracks with mood_happy ≥ 0.5 |
| **Party Mix** | mood_party | 0.55 | Tracks with mood_sad ≥ 0.4 |
| **Aggressive Mix** | mood_aggressive | 0.55 | Tracks with mood_relaxed ≥ 0.35 or mood_happy ≥ 0.45 |

The exclusions prevent contradictory tracks appearing — for example a cheerful pop song will not appear in the Aggressive Mix even if it scores moderately on aggression, because it scores higher on happiness.

### Composite mood playlists

These playlists exclude tracks that clearly don't fit the scenario, then take the highest-scoring tracks from what remains. This approach guarantees a full playlist regardless of how your library scores — every playlist always has tracks.

| Playlist | Excludes | Sorted by |
|----------|----------|-----------|
| **Study Mix** | aggressive ≥ 0.45, party ≥ 0.50 | mood_relaxed |
| **Workout Mix** | relaxed ≥ 0.60, sad ≥ 0.50 | danceability |
| **Sleep Mix** | aggressive ≥ 0.30, party ≥ 0.35 | mood_relaxed |
| **Road Trip Mix** | aggressive ≥ 0.40, sad ≥ 0.50 | mood_happy |
| **Cooking Mix** | aggressive ≥ 0.45, sad ≥ 0.45 | mood_happy |
| **Dining Mix** | aggressive ≥ 0.40 | mood_relaxed |
| **Background Mix** | aggressive ≥ 0.50, party ≥ 0.55 | mood_relaxed |

For example, Study Mix takes all tracks that are neither aggressive nor high-energy party music, then sorts them by relaxed score and picks the top 30 (or however many you've configured). The most genuinely relaxed tracks in your library always rise to the top.

### Tuning thresholds

**Playlist contains tracks that feel wrong:**
- Raise the threshold — fewer tracks qualify but they are a better fit

**Playlist has too few tracks or is empty:**
- Lower the threshold — more tracks qualify
- Check that enough tracks have been analyzed (see [Monitoring](#11-monitoring-analysis-progress))

### Weekly variation

By default, playlists change a little each time they are refreshed — even when no new tracks have been analyzed. The **Variation Pool** setting (default: 3) controls this. Instead of always picking the top 50 tracks, the plugin shuffles the top 150 qualifying tracks and picks 50 from those. Each refresh draws a different random 50 from the same high-quality pool.

- Set **Variation Pool** to `1` to disable shuffling and always get the same deterministic top-N tracks
- Set it higher (e.g. `5`) for more rotation between refreshes
- Quality stays high regardless — all candidates come from the top-scoring tracks in your library

### Artist diversity

**Max Tracks per Artist** (default: 3) prevents any one artist dominating a playlist. Tracks are sorted by score first, so the best-scoring tracks from each artist are kept and lower-scoring ones are dropped to make room for other artists. Set to `0` to disable the limit.

### Duplicate tracks

If the same recording appears on multiple albums in your library, only one copy appears in each playlist. The copy with the higher mood score is kept and the duplicate is dropped silently.

### Playlist updates

Each refresh updates existing playlists in-place rather than creating new ones. You will never accumulate duplicate playlists — the same 13 playlists are updated every time the refresh runs.

---

## 10. Configuration Reference

### Cron schedule format

```
┌──── minute (0–59)
│  ┌──── hour (0–23)
│  │  ┌──── day of month (1–31)
│  │  │  ┌──── month (1–12)
│  │  │  │  ┌──── day of week (0=Sunday, 6=Saturday)
│  │  │  │  │
*  *  *  *  *
```

| Expression | Meaning |
|-----------|---------|
| `0 2 * * *` | Every day at 2:00 AM |
| `0 3 * * 0` | Every Sunday at 3:00 AM |
| `0 */6 * * *` | Every 6 hours |
| `30 1 * * 1-5` | Weekdays at 1:30 AM |
| `* * * * *` | Every minute — **for testing only, set back when done** |

### All settings

| Setting | Default | Min | Max | Description |
|---------|---------|-----|-----|-------------|
| `navidrome_url` | `http://navidrome:4533` | — | — | Internal URL of Navidrome — must be reachable from inside Docker |
| `navidrome_user` | — | — | — | Navidrome username (masked in UI) |
| `navidrome_password` | — | — | — | Navidrome password (masked in UI) |
| `analyzer_url` | `http://mood-analyzer:8000` | — | — | URL of the analyzer service |
| `auto_analyze` | `true` | — | — | Enable scheduled analysis |
| `analyze_schedule` | `0 2 * * *` | — | — | Cron expression for analysis runs |
| `playlist_refresh_schedule` | `0 3 * * 0` | — | — | Cron expression for playlist refresh |
| `playlist_track_count` | `30` | 10 | 200 | Maximum tracks per playlist |
| `max_tracks_per_artist` | `3` | 0 | 50 | Maximum tracks per artist per playlist (0 = no limit) |
| `playlist_variation_pool` | `3` | 1 | 10 | Shuffle top N × pool tracks before picking; higher = more weekly variety (1 = always same tracks) |
| `reanalyze_uncertain` | `true` | — | — | Automatically re-analyze tracks with low-confidence scores |
| `reanalyze_percent` | `0` | 0 | 20 | Percentage of library to randomly re-analyze each re-analysis run (0 = disabled) |
| `reanalyze_schedule` | `0 4 1 * *` | — | — | Cron expression for the dedicated re-analysis pass |
| `similar_songs_count` | `20` | 5 | 100 | Tracks returned for Instant Mix |
| `happy_threshold` | `0.55` | 0 | 1 | Minimum score for Happy Mix |
| `chill_threshold` | `0.40` | 0 | 1 | Minimum score for Chill Mix |
| `energetic_threshold` | `0.60` | 0 | 1 | Minimum score for Energetic Mix |
| `party_threshold` | `0.55` | 0 | 1 | Minimum score for Party Mix |
| `melancholy_threshold` | `0.45` | 0 | 1 | Minimum score for Melancholy Mix |
| `aggressive_threshold` | `0.55` | 0 | 1 | Minimum score for Aggressive Mix |

---

## 11. Monitoring Analysis Progress

### Count analyzed tracks

```bash
docker exec navidrome sqlite3 /data/plugins/mood-playlists/kvstore.db \
  "SELECT COUNT(*) FROM kvstore WHERE key LIKE 'mood:%' AND key != 'mood:index';"
```

### Watch analysis in real time

Linux/macOS:
```bash
docker logs navidrome -f | grep -i "Analyzed\|failed\|playlist"
```

Windows PowerShell:
```powershell
docker logs navidrome -f | Select-String "Analyzed|failed|playlist"
```

### Check the analyzer service is healthy

```bash
docker exec mood-analyzer wget -qO- http://localhost:8000/health
```

### Check analyzer logs

```bash
docker logs mood-analyzer --tail 50
```

---

## 12. Troubleshooting

### Plugin does not appear in Settings → Plugins

- Verify `ND_PLUGINS_ENABLED=true` is in your Navidrome config
- Check the `.ndp` file is in the `plugins` subdirectory of your Navidrome data folder
- Check Navidrome logs:
  ```bash
  docker logs navidrome | grep -i plugin
  ```
  PowerShell: `docker logs navidrome | Select-String "plugin"`

### "Unable to render configuration form. The plugin's schema may be invalid."

The plugin file is outdated. Download the latest `mood-playlists.ndp` from the Releases page, copy it to the plugins directory, and restart Navidrome.

### Analysis tasks all failing immediately

The plugin cannot connect to Navidrome or the analyzer. Check both:

1. **Analyzer reachable from Navidrome?**
   ```bash
   docker exec navidrome wget -qO- http://mood-analyzer:8000/health
   ```
2. **Navidrome reachable from analyzer?**
   ```bash
   docker exec mood-analyzer wget -qO- "http://navidrome:4533/rest/ping?v=1.16.1&c=test&f=json"
   ```
3. Both containers must be on the same Docker network. Check with `docker network inspect`.

### Tasks failing with "context deadline exceeded"

The analyzer is taking too long. Make sure you are running the latest version of the analyzer image — older versions tried to download entire FLAC files before analysis, which could take too long for large files. Rebuild the image:
```bash
docker-compose up -d --build mood-analyzer
```

### Analyzer returning HTTP 500 errors

Check the analyzer logs for the actual error:
```bash
docker logs mood-analyzer --tail 50
```

Common causes:
- `NameError: name 'ANALYSIS_DURATION' is not defined` — old image, rebuild it
- ffmpeg connectivity error — Navidrome URL or credentials are wrong in plugin settings
- `models_available: false` — TensorFlow models missing, rebuild the image

### Playlists are empty or missing after refresh

- Check how many tracks have been analyzed (the sqlite count query above). Very few analyzed tracks means few or no qualifying tracks per playlist — this is expected early on.
- Check logs around the refresh time for `No tracks for X Mix` messages.
- Lower the threshold for empty playlists in the plugin settings.

### Playlists contain obviously wrong tracks

- The mood models are probabilistic. Some genres (classical, opera, spoken word) can produce unexpected scores.
- Raise the threshold for that playlist — stricter filtering removes borderline tracks.
- Make sure **Max Tracks per Artist** is set to a reasonable value (default 3) to prevent one artist dominating with mediocre scores.

### Playlists not refreshing on schedule

- Confirm the cron expression is correct using the [cron format table](#cron-schedule-format).
- Test by temporarily setting the schedule to `* * * * *`, saving, waiting one minute, then setting it back.

### Task queue database growing very large

Happens when large numbers of tasks fail repeatedly without being cleared. To reset safely:

```bash
# Stop Navidrome first — deleting while running just recreates the file immediately
docker stop navidrome

# Delete the task queue database
docker run --rm -v YOUR_NAVIDROME_VOLUME:/data alpine \
  rm -f /data/plugins/mood-playlists/taskqueue.db

# Restart
docker start navidrome
```

Replace `YOUR_NAVIDROME_VOLUME` with your actual volume name (find it with `docker inspect navidrome`).

### Plugin stops working after copying a new .ndp

Navidrome automatically **disables** the plugin whenever the `.ndp` file is replaced on disk. This is by design — it treats any file change as a new, unapproved plugin.

After copying a new `.ndp`:
1. Go to **Settings → Plugins**
2. Find **Mood Playlists** and toggle it back on
3. No restart required

You will need to do this every time you update the plugin.

### Configuration changes not appearing in the UI

Navidrome caches the plugin config schema. A full restart is required after replacing the `.ndp` file:
```bash
docker restart navidrome
```
Wait 30 seconds, then reload the settings page in your browser.

---

## 13. Building from Source

### Requirements

- Go 1.24 or later
- PowerShell (Windows) or zip utility (Linux/macOS)

### Build the plugin

**Windows (PowerShell):**
```powershell
cd C:\path\to\navidrome-mood-plugin
$env:GOOS = "wasip1"
$env:GOARCH = "wasm"
go build -buildmode=c-shared -o plugin.wasm .

# Package
Remove-Item -Force mood-playlists.ndp -ErrorAction SilentlyContinue
Compress-Archive -Path plugin.wasm,manifest.json -DestinationPath mood-playlists.zip
Rename-Item mood-playlists.zip mood-playlists.ndp
```

**Linux / macOS:**
```bash
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
zip mood-playlists.ndp plugin.wasm manifest.json
```

### Build the analyzer service image

```bash
docker-compose up -d --build mood-analyzer
```

Or standalone:
```bash
cd analyzer-service
docker build -t mood-analyzer .
docker restart mood-analyzer
```

The first build downloads ~500 MB of TensorFlow models. Subsequent builds are cached and fast.
