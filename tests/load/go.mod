module github.com/vincent-vinf/confhub/tests/load

go 1.25

require github.com/vincent-vinf/confhub/sdk/go v0.0.0

require github.com/gorilla/websocket v1.5.3 // indirect

replace github.com/vincent-vinf/confhub/sdk/go => ../../sdk/go
