module github.com/mclyashko/anomaly-detector/services/fake-service

go 1.25.1

require (
	github.com/joho/godotenv v1.5.1
	github.com/mclyashko/anomaly-detector/shared v0.0.0
)

replace github.com/mclyashko/anomaly-detector/shared => ../../shared
