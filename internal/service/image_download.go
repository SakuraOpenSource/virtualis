package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/SakuraOpenSource/virtualis/internal/model"
)

// 镜像下载：管理员给 URL（或选预设源），主控后台下载到本地镜像库，
// 之后与上传镜像走同一条创建链路。Incus 分割镜像（meta + rootfs/disk）
// 用 ExtraURL 同时抓取第二个文件。

const imageDownloadTimeout = 12 * time.Hour

// DownloadImageRequest 是发起一次镜像下载的参数。
type DownloadImageRequest struct {
	Name      string `json:"name"`
	Driver    string `json:"driver"`
	Type      string `json:"type"`
	OSType    string `json:"os_type"`
	OSVersion string `json:"os_version"`
	Arch      string `json:"arch"`
	URL       string `json:"url"`
	// ExtraURL 是 Incus 分割镜像的 meta.tar.xz 地址；URL 指向 rootfs.tar.xz
	// 或 disk.qcow2。
	ExtraURL string `json:"extra_url"`
}

// imagePreset 是预设源里的一条可下载镜像。
type imagePreset struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	URL         string `json:"url"`
	ExtraURL    string `json:"extra_url,omitempty"`
	OSType      string `json:"os_type,omitempty"`
	OSVersion   string `json:"os_version,omitempty"`
	Arch        string `json:"arch,omitempty"`
	Note        string `json:"note,omitempty"`
}

// MagicBoxImageBase 是魔方云官方镜像站（qcow2，QEMU 直接可用）。
const MagicBoxImageBase = "https://mirror.cloud.idcsmart.com/cloud/images/init-images"

// magicBoxPresets 是实测可下载的魔方云镜像（名称规律 <系统>-<版本>-x64）。
func magicBoxPresets() []imagePreset {
	items := []struct{ name, display, os, ver string }{
		{"Debian-12.0_x64", "Debian 12 (bookworm)", "debian", "12"},
		{"Debian-11.1-x64", "Debian 11 (bullseye)", "debian", "11"},
		{"Debian-10.3.3-x64", "Debian 10 (buster)", "debian", "10"},
		{"Ubuntu-24.04.1-x64", "Ubuntu 24.04 LTS", "ubuntu", "24.04"},
		{"Ubuntu-22.04-x64", "Ubuntu 22.04 LTS", "ubuntu", "22.04"},
		{"Ubuntu-20.04.1-x64", "Ubuntu 20.04 LTS", "ubuntu", "20.04"},
		{"Ubuntu-18.04-x64", "Ubuntu 18.04 LTS", "ubuntu", "18.04"},
		{"CentOS-9-Stream-x64", "CentOS Stream 9", "centos", "9"},
		{"AlmaLinux-9.2-x64", "AlmaLinux 9.2", "almalinux", "9.2"},
		{"openEuler-24.03-LTS", "openEuler 24.03 LTS", "openeuler", "24.03"},
	}
	out := make([]imagePreset, 0, len(items))
	for _, it := range items {
		out = append(out, imagePreset{
			Name:        it.name,
			DisplayName: it.display,
			URL:         MagicBoxImageBase + "/" + it.name + ".qcow2",
			OSType:      it.os,
			OSVersion:   it.ver,
			Arch:        "x86_64",
		})
	}
	return out
}

// TUNAIncusBase 是清华大学 LXC/Incus 镜像源根地址。
const TUNAIncusBase = "https://mirrors.tuna.tsinghua.edu.cn/lxc-images/images"

// incusDistroPresets 是 TUNA 上常用发行版的预设参数。
func incusDistroPresets() []imagePreset {
	return []imagePreset{
		{Name: "debian", DisplayName: "Debian"},
		{Name: "ubuntu", DisplayName: "Ubuntu"},
		{Name: "almalinux", DisplayName: "AlmaLinux"},
		{Name: "rockylinux", DisplayName: "Rocky Linux"},
		{Name: "centos", DisplayName: "CentOS Stream"},
		{Name: "oracle", DisplayName: "Oracle Linux"},
		{Name: "opensuse", DisplayName: "openSUSE"},
		{Name: "alpine", DisplayName: "Alpine"},
		{Name: "fedora", DisplayName: "Fedora"},
		{Name: "archlinux", DisplayName: "Arch Linux"},
	}
}

