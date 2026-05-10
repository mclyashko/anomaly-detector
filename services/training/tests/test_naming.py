"""Tests for core/naming.py."""

import pytest

from training.core.naming import (
    model_name,
    parse_model_name,
    sanitize_metric_name,
)


class TestSanitizeMetricName:
    def test_replaces_dots(self):
        assert sanitize_metric_name("http.latency_avg_ms") == "http_latency_avg_ms"

    def test_replaces_slashes(self):
        assert sanitize_metric_name("cpu/metrics") == "cpu_metrics"

    def test_replaces_colons(self):
        assert sanitize_metric_name("service:metric") == "service_metric"

    def test_preserves_alphanumeric(self):
        assert sanitize_metric_name("agent-1") == "agent-1"


class TestModelName:
    def test_basic_format(self):
        result = model_name("agent-1", "http_latency_avg_ms", "sarima", "v1")
        assert result == "agent-1__http_latency_avg_ms__sarima__v1"

    def test_metric_name_with_slash(self):
        result = model_name("agent-2", "worker_ops_total", "sarima", "v3")
        assert result == "agent-2__worker_ops_total__sarima__v3"

    def test_version_with_number(self):
        result = model_name("svc", "metric", "arima", "v12")
        assert result == "svc__metric__arima__v12"


class TestParseModelName:
    def test_parses_valid_name(self):
        parsed = parse_model_name("agent-1__http_latency__sarima__v3")
        assert parsed is not None
        assert parsed["agent_id"] == "agent-1"
        assert parsed["metric_name"] == "http_latency"
        assert parsed["model_type"] == "sarima"
        assert parsed["version"] == "v3"

    def test_handles_underscores_in_metric(self):
        # Metric name itself uses underscores, so the split on __ works correctly.
        parsed = parse_model_name("agent-1__http_latency_avg_ms__sarima__v1")
        assert parsed is not None
        assert parsed["metric_name"] == "http_latency_avg_ms"

    def test_invalid_format_returns_none(self):
        assert parse_model_name("no_double_underscore") is None
        assert parse_model_name("only_two_parts__v1") is None
        assert parse_model_name("") is None
