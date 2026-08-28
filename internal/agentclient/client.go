package agentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/SakuraOpenSource/virtualis/internal/model"
)

// Instance is the small, stable wire representation shared by master and agent.
// It deliberately excludes database associations and credentials.
type Instance struct {
	ID          uint                `json:"id"`
	Name        string              `json:"name"`
	DisplayName string              `json:"display_name,omitempty"`
	Driver      string              `json:"driver"`
	Type        string              `json:"type"`
	Status      string              `json:"status,omitempty"`
	ImageID     *uint               `json:"image_id,omitempty"`
	Spec        model.InstanceSpec  `json:"spec"`
	Network     model.NetworkConfig `json:"network"`
	Image       *Image              `json:"image,omitempty"`
}

// Image contains the metadata an agent needs to attach an image locally.
// FilePath is intentionally omitted from requests because it belongs to the
// master filesystem. When a file is uploaded, the agent fills in its local path.
type Image struct {
	ID           uint   `json:"id,omitempty"`
	Name         string `json:"name"`
	DisplayName  string `json:"display_name,omitempty"`
	Driver       string `json:"driver"`
	Type         string `json:"type"`
	OriginalName string `json:"original_name,omitempty"`
	SizeBytes    int64  `json:"size_bytes,omitempty"`
	Checksum     string `json:"checksum,omitempty"`
	Path         string `json:"path,omitempty"`
}

// Driver is an agent-side capability report.
type Driver struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Error     string `json:"error,omitempty"`
}

// HostInterface 是被控主机上的一个网卡，供独立 IP 模式选择挂载目标。
type HostInterface struct {
	Name  string   `json:"name"`
	Kind  string   `json:"kind"` // bridge / physical / vlan / other
	State string   `json:"state"`
	MAC   string   `json:"mac,omitempty"`
	IPv4  []string `json:"ipv4,omitempty"`
	IPv6  []string `json:"ipv6,omitempty"`
}

// HostNetworkSummary 汇总被控主机网络。
type HostNetworkSummary struct {
	Interfaces []HostInterface `json:"interfaces"`
	// IPv4Count 是全部非 lo 网卡的 IPv4 地址数；独立 IP 模式要求 >= 2。
	IPv4Count int `json:"ipv4_count"`
}

type Metrics struct {
	CPUPercent     float64   `json:"cpu_percent"`
	MemoryUsedMB   int64     `json:"memory_used_mb"`
	MemoryTotalMB  int64     `json:"memory_total_mb"`
	NetworkRxBytes uint64    `json:"network_rx_bytes"`
	NetworkTxBytes uint64    `json:"network_tx_bytes"`
	BandwidthRxBps float64   `json:"bandwidth_rx_bps"`
	BandwidthTxBps float64   `json:"bandwidth_tx_bps"`
	CollectedAt    time.Time `json:"collected_at"`
}

type NetworkInterface struct {
	Name    string   `json:"name"`
	MAC     string   `json:"mac,omitempty"`
	State   string   `json:"state,omitempty"`
	IPv4    []string `json:"ipv4,omitempty"`
	IPv6    []string `json:"ipv6,omitempty"`
	RxBytes uint64   `json:"rx_bytes"`
	TxBytes uint64   `json:"tx_bytes"`
}

type NetworkStatus struct {
	Reachable  bool               `json:"reachable"`
	LatencyMS  float64            `json:"latency_ms"`
	Interfaces []NetworkInterface `json:"interfaces"`
	Error      string             `json:"error,omitempty"`
	CheckedAt  time.Time          `json:"checked_at"`
}

type VNCInfo struct {
	Available bool   `json:"available"`
	Protocol  string `json:"protocol,omitempty"`
	Host      string `json:"host,omitempty"`
	Port      int    `json:"port,omitempty"`
	Display   string `json:"display,omitempty"`
	URL       string `json:"url,omitempty"`
	WebURL    string `json:"web_url,omitempty"`
	Message   string `json:"message,omitempty"`
}

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// New validates an agent endpoint before it is used for server-side requests.
func New(endpoint, token string) (*Client, error) {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("被控地址必须是 http:// 或 https:// 地址")
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("被控 token 不能为空")
	}
	return &Client{
		baseURL:    endpoint,
		token:      token,
		httpClient: &http.Client{Timeout: 90 * time.Second},
	}, nil
}

