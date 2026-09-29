package main

import (
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	updateUserAgent     = "Cline2API-Updater"
	updateMaxDownload   = 200 << 20
	updateCacheDuration = 2 * time.Minute
	updateRestartDelay  = 900 * time.Millisecond
)

var (
	// Release CI binds updates to this fork, never to a different project's binary.
	updateRepository     = "zcgg2001/cline2api"
	updateChannel        = "development" // CI sets "release" only for tagged desktop builds.
	errUpdateBusy        = errors.New("已有更新任务正在执行")
	errUpdateLatest      = errors.New("当前已是最新版本")
	errUpdateUnsupported = errors.New("当前运行环境不支持在线更新")
	updateMu             sync.Mutex
	updateCacheMu        sync.Mutex
	updateCache          *githubRelease
	updateCacheExpires   time.Time
	updateHTTPClient     = &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("更新请求重定向次数过多")
		}
		return validateUpdateURL(req.URL.String())
	}}
	updateRestartError string // protected by updateCacheMu
)

type githubRelease struct {
	TagName     string        `json:"tag_name"`
	Name        string        `json:"name"`
	Body        string        `json:"body"`
	PublishedAt string        `json:"published_at"`
	HTMLURL     string        `json:"html_url"`
	Assets      []githubAsset `json:"assets"`
	Draft       bool          `json:"draft"`
	Prerelease  bool          `json:"prerelease"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
	Size               int64  `json:"size"`
}

type systemUpdateInfo struct {
	CurrentVersion    string `json:"currentVersion"`
	LatestVersion     string `json:"latestVersion"`
	HasUpdate         bool   `json:"hasUpdate"`
	Supported         bool   `json:"supported"`
	UnsupportedReason string `json:"unsupportedReason,omitempty"`
	RuntimeOS         string `json:"runtimeOS"`
	RuntimeArch       string `json:"runtimeArch"`
	Mode              string `json:"mode"`
	ReleaseURL        string `json:"releaseURL,omitempty"`
	AssetName         string `json:"assetName,omitempty"`
	PublishedAt       string `json:"publishedAt,omitempty"`
	ReleaseNotes      string `json:"releaseNotes,omitempty"`
	Warning           string `json:"warning,omitempty"`
	CheckStatus       string `json:"checkStatus"`
	Repository        string `json:"repository"`
	CheckedAt         string `json:"checkedAt,omitempty"`
}

type systemUpdateResult struct {
	Message        string `json:"message"`
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion"`
	NeedRestart    bool   `json:"needRestart"`
	Restarting     bool   `json:"restarting"`
	Mode           string `json:"mode"`
	BackupPath     string `json:"backupPath,omitempty"`
}

// GET /admin/api/system/update
func handleAdminUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	info, _, err := inspectSystemUpdate(r.Context())
	if err != nil {
		// 检查更新是辅助能力，网络暂时不可用时仍返回当前版本和可解释状态。
		info = unavailableSystemUpdate()
		info.Warning = err.Error()
	}
	updateCacheMu.Lock()
	if updateRestartError != "" {
		info.Warning = updateRestartError
	}
	updateCacheMu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: info})
}

