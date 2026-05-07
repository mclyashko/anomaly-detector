module github.com/mclyashko/anomaly-detector/services/analyzer

go 1.25.1

require (
	github.com/jackc/pgx/v5 v5.9.1
	github.com/joho/godotenv v1.5.1
	github.com/mclyashko/anomaly-detector/shared v0.0.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.18.2 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/pierrec/lz4/v4 v4.1.15 // indirect
	github.com/rogpeppe/go-internal v1.6.1 // indirect
	golang.org/x/net v0.48.0 // indirect
	golang.org/x/sync v0.19.0 // indirect
	golang.org/x/text v0.32.0 // indirect
)

require (
	github.com/segmentio/kafka-go v0.4.51
	github.com/yuin/gopher-lua v1.1.1
)

replace github.com/mclyashko/anomaly-detector/shared => ../../shared
