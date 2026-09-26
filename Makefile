.PHONY: build app test check clean release release-upload release-notarize setup-notarization
build:
	mkdir -p bin
	go build -trimpath -o bin/devcleaner ./cmd/devcleaner
app:
	./scripts/build-app.sh
release:
	./scripts/build-release.sh
	./scripts/notarize-release.sh
release-notarize:
	./scripts/notarize-release.sh
setup-notarization:
	./scripts/setup-notarization.sh
release-upload:
	./scripts/upload-release.sh
test:
	go test -race ./...
check:
	go vet ./...
clean:
	rm -rf bin
