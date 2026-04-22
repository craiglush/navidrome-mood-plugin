"""Mood Analyzer Service — standalone HTTP API for audio mood analysis.

Uses essentia-tensorflow with Discogs-EffNet embeddings to extract:
- Mood scores: happy, sad, relaxed, aggressive, party
- Danceability, BPM, energy

Genre and BPM context is used to improve classification accuracy.

Run with: uvicorn app:app --host 0.0.0.0 --port 8000
"""

import logging
import os
import subprocess
import tempfile

import mutagen
import numpy as np
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

app = FastAPI(title="Mood Analyzer", version="1.1.0")

MODELS_DIR = os.environ.get("MODELS_DIR", "/app/models")
ANALYSIS_DURATION = 120.0  # seconds — cap audio loaded per track for predictable analysis time

_es = None


def _load_essentia():
    global _es
    if _es is None:
        import essentia.standard as es
        _es = es
    return _es


# ── Genre/BPM Context Boosts ─────────────────────────────────

GENRE_BOOSTS = {
    # High-energy dance genres: boost danceability + party
    "drum": {"danceability": 0.35, "mood_party": 0.15, "mood_aggressive": 0.10},
    "jungle": {"danceability": 0.35, "mood_party": 0.15, "mood_aggressive": 0.10},
    "dnb": {"danceability": 0.35, "mood_party": 0.15, "mood_aggressive": 0.10},
    "dance": {"danceability": 0.20, "mood_party": 0.10},
    "house": {"danceability": 0.20, "mood_party": 0.10},
    "techno": {"danceability": 0.25, "mood_party": 0.15, "mood_aggressive": 0.10},
    "trance": {"danceability": 0.20, "mood_party": 0.10, "mood_happy": 0.10},
    "edm": {"danceability": 0.20, "mood_party": 0.10},
    "electronic": {"danceability": 0.10, "mood_party": 0.05},
    "disco": {"danceability": 0.20, "mood_party": 0.15, "mood_happy": 0.10},
    "funk": {"danceability": 0.15, "mood_party": 0.10, "mood_happy": 0.05},
    # Aggressive genres
    "metal": {"mood_aggressive": 0.25, "mood_relaxed": -0.15},
    "hardcore": {"mood_aggressive": 0.25, "danceability": 0.15},
    "punk": {"mood_aggressive": 0.15, "mood_party": 0.05},
    "industrial": {"mood_aggressive": 0.20},
    # Chill genres
    "ambient": {"mood_relaxed": 0.20, "mood_aggressive": -0.10},
    "downtempo": {"mood_relaxed": 0.15, "danceability": -0.10},
    "chillout": {"mood_relaxed": 0.20, "mood_party": -0.10},
    "lounge": {"mood_relaxed": 0.15},
    "easy listening": {"mood_relaxed": 0.20, "mood_happy": 0.10},
    "new age": {"mood_relaxed": 0.20},
    # Sad/melancholy
    "blues": {"mood_sad": 0.10},
    "emo": {"mood_sad": 0.15, "mood_aggressive": 0.05},
    # Happy/upbeat
    "reggae": {"mood_happy": 0.10, "mood_relaxed": 0.10},
    "ska": {"mood_happy": 0.15, "danceability": 0.10},
    "pop": {"mood_happy": 0.05, "danceability": 0.05},
    # R&B/Soul
    "r&b": {"mood_relaxed": 0.05, "mood_party": 0.05},
    "soul": {"mood_relaxed": 0.05, "mood_happy": 0.05},
}

BPM_DANCEABILITY_BOOST = {
    (140, 180): 0.20,  # DnB, breakbeat, fast house
    (180, 250): 0.15,  # Hardcore, speedcore
    (90, 110): 0.05,   # Hip-hop, trip-hop
}


def _apply_context_boosts(scores, genre, artist, bpm):
    """Adjust raw essentia scores based on genre/artist/BPM context."""
    adjusted = dict(scores)
    genre_lower = (genre or "").lower()

    for keyword, boosts in GENRE_BOOSTS.items():
        if keyword in genre_lower:
            for score_key, boost in boosts.items():
                if score_key in adjusted:
                    adjusted[score_key] = adjusted[score_key] + boost

    if bpm and bpm > 0:
        effective_bpm = bpm
        if 80 <= bpm <= 95 and any(k in genre_lower for k in ("drum", "jungle", "dnb", "bass")):
            effective_bpm = bpm * 2

        for (lo, hi), boost in BPM_DANCEABILITY_BOOST.items():
            if lo <= effective_bpm <= hi:
                adjusted["danceability"] = adjusted.get("danceability", 0) + boost
                break

    for key in adjusted:
        if isinstance(adjusted[key], float):
            adjusted[key] = round(max(0.0, min(1.0, adjusted[key])), 4)

    return adjusted


# ── API ───────────────────────────────────────────────────────

class AnalyzeRequest(BaseModel):
    file_path: str

class AnalyzeUrlRequest(BaseModel):
    url: str


@app.get("/health")
def health():
    try:
        _load_essentia()
        effnet = os.path.join(MODELS_DIR, "discogs-effnet-bs64-1.pb")
        return {"status": "ok", "models_available": os.path.exists(effnet)}
    except ImportError:
        return {"status": "error", "message": "essentia not installed"}


