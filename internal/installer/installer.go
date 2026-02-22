package installer

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type Options struct {
	Repo    string
	Version string
	DestDir string
	Force   bool
}

type release struct {
	TagName string  `json:"tag_name"`
	Assets  []asset `json:"assets"`
}

type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

func Install(ctx context.Context, opts Options) (installedPath, tag string, err error) {
	repo := strings.TrimSpace(opts.Repo)
	if repo == "" {
		return "", "", errors.New("repo is required")
	}
	version := strings.TrimSpace(opts.Version)
	if version == "" {
		version = "latest"
	}
	destDir := strings.TrimSpace(opts.DestDir)
	if destDir == "" {
		destDir = "."
	}

	rel, err := fetchRelease(ctx, repo, version)
	if err != nil {
		return "", "", err
	}
	if rel.TagName == "" {
		rel.TagName = version
	}

	selected, err := chooseAsset(rel.Assets, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", rel.TagName, err
	}

	tmpDir, err := os.MkdirTemp("", "health-node-core-")
	if err != nil {
		return "", rel.TagName, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	downloadPath := filepath.Join(tmpDir, selected.Name)
	if err := downloadFile(ctx, selected.URL, downloadPath); err != nil {
		return "", rel.TagName, err
	}

	binName := expectedBinaryName(repo)
	destPath := filepath.Join(destDir, binName)
	if !opts.Force {
		if _, statErr := os.Stat(destPath); statErr == nil {
			return "", rel.TagName, fmt.Errorf("destination already exists: %s (use --force to overwrite)", destPath)
		}
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", rel.TagName, fmt.Errorf("create destination dir: %w", err)
	}

	extractedPath, err := extractBinary(downloadPath, tmpDir, binName)
	if err != nil {
		return "", rel.TagName, err
	}

	if err := copyExecutable(extractedPath, destPath); err != nil {
		return "", rel.TagName, err
	}
	return destPath, rel.TagName, nil
}

func fetchRelease(ctx context.Context, repo, version string) (*release, error) {
	var apiURL string
	if strings.EqualFold(version, "latest") {
		apiURL = fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	} else {
		apiURL = fmt.Sprintf("https://api.github.com/repos/%s/releases/tags/%s", repo, version)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "health-node")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch release metadata: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("github API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var rel release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("decode release metadata: %w", err)
	}
	if len(rel.Assets) == 0 {
		return nil, errors.New("release has no assets")
	}
	return &rel, nil
}

func chooseAsset(assets []asset, goos, goarch string) (asset, error) {
	osTokens := tokensForOS(goos)
	archTokens := tokensForArch(goarch)

	var candidates []asset
	for _, a := range assets {
		name := strings.ToLower(a.Name)
		if !isArchive(name) {
			continue
		}
		if !containsAny(name, osTokens) {
			continue
		}
		if !containsAny(name, archTokens) {
			continue
		}
		candidates = append(candidates, a)
	}

	if len(candidates) == 0 {
		var names []string
		for _, a := range assets {
			names = append(names, a.Name)
		}
		return asset{}, fmt.Errorf("no matching release asset for %s/%s, assets: %s", goos, goarch, strings.Join(names, ", "))
	}

	for _, c := range candidates {
		if strings.HasSuffix(strings.ToLower(c.Name), ".zip") {
			return c, nil
		}
	}
	return candidates[0], nil
}

func expectedBinaryName(repo string) string {
	v := strings.ToLower(repo)
	if strings.Contains(v, "v2ray") {
		return "v2ray"
	}
	return "xray"
}

func extractBinary(archivePath, workDir, preferredName string) (string, error) {
	lower := strings.ToLower(archivePath)
	if strings.HasSuffix(lower, ".zip") {
		return extractFromZip(archivePath, workDir, preferredName)
	}
	if strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz") {
		return extractFromTarGz(archivePath, workDir, preferredName)
	}
	return "", fmt.Errorf("unsupported archive format: %s", archivePath)
}

