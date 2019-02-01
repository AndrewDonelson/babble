BUILD_TAGS?=babble

# vendor uses Glide to install all the Go dependencies in vendor/
vendor:
	glide install

# install compiles and places the binary in GOPATH/bin
install:
	go install --ldflags '-extldflags "-static"' \
		--ldflags "-X github.com/mosaicnetworks/babble/src/version.GitCommit=`git rev-parse HEAD`" \
		./cmd/babble
	go install --ldflags '-extldflags "-static"' \
		--ldflags "-X github.com/mosaicnetworks/babble/src/version.GitCommit=`git rev-parse HEAD`" \
		./cmd/babble-test

# build compiles and places the binary in /build
build:
	CGO_ENABLED=0 go build \
		--ldflags "-X github.com/mosaicnetworks/babble/src/version.GitCommit=`git rev-parse HEAD`" \
		-o build/babble ./cmd/babble/
	CGO_ENABLED=0 go build \
		--ldflags "-X github.com/mosaicnetworks/babble/src/version.GitCommit=`git rev-parse HEAD`" \
		-o build/babble-test ./cmd/babble-test/

# dist builds binaries for all platforms and packages them for distribution
dist:
	@BUILD_TAGS='$(BUILD_TAGS)' sh -c "'$(CURDIR)/scripts/dist.sh'"

test:
	glide novendor | xargs go test -count=1

.PHONY: vendor install build dist test
