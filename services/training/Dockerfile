FROM python:3.12-slim

WORKDIR /app

COPY services/training/requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY services/training/ .

# Set Python path so 'training' package is importable.
ENV PYTHONPATH=/app

CMD ["python", "cmd/main.py"]
