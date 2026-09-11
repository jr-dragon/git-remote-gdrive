.PHONY: build clean

build: build/git-gdrive

build/git-gdrive: $(shell find git-gdrive internal -type f -name '*.go') go.mod go.sum
	@mkdir -p build
	go build -o $@ ./git-gdrive

clean:
	rm -f build/git-gdrive
