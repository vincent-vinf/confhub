module gitlab.bodesitech.com/bodesi/confhub/tests/system

go 1.25

require (
	github.com/gorilla/websocket v1.5.3
	gitlab.bodesitech.com/bodesi/confhub/sdk/go v0.0.0
)

replace gitlab.bodesitech.com/bodesi/confhub/sdk/go => ../../sdk/go