// ImagePresets 解析预设源。
//
// driver=qemu：返回魔方云镜像列表。
// driver=incus：无 distro 返回发行版列表；带 distro/release/arch 逐级
// 返回下一级目录；带全参数解析最新构建并给出容器/VM 两组下载地址。
func (s *VirtualisService) ImagePresets(ctx context.Context, driver, distro, release, arch, variant string) (any, error) {
	switch driver {
	case model.DriverQEMU, "":
		return map[string]any{"items": magicBoxPresets()}, nil
	case model.DriverIncus:
		if distro == "" {
			return map[string]any{"items": incusDistroPresets()}, nil
		}
		if release == "" {
			return tunaSubDirs(ctx, TUNAIncusBase+"/"+url.PathEscape(distro))
		}
		if arch == "" {
			return tunaSubDirs(ctx, TUNAIncusBase+"/"+url.PathEscape(distro)+"/"+url.PathEscape(release))
		}
		if variant == "" {
			return tunaSubDirs(ctx, TUNAIncusBase+"/"+url.PathEscape(distro)+"/"+url.PathEscape(release)+"/"+url.PathEscape(arch))
		}
		base := fmt.Sprintf("%s/%s/%s/%s/%s", TUNAIncusBase, url.PathEscape(distro), url.PathEscape(release), url.PathEscape(arch), url.PathEscape(variant))
		builds, err := tunaListDirs(ctx, base)
		if err != nil {
			return nil, BadRequest("读取镜像源失败: %s", err.Error())
		}
		if len(builds) == 0 {
			return nil, BadRequest("该组合下没有可用构建")
		}
		latest := builds[len(builds)-1] // 目录名字典序=时间序，取最新
		buildURL := base + "/" + url.PathEscape(latest)
		nameBase := fmt.Sprintf("%s-%s-%s-%s", distro, release, variant, arch)
		meta := buildURL + "/meta.tar.xz"
		return map[string]any{
			"build": latest,
			"items": []imagePreset{
				{
					Name: nameBase + "-container", DisplayName: nameBase + "（容器）",
					URL: buildURL + "/rootfs.tar.xz", ExtraURL: meta,
					OSType: distro, OSVersion: release, Arch: arch,
					Note: "meta.tar.xz + rootfs.tar.xz",
				},
				{
					Name: nameBase + "-vm", DisplayName: nameBase + "（虚拟机）",
					URL: buildURL + "/disk.qcow2", ExtraURL: meta,
					OSType: distro, OSVersion: release, Arch: arch,
					Note: "创建实例时类型选 vm",
				},
			},
		}, nil
	}
	return nil, BadRequest("invalid driver %q", driver)
}

// tunaSubDirs 抓取并返回目录列表的统一包装。
func tunaSubDirs(ctx context.Context, base string) (any, error) {
	dirs, err := tunaListDirs(ctx, base)
	if err != nil {
		return nil, BadRequest("读取镜像源失败: %s", err.Error())
	}
	items := make([]imagePreset, 0, len(dirs))
	for _, d := range dirs {
		items = append(items, imagePreset{Name: d, DisplayName: d})
	}
	return map[string]any{"items": items}, nil
}

var tunaDirPattern = regexp.MustCompile(`href="([^"?/][^"?]*)/"`)

// tunaListDirs 抓取 TUNA fancyindex 目录页的子目录名，字典序返回
// （日期构建目录天然满足字典序=时间序）。
func tunaListDirs(ctx context.Context, base string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/", nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var dirs []string
	for _, m := range tunaDirPattern.FindAllSubmatch(raw, -1) {
		name := string(m[1])
		if name == "" || name == ".." || name == "." || seen[name] {
			continue
		}
		seen[name] = true
		dirs = append(dirs, name)
	}
	sort.Strings(dirs)
	return dirs, nil
}

