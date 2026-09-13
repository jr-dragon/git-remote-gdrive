.PHONY: build clean benchmark-drive

build: build/git-gdrive build/git-remote-gdrive build/git-remote-gdrive-local

benchmark-drive:
	go test ./git-remote-gdrive -run '^$$' -bench '^BenchmarkDriveAPI$$' -benchmem -benchtime=300ms -count=3

build/git-gdrive: $(shell find git-gdrive internal -type f -name '*.go') go.mod go.sum
	@mkdir -p build
	go build -o $@ ./git-gdrive

build/git-remote-gdrive: $(shell find git-remote-gdrive internal -type f -name '*.go') go.mod go.sum
	@mkdir -p build
	go build -o $@ ./git-remote-gdrive

clean:
	rm -f build/git-gdrive build/git-remote-gdrive build/git-remote-gdrive-local

build/git-remote-gdrive-local: $(shell find git-remote-gdrive-local internal -type f -name '*.go') go.mod go.sum
	@mkdir -p build
	go build -o $@ ./git-remote-gdrive-local
