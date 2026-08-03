package unsplash

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// smallJPEG encodes a w x h solid-color JPEG into a byte slice.
func smallJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	c := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf strings.Builder
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 75}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return []byte(buf.String())
}

func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	c := New("test-access-key")
	c.SetBaseURL(srv.URL)
	c.HTTP = srv.Client()
	return c
}

func TestRandomPhotosAndDownload(t *testing.T) {
	imgBytes := smallJPEG(t, 8, 6)

	mux := http.NewServeMux()
	mux.HandleFunc("/photos/random", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Client-ID test-access-key" {
			t.Errorf("bad auth header: %q", r.Header.Get("Authorization"))
		}
		photos := []Photo{
			{
				ID:     "abc123",
				Width:  3840,
				Height: 2160,
				URLs: struct {
					Raw string `json:"raw"`
				}{Raw: ""}, // set below
			},
		}
		// Inject the test server base URL into the raw URL.
		photos[0].URLs.Raw = "http://" + r.Host + "/raw/abc123.jpg"
		photos[0].Links.DownloadLocation = "http://" + r.Host + "/download/abc123"
		photos[0].User.Name = "Test User"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(photos)
	})
	mux.HandleFunc("/raw/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(imgBytes)
	})
	downloadHits := 0
	mux.HandleFunc("/download/", func(w http.ResponseWriter, r *http.Request) {
		downloadHits++
		if r.Header.Get("Authorization") != "Client-ID test-access-key" {
			t.Errorf("download track bad auth: %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv)

	photos, err := c.RandomPhotos(context.Background(), "mountains", "high", 1)
	if err != nil {
		t.Fatalf("RandomPhotos: %v", err)
	}
	if len(photos) != 1 || photos[0].ID != "abc123" {
		t.Fatalf("got photos %#v", photos)
	}

	dir := t.TempDir()
	path, err := c.Download(context.Background(), photos[0], dir, 8, 6, 0)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("downloaded file missing at %s: %v", path, err)
	}
	if filepath.Ext(path) != ".jpg" {
		t.Errorf("downloaded file not jpg: %s", path)
	}
	if downloadHits != 1 {
		t.Errorf("download tracking called %d times, want 1", downloadHits)
	}
}

func TestDownloadRejectsTooSmallImage(t *testing.T) {
	imgBytes := smallJPEG(t, 4, 4)
	mux := http.NewServeMux()
	mux.HandleFunc("/raw/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(imgBytes)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv)
	p := Photo{ID: "x", URLs: struct {
		Raw string `json:"raw"`
	}{Raw: srv.URL + "/raw/x.jpg"}}
	// Require 100x100, served image is only 4x4.
	_, err := c.Download(context.Background(), p, t.TempDir(), 100, 100, 0)
	if err == nil {
		t.Fatal("expected download to be rejected for too-small image")
	}
	if !strings.Contains(err.Error(), "below required minimum") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRandomPhotosHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"errors":["Unauthorized"]}`)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	_, err := c.RandomPhotos(context.Background(), "", "high", 1)
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention status: %v", err)
	}
}

func TestSafeName(t *testing.T) {
	if got := safeName("a/b c?é"); strings.ContainsAny(got, "/ ?") {
		t.Errorf("safeName did not sanitize: %q", got)
	}
	if safeName("") != "photo" {
		t.Errorf("safeName empty = %q, want photo", safeName(""))
	}
}

func TestSizedURL(t *testing.T) {
	raw := "https://images.unsplash.com/photo-1?ixlib=foo"
	got, err := sizedURL(raw, 3840, 2160)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"w=3840", "h=2160", "fm=jpg", "q=90"} {
		if !strings.Contains(got, want) {
			t.Errorf("sizedURL missing %q in %s", want, got)
		}
	}
}

// avoid unused fmt import if future edits drop it.
var _ = fmt.Sprintf