// POST /admin/api/system/update/apply. A version-bound JSON confirmation prevents
// cross-origin form submissions and silently installing a different release.
func handleAdminUpdateApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeAPI(w, 415, apiResponse{Error: "需要 application/json 确认更新"})
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
			writeAPI(w, 403, apiResponse{Error: "不允许跨站更新请求"})
			return
		}
	}
	var confirmation struct {
		Version string `json:"version"`
		Confirm bool   `json:"confirm"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&confirmation) != nil || !confirmation.Confirm || confirmation.Version == "" {
		writeAPI(w, 400, apiResponse{Error: "请确认目标版本"})
		return
	}
	if !updateMu.TryLock() {
		writeAPI(w, http.StatusConflict, apiResponse{Error: errUpdateBusy.Error()})
		return
	}
	restarting := false
	defer func() {
		if !restarting {
			updateMu.Unlock()
		}
	}()

	info, asset, err := inspectSystemUpdate(r.Context())
	if err != nil {
		writeAPI(w, http.StatusBadGateway, apiResponse{Error: "检查更新失败：" + err.Error()})
		return
	}
	if !info.Supported {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: info.UnsupportedReason})
		return
	}
	if !info.HasUpdate {
		writeAPI(w, http.StatusConflict, apiResponse{Error: errUpdateLatest.Error()})
		return
	}
	if normalizeUpdateVersion(confirmation.Version) != info.LatestVersion {
		writeAPI(w, 409, apiResponse{Error: "发布版本已变化，请重新检查并确认"})
		return
	}
	if asset == nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: "未找到当前系统对应的发布文件"})
		return
	}

	// Once confirmed, closing the browser does not interrupt a file replacement.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	exePath, backupPath, err := applySystemUpdate(ctx, asset)
	if err != nil {
		writeAPI(w, http.StatusInternalServerError, apiResponse{Error: "在线更新失败：" + err.Error()})
		return
	}
	result := systemUpdateResult{
		Message:        "更新已应用，服务即将重启",
		CurrentVersion: info.CurrentVersion,
		LatestVersion:  info.LatestVersion,
		NeedRestart:    true,
		Restarting:     true,
		Mode:           info.Mode,
		BackupPath:     backupPath,
	}
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: result, Message: result.Message})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	restarting = true
	go scheduleSystemRestart(exePath, backupPath)
}

func inspectSystemUpdate(ctx context.Context) (*systemUpdateInfo, *githubAsset, error) {
	current := normalizeUpdateVersion(appVersion)
	info := &systemUpdateInfo{
		CurrentVersion: current,
		RuntimeOS:      runtime.GOOS,
		RuntimeArch:    runtime.GOARCH,
		Mode:           "binary",
		Supported:      true,
		Repository:     updateRepository,
		CheckStatus:    "ok",
	}
	if _, ok := parseUpdateVersion(current); !ok {
		info.Supported = false
		info.UnsupportedReason = "开发构建未注入版本号，无法安全判断升级目标"
	}
	if updateChannel != "release" {
		info.Supported = false
		info.UnsupportedReason = "本地开发/自编译版本仅支持检查更新；请使用本仓库的正式发行版，避免覆盖定制代码"
		info.Mode = "development"
	}
	if runtime.GOOS == "windows" {
		info.Supported = false
		info.UnsupportedReason = "Windows 运行时暂不支持在线替换正在运行的可执行文件"
	}
	exePath, err := os.Executable()
	if err != nil {
		info.Supported = false
		info.UnsupportedReason = "无法确定当前程序路径"
	}
	if err == nil && isGoRunExecutable(exePath) {
		info.Supported = false
		info.UnsupportedReason = "当前是 go run 开发进程，在线更新仅支持已发布的桌面可执行文件"
		info.Mode = "development"
	}
	if strings.Contains(filepath.ToSlash(exePath), ".app/Contents/MacOS/") {
		info.Supported = false
		info.UnsupportedReason = "macOS 应用包请下载完整安装包更新，避免破坏应用签名"
	}
	if runningInUpdateContainer() {
		info.Mode = "container"
		info.Supported = false
		info.UnsupportedReason = "容器内替换程序会在重建后丢失，请拉取新镜像并重新部署"
	}

	release, err := fetchLatestGitHubRelease(ctx)
	if err != nil {
		return nil, nil, err
	}
	latest := normalizeUpdateVersion(release.TagName)
	if _, ok := parseUpdateVersion(latest); !ok || release.Draft || release.Prerelease {
		return nil, nil, errors.New("最新 Release 缺少有效版本号")
	}
	info.LatestVersion = latest
	_, currentValid := parseUpdateVersion(current)
	info.HasUpdate = currentValid && compareUpdateVersions(current, latest) < 0
	info.ReleaseURL = "https://github.com/" + updateRepository + "/releases/tag/" + url.PathEscape(release.TagName)
	info.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	info.PublishedAt = release.PublishedAt
	info.ReleaseNotes = release.Body
	asset := findUpdateAsset(release)
	if asset != nil {
		info.AssetName = asset.Name
		if !validUpdateDigest(asset.Digest) || validateReleaseAssetURL(asset.BrowserDownloadURL, release.TagName, asset.Name) != nil || asset.Size <= 0 || asset.Size > updateMaxDownload {
			info.Supported = false
			info.UnsupportedReason = "发布文件缺少有效的 SHA-256、大小或可信下载地址，不能在线安装"
		}
	} else if info.Supported {
		info.Supported = false
		info.UnsupportedReason = fmt.Sprintf("未找到适配 %s/%s 的发布文件", runtime.GOOS, runtime.GOARCH)
	}
	return info, asset, nil
}

func unavailableSystemUpdate() *systemUpdateInfo {
	current := normalizeUpdateVersion(appVersion)
	return &systemUpdateInfo{
		CurrentVersion:    current,
		RuntimeOS:         runtime.GOOS,
		RuntimeArch:       runtime.GOARCH,
		Mode:              "binary",
		Supported:         false,
		UnsupportedReason: "暂时无法连接 GitHub Releases",
		CheckStatus:       "error",
		Repository:        updateRepository,
	}
}

func fetchLatestGitHubRelease(ctx context.Context) (*githubRelease, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	updateCacheMu.Lock()
	defer updateCacheMu.Unlock()
	if updateCache != nil && time.Now().Before(updateCacheExpires) {
		copy := *updateCache
		copy.Assets = append([]githubAsset(nil), updateCache.Assets...)
		return &copy, nil
	}
	endpoint := "https://api.github.com/repos/" + updateRepository + "/releases/latest"
	if err := validateUpdateURL(endpoint); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", updateUserAgent)
	resp, err := updateHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == 404 {
			return nil, errors.New("当前仓库尚未发布正式 Release，或仓库不可访问")
		}
		if resp.StatusCode == 403 || resp.StatusCode == 429 {
			return nil, errors.New("GitHub 检查限流，请稍后重试")
		}
		return nil, fmt.Errorf("GitHub API 返回 HTTP %d", resp.StatusCode)
	}
	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&release); err != nil {
		return nil, err
	}
	updateCache = &release
	updateCacheExpires = time.Now().Add(updateCacheDuration)
	return &release, nil
}

func findUpdateAsset(release *githubRelease) *githubAsset {
	if release == nil {
		return nil
	}
	name := ""
	switch {
	case runtime.GOOS == "darwin" && runtime.GOARCH == "arm64":
		name = "cline-proxy-desktop-darwin-arm64"
	case runtime.GOOS == "linux" && runtime.GOARCH == "amd64":
		name = "cline-proxy-desktop-linux-amd64"
	case runtime.GOOS == "windows" && runtime.GOARCH == "amd64":
		name = "cline-proxy-desktop.exe"
	default:
		return nil
	}
	for i := range release.Assets {
		if release.Assets[i].Name == name {
			return &release.Assets[i]
		}
	}
	return nil
}

func applySystemUpdate(ctx context.Context, asset *githubAsset) (string, string, error) {
	if asset == nil || asset.BrowserDownloadURL == "" {
		return "", "", errors.New("发布文件地址为空")
	}
	if err := validateUpdateURL(asset.BrowserDownloadURL); err != nil {
		return "", "", err
	}
	exePath, err := os.Executable()
	if err != nil {
		return "", "", fmt.Errorf("获取当前程序路径失败: %w", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return "", "", fmt.Errorf("解析当前程序路径失败: %w", err)
	}
	tempDir, err := os.MkdirTemp(filepath.Dir(exePath), ".cline2api-update-*")
	if err != nil {
		return "", "", fmt.Errorf("创建更新临时目录失败: %w", err)
	}
	defer os.RemoveAll(tempDir)
	tempPath := filepath.Join(tempDir, filepath.Base(exePath))
	if err := downloadUpdateFile(ctx, asset.BrowserDownloadURL, tempPath); err != nil {
		return "", "", err
	}
	if err := verifyUpdateDigest(tempPath, asset.Digest); err != nil {
		return "", "", err
	}
	stat, err := os.Stat(tempPath)
	if err != nil || stat.Size() != asset.Size {
		return "", "", errors.New("更新文件大小与发布信息不符")
	}
	if err := validateUpdateBinary(tempPath); err != nil {
		return "", "", err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(tempPath, 0o755); err != nil {
			return "", "", err
		}
	}
	backupPath, err := replaceUpdateExecutable(exePath, tempPath)
	if err != nil {
		return "", "", err
	}
	return exePath, backupPath, nil
}

func downloadUpdateFile(ctx context.Context, rawURL, dest string) error {
	if err := validateUpdateURL(rawURL); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", updateUserAgent)
	resp, err := updateHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载更新文件返回 HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > updateMaxDownload {
		return fmt.Errorf("更新文件超过 %d MB", updateMaxDownload>>20)
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(out, io.LimitReader(resp.Body, updateMaxDownload+1))
	if copyErr == nil {
		copyErr = out.Sync()
	}
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(dest)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(dest)
		return closeErr
	}
	if written > updateMaxDownload {
		_ = os.Remove(dest)
		return errors.New("更新文件超过大小限制")
	}
	return nil
}

func verifyUpdateDigest(path, digest string) error {
	if !validUpdateDigest(digest) {
		return errors.New("Release 必须提供有效 SHA-256 校验和")
	}
	digest = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(digest)), "sha256:")
	if len(digest) != sha256.Size*2 {
		return errors.New("Release 校验和格式无效")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return errors.New("Release 校验和格式无效")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), digest) {
		return errors.New("更新文件校验和不匹配，已取消替换")
	}
	return nil
}

func validateUpdateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return errors.New("更新地址必须使用 HTTPS")
	}
	host := strings.ToLower(u.Hostname())
	if host != "api.github.com" && host != "github.com" && host != "objects.githubusercontent.com" && host != "release-assets.githubusercontent.com" {
		return fmt.Errorf("不允许的更新域名: %s", host)
	}
	return nil
}

func normalizeUpdateVersion(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "refs/tags/")
	value = strings.TrimPrefix(strings.TrimPrefix(value, "v"), "V")
	return value
}

func parseUpdateVersion(value string) ([3]int, bool) {
	var result [3]int
	value = normalizeUpdateVersion(value)
	if !regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`).MatchString(value) {
		return result, false
	}
	segments := strings.Split(value, ".")
	for i, segment := range segments {
		n, err := strconv.Atoi(segment)
		if err != nil {
			return result, false
		}
		result[i] = n
	}
	return result, true
}