// DownloadImage 建立 downloading 记录并启动后台下载；
// 进度经 GET /images 的 status 字段轮询（downloading → available/error）。
func (s *VirtualisService) DownloadImage(req DownloadImageRequest) (*model.Image, error) {
	if s.storage == nil {
		return nil, Internal("image storage unavailable")
	}
	req.Name = strings.TrimSpace(req.Name)
	req.URL = strings.TrimSpace(req.URL)
	if req.Name == "" {
		return nil, BadRequest("请填写镜像名称")
	}
	if req.URL == "" || !strings.HasPrefix(req.URL, "http") {
		return nil, BadRequest("请填写有效的镜像下载地址")
	}
	if u, err := url.Parse(req.URL); err != nil || u.Host == "" {
		return nil, BadRequest("镜像下载地址无效")
	}
	extra := strings.TrimSpace(req.ExtraURL)
	driverName, err := normalizeImageDriver(req.Driver)
	if err != nil {
		return nil, err
	}
	kind := normalizeImageType(req.Type, req.URL, "")
	if extra != "" && driverName != model.DriverIncus {
		return nil, BadRequest("只有 Incus 分割镜像需要第二文件地址")
	}
	image := &model.Image{
		Name:         req.Name,
		DisplayName:  req.Name,
		Driver:       driverName,
		Type:         kind,
		OSType:       strings.TrimSpace(req.OSType),
		OSVersion:    strings.TrimSpace(req.OSVersion),
		Arch:         strings.TrimSpace(req.Arch),
		OriginalName: fileNameFromURL(req.URL),
		Status:       model.ImageStatusDownloading,
		IsPublic:     true,
	}
	if err := s.db.Create(image).Error; err != nil {
		return nil, err
	}
	go s.runImageDownload(image, req)
	return image, nil
}

// runImageDownload 执行下载并回写状态；panic 兜底避免拖垮主进程。
func (s *VirtualisService) runImageDownload(image *model.Image, req DownloadImageRequest) {
	defer func() {
		if r := recover(); r != nil {
			s.failImageDownload(image, "下载过程异常退出")
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), imageDownloadTimeout)
	defer cancel()

	path, size, _, checksum, err := s.downloadTo(ctx, req.URL)
	if err != nil {
		s.failImageDownload(image, err.Error())
		return
	}
	updates := map[string]any{
		"status": model.ImageStatusAvailable, "file_path": path,
		"size_bytes": size, "checksum": checksum,
	}
	if extra := strings.TrimSpace(req.ExtraURL); extra != "" {
		extraPath, _, _, _, err := s.downloadTo(ctx, extra)
		if err != nil {
			s.failImageDownload(image, "meta 文件下载失败: "+err.Error())
			return
		}
		updates["extra_path"] = extraPath
	}
	if err := s.db.Model(image).Updates(updates).Error; err != nil {
		s.failImageDownload(image, err.Error())
	}
}

// downloadTo 流式下载 URL 到镜像存储，返回 (相对路径, 大小, mime, sha256, err)。
func (s *VirtualisService) downloadTo(ctx context.Context, rawURL string) (string, int64, string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", 0, "", "", err
	}
	resp, err := (&http.Client{Timeout: imageDownloadTimeout}).Do(req)
	if err != nil {
		return "", 0, "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", 0, "", "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return s.storage.SaveNamed("images", fileNameFromURL(rawURL), resp.Body, maxImageUploadSize)
}

func (s *VirtualisService) failImageDownload(image *model.Image, message string) {
	detail, _ := json.Marshal(map[string]string{"error": message})
	_ = s.db.Model(image).Updates(map[string]any{
		"status":      model.ImageStatusError,
		"description": string(detail),
	}).Error
}

// fileNameFromURL 从 URL 末段取文件名（去掉查询串）。
func fileNameFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "image.bin"
	}
	parts := strings.Split(u.Path, "/")
	if last := parts[len(parts)-1]; last != "" {
		return last
	}
	return "image.bin"
}