func extractFromZip(path, workDir, preferredName string) (string, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("open zip: %w", err)
	}
	defer r.Close()

	var fallback string
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		base := strings.ToLower(filepath.Base(f.Name))
		if base == "" {
			continue
		}
		if isLikelyCoreBinary(base, preferredName) {
			dst := filepath.Join(workDir, filepath.Base(f.Name))
			if err := extractZipFile(f, dst); err != nil {
				return "", err
			}
			return dst, nil
		}
		if fallback == "" && isExecutableName(base) {
			dst := filepath.Join(workDir, filepath.Base(f.Name))
			if err := extractZipFile(f, dst); err != nil {
				return "", err
			}
			fallback = dst
		}
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", errors.New("core binary not found inside zip archive")
}

func extractZipFile(f *zip.File, dst string) error {
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("open zip member: %w", err)
	}
	defer rc.Close()

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create extracted file: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, rc); err != nil {
		return fmt.Errorf("extract zip member: %w", err)
	}
	return nil
}

func extractFromTarGz(path, workDir, preferredName string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open tar.gz: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("open gzip stream: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var fallback string
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read tar member: %w", err)
		}
		if h.FileInfo().IsDir() {
			continue
		}
		base := strings.ToLower(filepath.Base(h.Name))
		if isLikelyCoreBinary(base, preferredName) {
			dst := filepath.Join(workDir, filepath.Base(h.Name))
			if err := writeTarMember(tr, dst); err != nil {
				return "", err
			}
			return dst, nil
		}
		if fallback == "" && isExecutableName(base) {
			dst := filepath.Join(workDir, filepath.Base(h.Name))
			if err := writeTarMember(tr, dst); err != nil {
				return "", err
			}
			fallback = dst
		}
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", errors.New("core binary not found inside tar.gz archive")
}

func writeTarMember(r io.Reader, dst string) error {
	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create extracted file: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, r); err != nil {
		return fmt.Errorf("extract tar member: %w", err)
	}
	return nil
}

func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open extracted binary: %w", err)
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create destination binary: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copy binary: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close destination binary: %w", err)
	}
	if err := os.Chmod(dst, 0o755); err != nil {
		return fmt.Errorf("chmod destination binary: %w", err)
	}
	return nil
}

func tokensForOS(goos string) []string {
	switch goos {
	case "linux":
		return []string{"linux"}
	case "darwin":
		return []string{"darwin", "macos", "osx"}
	case "windows":
		return []string{"windows", "win"}
	default:
		return []string{goos}
	}
}

func tokensForArch(goarch string) []string {
	switch goarch {
	case "amd64":
		return []string{"amd64", "x86_64", "64"}
	case "386":
		return []string{"386", "i386", "32"}
	case "arm64":
		return []string{"arm64", "aarch64"}
	case "arm":
		return []string{"armv7", "armv6", "arm"}
	default:
		return []string{goarch}
	}
}

func isArchive(name string) bool {
	return strings.HasSuffix(name, ".zip") || strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".tgz")
}

func containsAny(v string, tokens []string) bool {
	for _, t := range tokens {
		if strings.Contains(v, t) {
			return true
		}
	}
	return false
}

func isLikelyCoreBinary(base, preferredName string) bool {
	base = strings.TrimSuffix(strings.ToLower(base), ".exe")
	preferred := strings.ToLower(preferredName)
	if base == preferred {
		return true
	}
	if base == "xray" || base == "v2ray" {
		return true
	}
	return false
}

func isExecutableName(base string) bool {
	base = strings.ToLower(base)
	if strings.HasSuffix(base, ".dat") || strings.HasSuffix(base, ".json") || strings.HasSuffix(base, ".txt") {
		return false
	}
	if strings.Contains(base, "geoip") || strings.Contains(base, "geosite") {
		return false
	}
	return true
}

func downloadFile(ctx context.Context, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build download request: %w", err)
	}
	req.Header.Set("User-Agent", "health-node")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download asset: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("download failed with %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create download file: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return fmt.Errorf("write download file: %w", err)
	}
	return nil
}
