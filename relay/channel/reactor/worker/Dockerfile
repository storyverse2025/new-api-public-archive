FROM python:3.12-slim-bookworm
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt
COPY worker.py ./
RUN useradd --system --uid 10001 worker && mkdir -p /data/reactor && chown worker:worker /data/reactor
USER worker
EXPOSE 8091
VOLUME /data/reactor
CMD ["python", "worker.py"]