func (c *Client) endpoint(p string) string {
	return c.baseURL + "/api/" + strings.TrimLeft(p, "/")
}

func (c *Client) newRequest(ctx context.Context, method, p string, body io.Reader, contentType string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint(p), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Agent-Token", c.token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req, nil
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("连接被控失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		msg := e.Error
		if msg == "" {
			msg = e.Message
		}
		if msg == "" {
			msg = resp.Status
		}
		if resp.StatusCode == http.StatusNotFound {
			return fmt.Errorf("被控节点版本过旧，缺少当前接口（%s），请重启最新 virtualis-agent", req.URL.Path)
		}
		return fmt.Errorf("被控请求失败: %s", msg)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("解析被控响应失败: %w", err)
	}
	return nil
}

func (c *Client) Drivers(ctx context.Context) ([]Driver, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "drivers", nil, "")
	if err != nil {
		return nil, err
	}
	var out struct {
		Items []Driver `json:"items"`
	}
	if err := c.do(req, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// HostNetwork 拉取被控主机的网卡与地址清单。
func (c *Client) HostNetwork(ctx context.Context) (*HostNetworkSummary, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "host/network", nil, "")
	if err != nil {
		return nil, err
	}
	var out struct {
		Network HostNetworkSummary `json:"network"`
	}
	if err := c.do(req, &out); err != nil {
		return nil, err
	}
	return &out.Network, nil
}

func (c *Client) CreateInstance(ctx context.Context, instance Instance, image *Image, imageFile io.Reader, filename string) (Instance, error) {
	instance.Image = image
	var out struct {
		Instance Instance `json:"instance"`
	}
	if imageFile == nil {
		payload, err := json.Marshal(struct {
			Instance Instance `json:"instance"`
		}{Instance: instance})
		if err != nil {
			return Instance{}, err
		}
		req, err := c.newRequest(ctx, http.MethodPost, "instances", bytes.NewReader(payload), "application/json")
		if err != nil {
			return Instance{}, err
		}
		if err := c.do(req, &out); err != nil {
			return Instance{}, err
		}
		return out.Instance, nil
	}

	body, contentType, err := multipartBody("instance", instance, "image", imageFile, filename)
	if err != nil {
		return Instance{}, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "instances", body, contentType)
	if err != nil {
		return Instance{}, err
	}
	if err := c.do(req, &out); err != nil {
		return Instance{}, err
	}
	return out.Instance, nil
}

func (c *Client) DeleteInstance(ctx context.Context, instance Instance) error {
	payload, err := json.Marshal(struct {
		Instance Instance `json:"instance"`
	}{Instance: instance})
	if err != nil {
		return err
	}
	req, err := c.newRequest(ctx, http.MethodDelete, "instances/"+fmt.Sprint(instance.ID), bytes.NewReader(payload), "application/json")
	if err != nil {
		return err
	}
	return c.do(req, nil)
}

func (c *Client) PowerInstance(ctx context.Context, instance Instance, action string, image *Image, imageFile io.Reader, filename string) (Instance, error) {
	instance.Image = image
	var out struct {
		Instance Instance `json:"instance"`
	}
	if imageFile == nil {
		payload, err := json.Marshal(struct {
			Action   string   `json:"action"`
			Instance Instance `json:"instance"`
		}{Action: action, Instance: instance})
		if err != nil {
			return Instance{}, err
		}
		req, err := c.newRequest(ctx, http.MethodPost, "instances/"+fmt.Sprint(instance.ID)+"/power", bytes.NewReader(payload), "application/json")
		if err != nil {
			return Instance{}, err
		}
		if err := c.do(req, &out); err != nil {
			return Instance{}, err
		}
		return out.Instance, nil
	}
	body, contentType, err := multipartBody("power", struct {
		Action   string   `json:"action"`
		Instance Instance `json:"instance"`
	}{Action: action, Instance: instance}, "image", imageFile, filename)
	if err != nil {
		return Instance{}, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "instances/"+fmt.Sprint(instance.ID)+"/power", body, contentType)
	if err != nil {
		return Instance{}, err
	}
	if err := c.do(req, &out); err != nil {
		return Instance{}, err
	}
	return out.Instance, nil
}

