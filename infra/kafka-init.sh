#!/bin/bash
set -e

echo "Waiting for Kafka broker to be ready..."
until kafka-topics --bootstrap-server kafka:9092 --list 2>/dev/null; do
    sleep 2
done

echo "Creating topics..."
kafka-topics --bootstrap-server kafka:9092 \
  --create --if-not-exists \
  --topic metrics \
  --partitions 1 \
  --replication-factor 1 2>/dev/null || true

kafka-topics --bootstrap-server kafka:9092 \
  --create --if-not-exists \
  --topic anomalies \
  --partitions 1 \
  --replication-factor 1 2>/dev/null || true

echo "Topics ready."
