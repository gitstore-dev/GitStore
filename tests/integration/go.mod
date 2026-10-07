module github.com/gitstore-dev/gitstore/tests/integration

go 1.26.0

require (
	github.com/gitstore-dev/gitstore/secretmaterial v0.0.0
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/gorilla/websocket v1.5.3
	github.com/prometheus/client_model v0.6.3
	github.com/prometheus/common v0.72.0
	github.com/stretchr/testify v1.12.1
)

replace github.com/gitstore-dev/gitstore/secretmaterial => ../../shared/secretmaterial

require (
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