func (c *Client) Status(ctx context.Context, instance Instance) (Instance, error) {
	payload, err := json.Marshal(struct {
		Instance Instance `json:"instance"`
	}{Instance: instance})
	if err != nil {
		return Instance{}, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "instances/"+fmt.Sprint(instance.ID)+"/status", bytes.NewReader(payload), "application/json")
	if err != nil {
		return Instance{}, err
	}
	var out struct {
		Instance Instance `json:"instance"`
	}
	if err := c.do(req, &out); err != nil {
		return Instance{}, err
	}
	return out.Instance, nil
}

func (c *Client) Metrics(ctx context.Context, instance Instance) (Metrics, error) {
	payload, err := json.Marshal(struct {
		Instance Instance `json:"instance"`
	}{Instance: instance})
	if err != nil {
		return Metrics{}, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "instances/"+fmt.Sprint(instance.ID)+"/metrics", bytes.NewReader(payload), "application/json")
	if err != nil {
		return Metrics{}, err
	}
	var out struct {
		Metrics Metrics `json:"metrics"`
	}
	if err := c.do(req, &out); err != nil {
		return Metrics{}, err
	}
	return out.Metrics, nil
}

func (c *Client) Network(ctx context.Context, instance Instance) (NetworkStatus, error) {
	payload, err := json.Marshal(struct {
		Instance Instance `json:"instance"`
	}{Instance: instance})
	if err != nil {
		return NetworkStatus{}, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "instances/"+fmt.Sprint(instance.ID)+"/network", bytes.NewReader(payload), "application/json")
	if err != nil {
		return NetworkStatus{}, err
	}
	var out struct {
		Network NetworkStatus `json:"network"`
	}
	if err := c.do(req, &out); err != nil {
		return NetworkStatus{}, err
	}
	return out.Network, nil
}

func (c *Client) VNC(ctx context.Context, instance Instance) (VNCInfo, error) {
	payload, err := json.Marshal(struct {
		Instance Instance `json:"instance"`
	}{Instance: instance})
	if err != nil {
		return VNCInfo{}, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "instances/"+fmt.Sprint(instance.ID)+"/vnc", bytes.NewReader(payload), "application/json")
	if err != nil {
		return VNCInfo{}, err
	}
	var out struct {
		VNC VNCInfo `json:"vnc"`
	}
	if err := c.do(req, &out); err != nil {
		return VNCInfo{}, err
	}
	return out.VNC, nil
}

func multipartBody(field string, value any, fileField string, file io.Reader, filename string) (io.Reader, string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	filename = filepath.Base(strings.TrimSpace(filename))
	if filename == "." || filename == "" {
		filename = "image.bin"
	}
	reader, writer := io.Pipe()
	w := multipart.NewWriter(writer)
	contentType := w.FormDataContentType()
	go func() {
		if err := w.WriteField(field, string(raw)); err != nil {
			_ = writer.CloseWithError(err)
			return
		}
		part, err := w.CreateFormFile(fileField, filename)
		if err != nil {
			_ = writer.CloseWithError(err)
			return
		}
		if _, err := io.Copy(part, file); err != nil {
			_ = writer.CloseWithError(err)
			return
		}
		if err := w.Close(); err != nil {
			_ = writer.CloseWithError(err)
			return
		}
		_ = writer.Close()
	}()
	return reader, contentType, nil
}
