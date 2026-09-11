.PHONY: build clean

build: build/git-gdrive build/git-remote-gdrive

build/git-gdrive: $(shell find git-gdrive internal -type f -name '*.go') go.mod go.sum
	@mkdir -p build
	go build -o $@ ./git-gdrive

build/git-remote-gdrive: $(shell find git-remote-gdrive internal -type f -name '*.go') go.mod go.sum
	@mkdir -p build
	go build -o $@ ./git-remote-gdrive

clean:
	rm -f build/git-gdrive build/git-remote-gdrive
