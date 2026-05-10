module github.com/mclyashko/anomaly-detector/services/agent

go 1.25.1

require (
	github.com/joho/godotenv v1.5.1
	github.com/mclyashko/anomaly-detector/shared v0.0.0
	github.com/segmentio/kafka-go v0.4.50
)

require (
	github.com/klauspost/compress v1.15.9 // indirect
	github.com/pierrec/lz4/v4 v4.1.15 // indirect
	github.com/stretchr/testify v1.11.1 // indirect
	golang.org/x/net v0.43.0 // indirect
	golang.org/x/text v0.29.0 // indirect
)

replace github.com/mclyashko/anomaly-detector/shared => ../../shared
