package unsplash

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const apiBase = "https://api.unsplash.com"

type Client struct {
	AccessKey string
	HTTP      *http.Client
}

type Photo struct {
	ID             string `json:"id"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
	Color          string `json:"color"`
	Description    string `json:"description"`
	AltDescription string `json:"alt_description"`
	URLs           struct {
		Raw string `json:"raw"`
	} `json:"urls"`
	Links struct {
		HTML             string `json:"html"`
		DownloadLocation string `json:"download_location"`
	} `json:"links"`
	User struct {
		Name     string `json:"name"`
		Username string `json:"username"`
	} `json:"user"`
}

type SavedPhoto struct {
	Photo Photo  `json:"photo"`
	Path  string `json:"path"`
}

func New(accessKey string) *Client {
	return &Client{
		AccessKey: strings.TrimSpace(accessKey),
		HTTP: &http.Client{
			Timeout: 45 * time.Second,
		},
	}
}

func (c *Client) RandomPhotos(ctx context.Context, query, contentFilter string, count int) ([]Photo, error) {
	if count < 1 {
		count = 1
	}
	if count > 30 {
		count = 30
	}
	u, _ := url.Parse(apiBase + "/photos/random")
	q := u.Query()
	q.Set("orientation", "landscape")
	q.Set("content_filter", defaultString(contentFilter, "high"))
	q.Set("count", fmt.Sprint(count))
	if strings.TrimSpace(query) != "" {
		q.Set("query", query)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	c.authorize(req)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("unsplash random request failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var photos []Photo
	if err := json.NewDecoder(resp.Body).Decode(&photos); err != nil {
		return nil, err
	}
	return photos, nil
}

func (c *Client) Download(ctx context.Context, p Photo, dir string, minWidth, minHeight, index int) (string, error) {
	if p.URLs.Raw == "" {
		return "", fmt.Errorf("photo %s has no raw URL", p.ID)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	downloadURL, err := sizedURL(p.URLs.Raw, minWidth, minHeight)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "cecunsplash/1.0")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("download %s failed: %s: %s", p.ID, resp.Status, strings.TrimSpace(string(body)))
	}

	name := fmt.Sprintf("%s_%02d_%s.jpg", time.Now().Format("20060102_150405"), index+1, safeName(p.ID))
	finalPath := filepath.Join(dir, name)
	tmpPath := finalPath + ".tmp"
	out, err := os.Create(tmpPath)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(out, resp.Body)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return "", copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return "", closeErr
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	_ = c.TrackDownload(ctx, p)
	return finalPath, nil
}

func (c *Client) TrackDownload(ctx context.Context, p Photo) error {
	if p.Links.DownloadLocation == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.Links.DownloadLocation, nil)
	if err != nil {
		return err
	}
	c.authorize(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("track download failed: %s", resp.Status)
	}
	return nil
}

func (c *Client) authorize(req *http.Request) {
	req.Header.Set("Authorization", "Client-ID "+c.AccessKey)
	req.Header.Set("Accept-Version", "v1")
	req.Header.Set("User-Agent", "cecunsplash/1.0")
}

func sizedURL(raw string, width, height int) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("auto", "format")
	q.Set("fit", "crop")
	q.Set("crop", "entropy")
	q.Set("w", fmt.Sprint(width))
	q.Set("h", fmt.Sprint(height))
	q.Set("q", "90")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "photo"
	}
	return b.String()
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
