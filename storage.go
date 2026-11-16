package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

type StorageConfigItem struct {
	ID               int    `json:"id"`
	PublicID         string `json:"public_id"`
	Name             string `json:"name"`
	Provider         string `json:"provider"`
	EndpointURL      string `json:"endpoint_url"`
	S3EndpointURL    string `json:"s3_endpoint_url"`
	FilerEndpointURL string `json:"filer_endpoint_url"`
}

type StorageConfigRes struct {
	Instances []StorageConfigItem `json:"instances"`
}

type StorageMetricIngestReq struct {
	Timestamp         string         `json:"timestamp"`
	StorageInstanceID int            `json:"storage_instance_id"`
	IsHealthy         bool           `json:"is_healthy"`
	IsQuorumOK        bool           `json:"is_quorum_ok"`
	MasterLeader      string         `json:"master_leader"`
	VolumeCount       int            `json:"volume_count"`
	ActiveVolumes     int            `json:"active_volumes"`
	ReadOnlyVolumes   int            `json:"read_only_volumes"`
	TotalBytes        int64          `json:"total_bytes"`
	UsedBytes         int64          `json:"used_bytes"`
	FreeBytes         int64          `json:"free_bytes"`
	FileCount         int64          `json:"file_count"`
	S3Healthy         bool           `json:"s3_healthy"`
	FilerHealthy      bool           `json:"filer_healthy"`
	LatencyMs         float64        `json:"latency_ms"`
	ErrorMessage      string         `json:"error_message"`
	AdditionalMetrics map[string]any `json:"additional_metrics,omitempty"`
}

func fetchStorageConfigs(client *http.Client, cfg Config) ([]StorageConfigItem, error) {
	endpoint := cfg.BaseURL + "/api/ingest/storage-instances/config"
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Agent-Token", cfg.Token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status=%d", resp.StatusCode)
	}

	var res StorageConfigRes
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}
	return res.Instances, nil
}

func pollAllStorage(client *http.Client, cfg Config, logger *log.Logger) {
	configs, err := fetchStorageConfigs(client, cfg)
	if err != nil || len(configs) == 0 {
		if err != nil {
			logger.Printf("storage-config fetch failed: %v", err)
		}
		return
	}

	for _, st := range configs {
		go pollSeaweedFSInstance(client, cfg, logger, st)
	}
}

func pollSeaweedFSInstance(client *http.Client, cfg Config, logger *log.Logger, st StorageConfigItem) {
	localClient := &http.Client{Timeout: 6 * time.Second}
	metric := collectSeaweedFSMetrics(localClient, st)

	body, _ := json.Marshal(metric)
	endpoint := cfg.BaseURL + "/api/ingest/storage-metrics"
	httpReq, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Agent-Token", cfg.Token)

	resp, err := client.Do(httpReq)
	if err != nil {
		logger.Printf("storage metrics ingest failed [%s]: %v", st.Name, err)
		return
	}
	defer resp.Body.Close()
}

func collectSeaweedFSMetrics(localClient *http.Client, st StorageConfigItem) StorageMetricIngestReq {
	start := time.Now()
	metric := StorageMetricIngestReq{
		Timestamp:         time.Now().UTC().Format(time.RFC3339Nano),
		StorageInstanceID: st.ID,
		IsQuorumOK:        true,
	}

	masterURL := strings.TrimRight(st.EndpointURL, "/")
	if masterURL == "" {
		metric.ErrorMessage = "empty master endpoint_url"
		return metric
	}

	resp, err := localClient.Get(masterURL + "/dir/status")
	metric.LatencyMs = float64(time.Since(start).Milliseconds())
	if err != nil {
		metric.ErrorMessage = err.Error()
		return metric
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		metric.ErrorMessage = fmt.Sprintf("master HTTP %d", resp.StatusCode)
		return metric
	}

	var dirStatus struct {
		Topology struct {
			Max         int `json:"Max"`
			Free        int `json:"Free"`
			DataCenters []struct {
				ID    string `json:"Id"`
				Racks []struct {
					DataNodes []struct {
						URL       string `json:"Url"`
						Volumes   int    `json:"Volumes"`
						Max       int    `json:"Max"`
						FileCount int64  `json:"FileCount"`
					} `json:"DataNodes"`
				} `json:"Racks"`
			} `json:"DataCenters"`
		} `json:"Topology"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&dirStatus); err != nil {
		metric.ErrorMessage = fmt.Sprintf("decode dir/status: %v", err)
		return metric
	}

	metric.IsHealthy = true
	var dataNodeURLs []string
	for _, dc := range dirStatus.Topology.DataCenters {
		for _, rack := range dc.Racks {
			for _, dn := range rack.DataNodes {
				metric.VolumeCount += dn.Volumes
				metric.ActiveVolumes += dn.Volumes
				metric.FileCount += dn.FileCount
				if dn.URL != "" {
					dataNodeURLs = append(dataNodeURLs, dn.URL)
				}
			}
		}
	}

	// Check cluster status for leader & quorum
	if cResp, err := localClient.Get(masterURL + "/cluster/status"); err == nil {
		var cStatus struct {
			IsLeader bool   `json:"IsLeader"`
			Leader   string `json:"Leader"`
			Peers    []any  `json:"Peers"`
		}
		if err := json.NewDecoder(cResp.Body).Decode(&cStatus); err == nil {
			metric.MasterLeader = cStatus.Leader
			metric.IsQuorumOK = cStatus.Leader != ""
		}
		cResp.Body.Close()
	}

	// Query volume server /status for disk usage if reachable
	for _, dnURL := range dataNodeURLs {
		target := dnURL
		if !strings.HasPrefix(target, "http") {
			target = "http://" + target
		}
		if vResp, err := localClient.Get(strings.TrimRight(target, "/") + "/status"); err == nil {
			var vStatus struct {
				DiskStatuses []struct {
					All  int64 `json:"all"`
					Used int64 `json:"used"`
					Free int64 `json:"free"`
				} `json:"DiskStatuses"`
			}
			if err := json.NewDecoder(vResp.Body).Decode(&vStatus); err == nil {
				for _, d := range vStatus.DiskStatuses {
					metric.TotalBytes += d.All
					metric.UsedBytes += d.Used
					metric.FreeBytes += d.Free
				}
			}
			vResp.Body.Close()
		}
	}

	if st.S3EndpointURL != "" {
		if s3Resp, err := localClient.Get(strings.TrimRight(st.S3EndpointURL, "/")); err == nil {
			s3Resp.Body.Close()
			metric.S3Healthy = s3Resp.StatusCode < 500
		}
	}
	if st.FilerEndpointURL != "" {
		if fResp, err := localClient.Get(strings.TrimRight(st.FilerEndpointURL, "/")); err == nil {
			fResp.Body.Close()
			metric.FilerHealthy = fResp.StatusCode < 500
		}
	}

	return metric
}
