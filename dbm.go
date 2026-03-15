package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	_ "github.com/lib/pq"
)

type DBInstanceConfigItem struct {
	ID           int    `json:"id"`
	PublicID     string `json:"public_id"`
	Name         string `json:"name"`
	Engine       string `json:"engine"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	DatabaseName string `json:"database_name"`
	Username     string `json:"username"`
	Password     string `json:"password"`
}

type DBInstanceConfigRes struct {
	Instances []DBInstanceConfigItem `json:"instances"`
}

type DBMetricIngestReq struct {
	Timestamp             string         `json:"timestamp"`
	DatabaseInstanceID    int            `json:"database_instance_id"`
	ActiveConnections     int            `json:"active_connections"`
	MaxConnections        int            `json:"max_connections"`
	CacheHitRatio         float64        `json:"cache_hit_ratio"`
	TransactionsCommit    int64          `json:"transactions_commit"`
	TransactionsRollback  int64          `json:"transactions_rollback"`
	Deadlocks             int64          `json:"deadlocks"`
	RowsRead              int64          `json:"rows_read"`
	RowsWritten           int64          `json:"rows_written"`
	AdditionalMetrics     map[string]any `json:"additional_metrics"`
}

type SlowQueryIngestReq struct {
	Timestamp          string  `json:"timestamp"`
	DatabaseInstanceID int     `json:"database_instance_id"`
	QueryText          string  `json:"query_text"`
	QueryHash          string  `json:"query_hash"`
	DBUser             string  `json:"db_user"`
	Calls              int64   `json:"calls"`
	TotalTimeMs        float64 `json:"total_time_ms"`
	MinTimeMs          float64 `json:"min_time_ms"`
	MaxTimeMs          float64 `json:"max_time_ms"`
	MeanTimeMs         float64 `json:"mean_time_ms"`
	RowsReturned       int64   `json:"rows_returned"`
}

func fetchDBConfigs(client *http.Client, cfg Config) ([]DBInstanceConfigItem, error) {
	endpoint := cfg.BaseURL + "/api/ingest/db-instances/config"
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

	var res DBInstanceConfigRes
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}

	return res.Instances, nil
}

func pollAllDatabases(client *http.Client, cfg Config, logger *log.Logger) {
	configs, err := fetchDBConfigs(client, cfg)
	if err != nil || len(configs) == 0 {
		if err != nil {
			logger.Printf("db-config fetch failed: %v", err)
		}
		return
	}

	for _, dbCfg := range configs {
		if dbCfg.Engine == "postgresql" {
			go pollPostgres(client, cfg, logger, dbCfg)
		}
	}
}

func pollPostgres(client *http.Client, cfg Config, logger *log.Logger, dbCfg DBInstanceConfigItem) {
	dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable connect_timeout=5",
		dbCfg.Host, dbCfg.Port, dbCfg.Username, dbCfg.Password, dbCfg.DatabaseName)

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		logger.Printf("postgres open error [%s]: %v", dbCfg.Name, err)
		return
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		logger.Printf("postgres ping error [%s]: %v", dbCfg.Name, err)
		return
	}

	// Fetch Overall DB Metrics
	var activeConns, maxConns int
	err = db.QueryRowContext(ctx, "SELECT count(*) FROM pg_stat_activity WHERE datname = $1", dbCfg.DatabaseName).Scan(&activeConns)
	if err != nil {
		logger.Printf("postgres query activity error [%s]: %v", dbCfg.Name, err)
	}

	err = db.QueryRowContext(ctx, "SHOW max_connections").Scan(&maxConns)
	if err != nil {
		maxConns = 100 // fallback
	}

	var xactCommit, xactRollback, blksRead, blksHit, deadlocks int64
	var tupReturned, tupFetched, tupInserted, tupUpdated, tupDeleted int64
	err = db.QueryRowContext(ctx, `
		SELECT xact_commit, xact_rollback, blks_read, blks_hit, deadlocks, 
		       tup_returned, tup_fetched, tup_inserted, tup_updated, tup_deleted
		FROM pg_stat_database WHERE datname = $1`, dbCfg.DatabaseName).
		Scan(&xactCommit, &xactRollback, &blksRead, &blksHit, &deadlocks,
			&tupReturned, &tupFetched, &tupInserted, &tupUpdated, &tupDeleted)

	if err != nil {
		logger.Printf("postgres stat_database error [%s]: %v", dbCfg.Name, err)
	}

	var cacheHitRatio float64 = 0
	if blksRead+blksHit > 0 {
		cacheHitRatio = float64(blksHit) / float64(blksRead+blksHit)
	}

	metric := DBMetricIngestReq{
		Timestamp:            time.Now().UTC().Format(time.RFC3339Nano),
		DatabaseInstanceID:   dbCfg.ID,
		ActiveConnections:    activeConns,
		MaxConnections:       maxConns,
		CacheHitRatio:        cacheHitRatio,
		TransactionsCommit:   xactCommit,
		TransactionsRollback: xactRollback,
		Deadlocks:            deadlocks,
		RowsRead:             tupReturned + tupFetched,
		RowsWritten:          tupInserted + tupUpdated + tupDeleted,
	}

	sendDBMetrics(client, cfg, logger, metric)

	// Fetch Slow Queries (via pg_stat_statements)
	// Requires pg_stat_statements extension
	rows, err := db.QueryContext(ctx, `
		SELECT queryid, query, calls, total_exec_time, min_exec_time, max_exec_time, mean_exec_time, rows
		FROM pg_stat_statements
		WHERE dbid = (SELECT datid FROM pg_stat_database WHERE datname = $1)
		ORDER BY mean_exec_time DESC LIMIT 20`, dbCfg.DatabaseName)
		
	if err == nil {
		defer rows.Close()
		var queries []SlowQueryIngestReq
		for rows.Next() {
			var qid int64
			var query string
			var calls, rowsReturned int64
			var totalTime, minTime, maxTime, meanTime float64
			if err := rows.Scan(&qid, &query, &calls, &totalTime, &minTime, &maxTime, &meanTime, &rowsReturned); err == nil {
				queries = append(queries, SlowQueryIngestReq{
					Timestamp:          time.Now().UTC().Format(time.RFC3339Nano),
					DatabaseInstanceID: dbCfg.ID,
					QueryText:          query,
					QueryHash:          fmt.Sprintf("%d", qid),
					DBUser:             dbCfg.Username,
					Calls:              calls,
					TotalTimeMs:        totalTime,
					MinTimeMs:          minTime,
					MaxTimeMs:          maxTime,
					MeanTimeMs:         meanTime,
					RowsReturned:       rowsReturned,
				})
			}
		}
		
		if len(queries) > 0 {
			sendSlowQueries(client, cfg, logger, queries)
		}
	}
}

func sendDBMetrics(client *http.Client, cfg Config, logger *log.Logger, req DBMetricIngestReq) {
	body, _ := json.Marshal(req)
	endpoint := cfg.BaseURL + "/api/ingest/db-metrics"
	httpReq, _ := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Agent-Token", cfg.Token)

	resp, err := client.Do(httpReq)
	if err != nil {
		logger.Printf("db metrics ingest failed: %v", err)
		return
	}
	defer resp.Body.Close()
}

func sendSlowQueries(client *http.Client, cfg Config, logger *log.Logger, reqs []SlowQueryIngestReq) {
	body, _ := json.Marshal(reqs)
	endpoint := cfg.BaseURL + "/api/ingest/slow-queries"
	httpReq, _ := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Agent-Token", cfg.Token)

	resp, err := client.Do(httpReq)
	if err != nil {
		logger.Printf("slow queries ingest failed: %v", err)
		return
	}
	defer resp.Body.Close()
}