func compareUpdateVersions(a, b string) int {
	left, lok := parseUpdateVersion(a)
	right, rok := parseUpdateVersion(b)
	if !lok || !rok {
		return 0 // Unknown/development versions must never imply an upgrade.
	}
	for i := range left {
		if left[i] < right[i] {
			return -1
		}
		if left[i] > right[i] {
			return 1
		}
	}
	return 0
}

func isGoRunExecutable(path string) bool {
	p := strings.ToLower(filepath.ToSlash(path))
	return strings.Contains(p, "/go-build/") || strings.Contains(p, "/go-build")
}

func scheduleSystemRestart(exePath, backupPath string) {
	time.Sleep(updateRestartDelay)
	// Exec replaces this process: no second listener races the old listener.
	// If exec fails the old process stays alive and its binary is restored.
	err := restartUpdatedExecutable(exePath)
	rollbackErr := os.Rename(backupPath, exePath)
	message := fmt.Sprintf("重启失败，旧进程仍在运行：%v", err)
	if rollbackErr != nil {
		message += fmt.Sprintf("；恢复程序失败，请从 %s 手动恢复：%v", backupPath, rollbackErr)
	} else {
		message += "；原程序已恢复"
	}
	log.Print(message)
	updateCacheMu.Lock()
	updateRestartError = message
	updateCacheMu.Unlock()
	updateMu.Unlock()
}

