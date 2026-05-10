FROM python:3.12-slim

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends wget && rm -rf /var/lib/apt/lists/*

COPY services/training/requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY services/training/ .

# Set Python path so 'training' package is importable.
ENV PYTHONPATH=/app

# Set models directory
ENV MODELS_DIR=/app/models
ENV PORT=8085

# Create models directory
RUN mkdir -p /app/models

# Train command is no longer used — training is triggered via the HTTP API
# or auto-retrain loop, both served by uvicorn.
CMD ["uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8085"]
