package drive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSDKV3MetadataAndProperties(t *testing.T) {
	for _, etag := range []string{"", `"header-tag"`} {
		t.Run(etag, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "PATCH" {
					if r.Header.Get("If-Match") != etag || etag == "" {
						t.Error("missing precondition")
					}
					var patch map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
						t.Error(err)
					}
					var public, private map[string]string
					_ = json.Unmarshal(patch["properties"], &public)
					_ = json.Unmarshal(patch["appProperties"], &private)
					if len(patch) != 2 || public["gdrive-repo"] != "directory" || public["unrelated"] != "keep" || private["gdrive-repo"] != "private-directory" {
						t.Error("lost property visibility or changed unrelated fields", patch)
					}
					fmt.Fprint(w, `{"id":"root"}`)
					return
				}
				if r.URL.Path != "/drive/v3/files/root" || strings.Contains(r.URL.Query().Get("fields"), "etag") {
					t.Error("invalid v3 metadata request", r.URL)
				}
				w.Header().Set("ETag", etag)
				fmt.Fprint(w, `{"id":"root","name":"repo","mimeType":"application/vnd.google-apps.folder","parents":["parent"],"properties":{"gdrive-repo":"directory","unrelated":"keep"},"appProperties":{"gdrive-repo":"private-directory"},"version":"123"}`)
			}))
			defer server.Close()
			api := backendForTest(t, server, "sdkv3")
			f, err := api.Metadata(context.Background(), "root")
			if err != nil || f.ETag != etag || !f.in("parent") || f.Title != "repo" || f.value("gdrive-repo") != "directory" {
				t.Fatal(f, err)
			}
			err = api.Patch(context.Background(), "root", f.ETag, File{Properties: f.Properties})
			if (err == nil) != (etag != "") {
				t.Fatal("missing ETag must fail closed; version is not an ETag", err)
			}
		})
	}
}

func TestSDKV3ArchiveValidation(t *testing.T) {
	for _, kind := range []string{"valid", "no-etag", "moved", "renamed", "trashed", "wrong-type", "duplicate", "incomplete"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/files") {
					if r.URL.Query().Get("q") != "'folder' in parents and name = 'main.zip' and trashed = false" || r.URL.Query().Get("pageSize") != "1000" {
						t.Error("invalid v3 list query", r.URL)
					}
					w.Header().Set("ETag", "list-etag-must-not-be-used")
					if kind == "incomplete" {
						fmt.Fprint(w, `{"files":[],"incompleteSearch":true}`)
						return
					}
					if kind == "duplicate" {
						fmt.Fprint(w, `{"files":[{"id":"zip","mimeType":"application/zip"},{"id":"other","mimeType":"application/zip"}]}`)
						return
					}
					fmt.Fprint(w, `{"files":[{"id":"zip","mimeType":"application/zip"}]}`)
					return
				}
				if kind != "no-etag" {
					w.Header().Set("ETag", "file-etag")
				}
				name, parent, mime := "main.zip", "folder", "application/zip"
				if kind == "renamed" {
					name = "other.zip"
				}
				if kind == "moved" {
					parent = "unrelated"
				}
				if kind == "wrong-type" {
					mime = folderMIME
				}
				json.NewEncoder(w).Encode(map[string]any{"id": "zip", "name": name, "parents": []string{parent}, "mimeType": mime, "trashed": kind == "trashed"})
			}))
			defer server.Close()
			api := backendForTest(t, server, "sdkv3")
			f, err := api.FindArchive(context.Background(), "folder", "main.zip")
			if kind == "valid" {
				if err != nil || f.ETag != "file-etag" {
					t.Fatal(f, err)
				}
			} else if err == nil {
				t.Fatal("accepted unsafe archive", f)
			}
		})
	}
}
