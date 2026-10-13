.PHONY: test lint dev it

# Unit tests with the race detector.
test:
	go test -race ./...

lint:
	golangci-lint run ./...
	golangci-lint run --build-tags integration ./integration/

# Development build next to the released binary; it always needs --profile
# or DOK_PROFILE, so it cannot touch production by accident.
dev:
	go build -o "$$(go env GOPATH)/bin/dok-dev" ./cmd/dok

# Integration tests against a test site; see integration/README.md.
it:
	DOK_PROFILE=$${DOK_PROFILE:-test} go test -tags integration -count=1 -v ./integration/
