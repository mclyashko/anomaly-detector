package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ModelStoragePort fetches ONNX models and metadata from MinIO.
type ModelStoragePort interface {
	// ListModelPrefixes returns all model prefixes under "models/" prefix.
	// e.g. ["agent-1__http_latency_avg_ms__sarima__v1", "agent-1__http_latency_avg_ms__sarima__v2"]
	ListModelPrefixes(ctx context.Context) ([]string, error)
	// DownloadModel returns the raw ONNX bytes for a given model prefix.
	DownloadModel(ctx context.Context, prefix string) ([]byte, error)
	// GetMetadata returns the parsed metadata.json for a given prefix.
	GetMetadata(ctx context.Context, prefix string) (*ModelMetadata, error)
	// GetLatestVersion returns the latest version prefix for a given triple.
	GetLatestVersion(ctx context.Context, agentID, metricName, modelType string) (string, error)
}

// ModelMetadata is the content of metadata.json stored alongside each ONNX model.
type ModelMetadata struct {
	AgentID           string  `json:"agent_id"`
	MetricName       string  `json:"metric_name"`
	ModelType        string  `json:"model_type"`
	Version          string  `json:"version"`
	TrainingTimestamp string `json:"training_timestamp"`
	TrainingStart    string  `json:"training_start"`
	TrainingEnd      string  `json:"training_end"`
	SeasonalityPeriod int    `json:"seasonality_period"`
	ConfidenceLevel  float64 `json:"confidence_level"`
	WindowSize       int     `json:"window_size"`
	// SARIMA parameters.
	ARParams         float64 `json:"ar_params"`
	MAParams         float64 `json:"ma_params"`
	SeasonalARParams float64 `json:"seasonal_ar_params"`
	SeasonalMAParams float64 `json:"seasonal_ma_params"`
	ResidualStd      float64 `json:"residual_std"`
	D                int     `json:"d"`
	SeasonalD        int     `json:"seasonal_d"`
}

// MinIOStorage is a ModelStoragePort implementation using MinIO/S3.
type MinIOStorage struct {
	client *minio.Client
	bucket string
}

// NewMinIOStorage creates a MinIO storage client.
func NewMinIOStorage(endpoint, accessKey, secretKey, bucket string, useSSL bool) (*MinIOStorage, error) {
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("minio new client: %w", err)
	}
	return &MinIOStorage{client: client, bucket: bucket}, nil
}

func (s *MinIOStorage) ListModelPrefixes(ctx context.Context) ([]string, error) {
	prefix := "models/"
	objects := s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: false,
	})
	var prefixes []string
	seen := make(map[string]struct{})
	for obj := range objects {
		if obj.Err != nil {
			return nil, fmt.Errorf("list objects: %w", obj.Err)
		}
		name := strings.TrimPrefix(obj.Key, prefix)
		if name == "" {
			continue
		}
		// name is like "agent-1__http_latency__sarima__v1/model.onnx"
		// extract the prefix part (up to the last '/')
		idx := strings.LastIndex(name, "/")
		if idx == -1 {
			continue
		}
		p := name[:idx]
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			prefixes = append(prefixes, p)
		}
	}
	return prefixes, nil
}

func (s *MinIOStorage) DownloadModel(ctx context.Context, prefix string) ([]byte, error) {
	objectName := fmt.Sprintf("models/%s/model.onnx", prefix)
	obj, err := s.client.GetObject(ctx, s.bucket, objectName, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("get object %s: %w", objectName, err)
	}
	defer obj.Close()
	data, err := io.ReadAll(obj)
	if err != nil {
		return nil, fmt.Errorf("read object %s: %w", objectName, err)
	}
	return data, nil
}

func (s *MinIOStorage) GetMetadata(ctx context.Context, prefix string) (*ModelMetadata, error) {
	objectName := fmt.Sprintf("models/%s/metadata.json", prefix)
	obj, err := s.client.GetObject(ctx, s.bucket, objectName, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("get object %s: %w", objectName, err)
	}
	defer obj.Close()
	data, err := io.ReadAll(obj)
	if err != nil {
		return nil, fmt.Errorf("read metadata %s: %w", objectName, err)
	}
	var meta ModelMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parse metadata.json for %s: %w", prefix, err)
	}
	return &meta, nil
}

func (s *MinIOStorage) GetLatestVersion(ctx context.Context, agentID, metricName, modelType string) (string, error) {
	prefix := "models/"
	objects := s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: false,
	})
	// The prefix we want is agentID__metricName__modelType__v{N}
	prefixStr := fmt.Sprintf("%s__%s__%s__", agentID, metricName, modelType)
	var best string
	var bestNum int
	for obj := range objects {
		if obj.Err != nil {
			return "", fmt.Errorf("list objects: %w", obj.Err)
		}
		name := strings.TrimPrefix(obj.Key, prefix)
		if !strings.HasPrefix(name, prefixStr) {
			continue
		}
		// name is like "agent-1__http_latency__sarima__v1/model.onnx"
		name = strings.TrimPrefix(name, prefixStr)
		idx := strings.LastIndex(name, "/")
		if idx != -1 {
			name = name[:idx]
		}
		// name is like "v1" or "v10"
		if len(name) < 2 || name[0] != 'v' {
			continue
		}
		var num int
		if _, err := fmt.Sscanf(name[1:], "%d", &num); err != nil {
			continue
		}
		if num > bestNum {
			bestNum = num
			best = name
		}
	}
	if best == "" {
		return "", nil
	}
	return fmt.Sprintf("%s__%s__%s__%s", agentID, metricName, modelType, best), nil
}

// UploadModel uploads a model (ONNX bytes + metadata) to MinIO.
// This is used by the training service; the analyzer only reads.
func (s *MinIOStorage) UploadModel(ctx context.Context, prefix string, onnxBytes []byte, meta *ModelMetadata) error {
	objectONNX := fmt.Sprintf("models/%s/model.onnx", prefix)
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	_, err = s.client.PutObject(ctx, s.bucket, objectONNX, bytes.NewReader(onnxBytes), int64(len(onnxBytes)), minio.PutObjectOptions{
		ContentType: "application/octet-stream",
	})
	if err != nil {
		return fmt.Errorf("put onnx object: %w", err)
	}
	_, err = s.client.PutObject(ctx, s.bucket, fmt.Sprintf("models/%s/metadata.json", prefix), bytes.NewReader(metaBytes), int64(len(metaBytes)), minio.PutObjectOptions{
		ContentType: "application/json",
	})
	if err != nil {
		return fmt.Errorf("put metadata object: %w", err)
	}
	return nil
}

// Verify ModelStoragePort is implemented.
var _ ModelStoragePort = (*MinIOStorage)(nil)
