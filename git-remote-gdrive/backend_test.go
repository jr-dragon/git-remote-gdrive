package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jr-dragon/git-remote-gdrive/internal/drive"
)

func testAPI(t testing.TB, client *http.Client, endpoint, backend string) drive.API {
	t.Helper()
	api, err := drive.NewAPI(context.Background(), client, backend)
	if err != nil {
		t.Fatal(err)
	}
	switch c := api.(type) {
	case *drive.Client:
		c.BaseURL = endpoint
	case *drive.SDKV3Client:
		c.Service.BasePath = endpoint + "/drive/v3/"
	case *drive.SDKV2Client:
		c.Service.BasePath = endpoint + "/drive/v2/"
	}
	return api
}

func (d *mockDrive) api(t testing.TB) drive.API {
	return testAPI(t, d.server.Client(), d.server.URL, os.Getenv("GIT_GDRIVE_API_BACKEND"))
}

// Rotate writers and readers over the same root, including asset pointers and
// mutable ZIPs. The format must not depend on the selected API version.
func TestBackendInteroperability(t *testing.T) {
	d := newMockDrive(t)
	env := testEnvironment(t, d)
	for i, value := range env {
		if strings.HasPrefix(value, "GIT_GDRIVE_API_BACKEND=") {
			env = append(env[:i], env[i+1:]...)
			break
		}
	}
	root := t.TempDir()
	var previous, previousDir string
	var zipID string
	for index, backend := range []string{"http", "sdkv3", "sdkv2", "sdkv3", "http"} {
		backendEnv := append(append([]string{}, env...), "GIT_GDRIVE_API_BACKEND="+backend)
		git := func(dir string, args ...string) string { return gitCommand(t, dir, backendEnv, true, args...) }
		dir := filepath.Join(root, fmt.Sprintf("clone-%s-%d", backend, index))
		if previousDir == "" {
			git(root, "init", "-b", "main", dir)
			git(dir, "remote", "add", "origin", "gdrive://root")
			git(dir, "gdrive", "install")
			if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("*.bin filter=gdrive-assets -text\n"), 0600); err != nil {
				t.Fatal(err)
			}
		} else {
			git(root, "clone", "--no-checkout", "gdrive://root", dir)
			git(dir, "gdrive", "install")
			git(dir, "reset", "--hard", "HEAD")
			data, err := os.ReadFile(filepath.Join(dir, "asset.bin"))
			if err != nil || string(data) != previous {
				t.Fatal("cross-backend asset checkout failed", string(data), err)
			}
		}
		payload := backend + previous
		if err := os.WriteFile(filepath.Join(dir, "asset.bin"), []byte(payload), 0600); err != nil {
			t.Fatal(err)
		}
		git(dir, "add", ".")
		git(dir, "commit", "-m", backend)
		git(dir, "push", "origin", "main")
		m := d.manifest(t)
		archive := m.Archives["refs/heads/main"]
		if zipID != "" && archive.ID != zipID {
			t.Fatal("backend switch created a new ZIP")
		}
		zipID = archive.ID
		if previousDir != "" {
			git(previousDir, "pull", "--ff-only", "origin", "main")
			if git(previousDir, "rev-parse", "HEAD") != git(dir, "rev-parse", "HEAD") {
				t.Fatal("cross-backend pull lost refs")
			}
		}
		git(dir, "fsck", "--full")
		previous, previousDir = payload, dir
	}
}

func TestBackendLostUploadResponse(t *testing.T) {
	for _, backend := range []string{"http", "sdkv2", "sdkv3"} {
		t.Run(backend, func(t *testing.T) {
			d := newMockDrive(t)
			api := testAPI(t, d.server.Client(), d.server.URL, backend)
			data := []byte("immutable payload")
			d.lostUpload = true
			id, err := api.UploadFile(context.Background(), drive.File{}, "root", "test.pack", bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			d.mu.Lock()
			defer d.mu.Unlock()
			if !bytes.Equal(d.files[id].Data, data) || len(d.files) != 2 {
				t.Fatal("lost response caused duplicate or corrupt upload")
			}
		})
	}
}

// Run the same repository invariants against the alternative backends.
// Ordinary tests exercise the default v3 SDK.
func TestAlternativeBackends(t *testing.T) {
	for _, backend := range []string{"http", "sdkv2"} {
		t.Run(backend, func(t *testing.T) {
			t.Setenv("GIT_GDRIVE_API_BACKEND", backend)
			for _, tc := range []struct {
				name string
				run  func(*testing.T)
			}{
				{"GitRoundTrip", TestGitRoundTrip},
				{"ConcurrentInitialization", TestConcurrentInitialization},
				{"LargeResumableUpload", TestLargeResumableUpload},
				{"EmptyAndCorruptRemote", TestEmptyAndCorruptRemote},
				{"PushArchives", TestPushArchives},
				{"ArchiveOverwriteResumeAndStaleWriter", TestArchiveOverwriteResumeAndStaleWriter},
				{"LegacyBranchArchiveDirectory", TestLegacyBranchArchiveDirectory},
				{"ArchiveMetadataFailure", TestPushSucceedsWhenArchiveMetadataFails},
				{"AssetsRoundTrip", TestAssetsGitRoundTrip},
				{"GlobalAssetInstall", TestGlobalAssetInstallAndClone},
				{"AssetFailure", TestAssetFailureDoesNotPublishRefs},
				{"AssetVerification", TestAssetDownloadVerification},
				{"Progress", TestGitProgress},
			} {
				t.Run(tc.name, tc.run)
			}
		})
	}
}

// These loopback benchmarks compare adapter/request overhead, not Google Drive
// latency. All backends use the same client boundary and payloads.
func BenchmarkDriveAPI(b *testing.B) {
	for _, backend := range []string{"http", "sdkv2", "sdkv3"} {
		b.Run(backend, func(b *testing.B) {
			b.Run("Metadata", func(b *testing.B) {
				d := newMockDrive(b)
				api := testAPI(b, d.server.Client(), d.server.URL, backend)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := api.Metadata(context.Background(), "root"); err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				d.mu.Lock()
				b.ReportMetric(float64(d.requests)/float64(b.N), "requests/op")
				d.mu.Unlock()
			})
			for _, size := range []int{1024, 9 << 20} {
				b.Run(fmt.Sprintf("Upload-%d", size), func(b *testing.B) {
					d := newMockDrive(b)
					api := testAPI(b, d.server.Client(), d.server.URL, backend)
					data := bytes.Repeat([]byte("x"), size)
					b.ReportAllocs()
					b.SetBytes(int64(size))
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						id, err := api.UploadFile(context.Background(), drive.File{}, "root", "benchmark.pack", bytes.NewReader(data), int64(size))
						if err != nil {
							b.Fatal(err)
						}
						d.mu.Lock()
						delete(d.files, id)
						delete(d.uploads, "/session/"+id)
						d.mu.Unlock()
					}
					b.StopTimer()
					d.mu.Lock()
					b.ReportMetric(float64(d.requests)/float64(b.N), "requests/op")
					d.mu.Unlock()
				})
			}
		})
	}
}