def _analyze_path(file_path: str) -> dict:
    es = _load_essentia()
    results = {}

    # Read metadata
    title, artist, album, genre = "", "", "", ""
    try:
        tags = mutagen.File(file_path)
        if tags:
            title = str((tags.get("\xa9nam") or tags.get("title") or [""])[0])
            artist = str((tags.get("\xa9ART") or tags.get("artist") or [""])[0])
            album = str((tags.get("\xa9alb") or tags.get("album") or [""])[0])
            genre = str((tags.get("\xa9gen") or tags.get("genre") or [""])[0])
    except Exception:
        pass
    if not title:
        title = os.path.splitext(os.path.basename(file_path))[0]

    # BPM
    try:
        audio_44k = es.MonoLoader(filename=file_path, sampleRate=44100)()
        max_samples = int(ANALYSIS_DURATION * 44100)
        if len(audio_44k) > max_samples:
            audio_44k = audio_44k[:max_samples]
        bpm = es.RhythmExtractor2013(method="multifeature")(audio_44k)[0]
        results["bpm"] = round(float(bpm), 1)
    except Exception as e:
        logger.warning(f"BPM failed for {file_path}: {e}")
        results["bpm"] = 0.0

    # Energy (RMS)
    try:
        results["energy"] = round(float(np.sqrt(np.mean(audio_44k ** 2))), 4)
    except Exception:
        results["energy"] = 0.0

    # Embeddings
    audio_16k = es.MonoLoader(filename=file_path, sampleRate=16000, resampleQuality=4)()
    max_samples_16k = int(ANALYSIS_DURATION * 16000)
    if len(audio_16k) > max_samples_16k:
        audio_16k = audio_16k[:max_samples_16k]
    effnet_path = os.path.join(MODELS_DIR, "discogs-effnet-bs64-1.pb")

    try:
        embeddings = es.TensorflowPredictEffnetDiscogs(
            graphFilename=effnet_path, output="PartitionedCall:1"
        )(audio_16k)
    except Exception as e:
        logger.error(f"Embedding failed: {e}")
        results.update({k: 0.0 for k in [
            "danceability", "mood_happy", "mood_sad",
            "mood_relaxed", "mood_aggressive", "mood_party"
        ]})
        results = _apply_context_boosts(results, genre, artist, results.get("bpm", 0))
        return {"file_path": file_path, "title": title, "artist": artist,
                "album": album, "genre": genre, **results}

    # Mood classifiers
    mood_models = {
        "mood_happy": "mood_happy-discogs-effnet-1.pb",
        "mood_sad": "mood_sad-discogs-effnet-1.pb",
        "mood_relaxed": "mood_relaxed-discogs-effnet-1.pb",
        "mood_aggressive": "mood_aggressive-discogs-effnet-1.pb",
        "mood_party": "mood_party-discogs-effnet-1.pb",
    }

    for key, model_file in mood_models.items():
        model_path = os.path.join(MODELS_DIR, model_file)
        try:
            if not os.path.exists(model_path):
                results[key] = 0.0
                continue
            preds = es.TensorflowPredict2D(
                graphFilename=model_path, output="model/Softmax"
            )(embeddings)
            results[key] = round(float(np.mean(preds[:, 1])), 4)
        except Exception as e:
            logger.warning(f"{key} failed: {e}")
            results[key] = 0.0

    # Danceability
    dance_path = os.path.join(MODELS_DIR, "danceability-discogs-effnet-1.pb")
    try:
        if os.path.exists(dance_path):
            preds = es.TensorflowPredict2D(
                graphFilename=dance_path, output="model/Softmax"
            )(embeddings)
            results["danceability"] = round(float(np.mean(preds[:, 1])), 4)
        else:
            results["danceability"] = 0.0
    except Exception as e:
        logger.warning(f"Danceability failed: {e}")
        results["danceability"] = 0.0

    # Apply genre/BPM context boosts
    results = _apply_context_boosts(results, genre, artist, results.get("bpm", 0))

    return {"file_path": file_path, "title": title, "artist": artist,
            "album": album, "genre": genre, **results}


@app.post("/api/analysis/file")
def analyze_file(req: AnalyzeRequest):
    if not os.path.exists(req.file_path):
        raise HTTPException(status_code=404, detail=f"File not found: {req.file_path}")
    return _analyze_path(req.file_path)


@app.post("/api/analysis/url")
def analyze_url(req: AnalyzeUrlRequest):
    """Fetch audio from a URL and analyze it.

    Uses ffmpeg to stream only the first ANALYSIS_DURATION seconds directly
    from the URL, converting to a small mono WAV (~7 MB) regardless of the
    source format or bitrate. This avoids downloading multi-hundred-MB FLAC
    files in full before analysis can begin.
    """
    tmp_path = None
    try:
        with tempfile.NamedTemporaryFile(suffix=".wav", delete=False) as tmp:
            tmp_path = tmp.name

        result = subprocess.run(
            [
                "ffmpeg", "-y",
                "-i", req.url,
                "-t", str(ANALYSIS_DURATION),
                "-ar", "44100",
                "-ac", "1",
                "-sample_fmt", "s16",
                tmp_path,
            ],
            capture_output=True,
            timeout=60,
        )
        if result.returncode != 0:
            err = result.stderr.decode(errors="replace").strip()
            logger.error(f"ffmpeg failed (rc={result.returncode}):\n{err}")
            last_line = err.splitlines()[-1] if err else "ffmpeg failed"
            raise Exception(last_line)

        return _analyze_path(tmp_path)
    except Exception as e:
        logger.error(f"URL analysis error ({type(e).__name__}): {e}")
        raise HTTPException(status_code=500, detail=f"Failed to fetch or analyze URL: {e}")
    finally:
        if tmp_path and os.path.exists(tmp_path):
            os.unlink(tmp_path)
