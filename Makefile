.PHONY: build build-master build-plugins run-master test test-race test-integration clean

build: build-master build-plugins

build-master:
	mkdir -p dist
	go build -o dist/master ./cmd/master

build-plugins:
	mkdir -p dist/plugins/timestamp/v1.0.0 dist/plugins/timestamp/v2.0.0 dist/plugins/heartbeat/v1.0.0
	go build -o dist/plugins/timestamp/v1.0.0/timestamp-plugin ./cmd/plugins/timestamp
	go build -o dist/plugins/timestamp/v2.0.0/timestamp-plugin ./cmd/plugins/timestamp
	go build -o dist/plugins/heartbeat/v1.0.0/heartbeat-plugin ./cmd/plugins/heartbeat

run-master: build
	./dist/master

test:
	go test ./...

test-race:
	go test -race ./...

test-integration: build
	bash scripts/test-all.sh

clean:
	rm -rf dist

