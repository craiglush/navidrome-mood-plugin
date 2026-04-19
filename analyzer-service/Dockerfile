FROM python:3.11-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ffmpeg wget ca-certificates && \
    rm -rf /var/lib/apt/lists/*

COPY requirements.txt /app/requirements.txt
RUN pip install --no-cache-dir -r /app/requirements.txt

# Download Essentia TF mood models
RUN mkdir -p /app/models && \
    wget -q -P /app/models https://essentia.upf.edu/models/feature-extractors/discogs-effnet/discogs-effnet-bs64-1.pb && \
    wget -q -P /app/models https://essentia.upf.edu/models/classification-heads/mood_happy/mood_happy-discogs-effnet-1.pb && \
    wget -q -P /app/models https://essentia.upf.edu/models/classification-heads/mood_sad/mood_sad-discogs-effnet-1.pb && \
    wget -q -P /app/models https://essentia.upf.edu/models/classification-heads/mood_relaxed/mood_relaxed-discogs-effnet-1.pb && \
    wget -q -P /app/models https://essentia.upf.edu/models/classification-heads/mood_aggressive/mood_aggressive-discogs-effnet-1.pb && \
    wget -q -P /app/models https://essentia.upf.edu/models/classification-heads/mood_party/mood_party-discogs-effnet-1.pb && \
    wget -q -P /app/models https://essentia.upf.edu/models/classification-heads/danceability/danceability-discogs-effnet-1.pb

COPY app.py /app/app.py
WORKDIR /app

EXPOSE 8000

CMD ["uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8000"]