func validUpdateDigest(digest string) bool {
	if !strings.HasPrefix(digest, "sha256:") {
		return false
	}
	b, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	return err == nil && len(b) == sha256.Size
}

func validateReleaseAssetURL(raw, tag, name string) error {
	if err := validateUpdateURL(raw); err != nil {
		return err
	}
	u, _ := url.Parse(raw)
	if u.Host != "github.com" || u.RawQuery != "" || u.Path != "/"+updateRepository+"/releases/download/"+tag+"/"+name {
		return errors.New("下载文件必须来自本仓库对应 Release")
	}
	return nil
}

func runningInUpdateContainer() bool {
	for _, path := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	data, _ := os.ReadFile("/proc/1/cgroup")
	for _, marker := range []string{"docker", "kubepods", "containerd"} {
		if strings.Contains(string(data), marker) {
			return true
		}
	}
	return false
}

func validateUpdateBinary(path string) error {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return fmt.Errorf("更新文件不是 Go 可执行程序: %w", err)
	}
	if info.Main.Path != "cline-go-proxy" {
		return errors.New("更新文件不是 Cline2API 程序")
	}
	settings := map[string]string{}
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	if settings["GOOS"] != runtime.GOOS || settings["GOARCH"] != runtime.GOARCH || !strings.Contains(settings["-tags"], "desktop") {
		return errors.New("更新文件平台或构建类型不匹配")
	}
	return nil
}

// A uniquely named backup is copied and fsynced first. The final rename leaves
// no missing-executable window and never deletes an earlier recovery copy.
func replaceUpdateExecutable(exePath, newPath string) (string, error) {
	src, err := os.Open(exePath)
	if err != nil {
		return "", err
	}
	defer src.Close()
	stat, err := src.Stat()
	if err != nil {
		return "", err
	}
	backup, err := os.CreateTemp(filepath.Dir(exePath), filepath.Base(exePath)+".backup-*")
	if err != nil {
		return "", err
	}
	backupPath := backup.Name()
	_, copyErr := io.Copy(backup, src)
	if copyErr == nil {
		copyErr = backup.Chmod(stat.Mode().Perm())
	}
	if copyErr == nil {
		copyErr = backup.Sync()
	}
	closeErr := backup.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(backupPath)
		return "", fmt.Errorf("备份当前程序失败: %v / %v", copyErr, closeErr)
	}
	if err := os.Rename(newPath, exePath); err != nil {
		return "", fmt.Errorf("替换失败，原程序未变；备份在 %s: %w", backupPath, err)
	}
	return backupPath, nil
}
