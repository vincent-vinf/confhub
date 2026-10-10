module github.com/vincent-vinf/confhub/tests/system

go 1.25

require (
	github.com/gorilla/websocket v1.5.3
	github.com/vincent-vinf/confhub/sdk/go v0.0.0
)

replace github.com/vincent-vinf/confhub/sdk/go => ../../sdk/go
