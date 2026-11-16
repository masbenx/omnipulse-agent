package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
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
		switch strings.ToLower(strings.TrimSpace(dbCfg.Engine)) {
		case "postgresql", "postgres":
			go pollPostgres(client, cfg, logger, dbCfg)
		case "redis":
			go pollRedis(client, cfg, logger, dbCfg)
		}
	}
}

func quoteLibpqValue(v string) string {
	escaped := strings.ReplaceAll(v, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `'`, `\'`)
	return "'" + escaped + "'"
}

func pollPostgres(client *http.Client, cfg Config, logger *log.Logger, dbCfg DBInstanceConfigItem) {
	sslMode := strings.TrimSpace(os.Getenv("OMNIPULSE_DBM_SSLMODE"))
	if sslMode == "" {
		sslMode = "require"
	}
	dsn := fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s connect_timeout=5",
		quoteLibpqValue(dbCfg.Host),
		dbCfg.Port,
		quoteLibpqValue(dbCfg.Username),
		quoteLibpqValue(dbCfg.Password),
		quoteLibpqValue(dbCfg.DatabaseName),
		quoteLibpqValue(sslMode),
	)

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

func pollRedis(client *http.Client, cfg Config, logger *log.Logger, dbCfg DBInstanceConfigItem) {
	port := dbCfg.Port
	if port <= 0 {
		port = 6379
	}
	addr := net.JoinHostPort(dbCfg.Host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		logger.Printf("redis dial error [%s]: %v", dbCfg.Name, err)
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	reader := bufio.NewReader(conn)

	if dbCfg.Password != "" {
		var authCmd []string
		if dbCfg.Username != "" && !strings.EqualFold(dbCfg.Username, "default") {
			authCmd = []string{"AUTH", dbCfg.Username, dbCfg.Password}
		} else {
			authCmd = []string{"AUTH", dbCfg.Password}
		}
		if _, err := redisExec(conn, reader, authCmd...); err != nil {
			logger.Printf("redis auth error [%s]: %v", dbCfg.Name, err)
			return
		}
	}

	infoStr, err := redisExec(conn, reader, "INFO")
	if err != nil {
		logger.Printf("redis info error [%s]: %v", dbCfg.Name, err)
		return
	}

	activeConn := int(parseRedisInfoInt64(infoStr, "connected_clients:"))
	maxClients := int(parseRedisInfoInt64(infoStr, "maxclients:"))
	if maxClients <= 0 {
		maxClients = 10000
	}
	hits := parseRedisInfoInt64(infoStr, "keyspace_hits:")
	misses := parseRedisInfoInt64(infoStr, "keyspace_misses:")
	evicted := parseRedisInfoInt64(infoStr, "evicted_keys:")
	totalConns := parseRedisInfoInt64(infoStr, "total_connections_received:")
	totalCmds := parseRedisInfoInt64(infoStr, "total_commands_processed:")
	usedMem := parseRedisInfoInt64(infoStr, "used_memory:")
	usedMemRSS := parseRedisInfoInt64(infoStr, "used_memory_rss:")

	var hitRatio float64
	if hits+misses > 0 {
		hitRatio = float64(hits) / float64(hits+misses)
	}

	metric := DBMetricIngestReq{
		Timestamp:            time.Now().UTC().Format(time.RFC3339Nano),
		DatabaseInstanceID:   dbCfg.ID,
		ActiveConnections:    activeConn,
		MaxConnections:       maxClients,
		CacheHitRatio:        hitRatio,
		TransactionsCommit:   totalCmds,
		TransactionsRollback: evicted,
		RowsRead:             hits,
		RowsWritten:          misses,
		AdditionalMetrics: map[string]any{
			"used_memory":       usedMem,
			"used_memory_rss":   usedMemRSS,
			"total_connections": totalConns,
			"total_commands":    totalCmds,
			"evicted_keys":      evicted,
			"keyspace_hits":     hits,
			"keyspace_misses":   misses,
		},
	}

	sendDBMetrics(client, cfg, logger, metric)

	if slowEntries, err := redisSlowLog(conn, reader, 20); err == nil && len(slowEntries) > 0 {
		reqs := make([]SlowQueryIngestReq, 0, len(slowEntries))
		for _, se := range slowEntries {
			ms := float64(se.DurationMicros) / 1000.0
			reqs = append(reqs, SlowQueryIngestReq{
				Timestamp:          se.Timestamp.UTC().Format(time.RFC3339Nano),
				DatabaseInstanceID: dbCfg.ID,
				QueryText:          se.Command,
				QueryHash:          fmt.Sprintf("%d", se.ID),
				DBUser:             "redis",
				Calls:              1,
				TotalTimeMs:        ms,
				MinTimeMs:          ms,
				MaxTimeMs:          ms,
				MeanTimeMs:         ms,
				RowsReturned:       1,
			})
		}
		sendSlowQueries(client, cfg, logger, reqs)
	}
}

func redisWriteCmd(conn net.Conn, args ...string) error {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&buf, "$%d\r\n%s\r\n", len(a), a)
	}
	_, err := conn.Write(buf.Bytes())
	return err
}

func redisReadValue(r *bufio.Reader) (any, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if len(line) == 0 {
		return nil, io.EOF
	}
	prefix := line[0]
	rest := line[1:]
	switch prefix {
	case '+':
		return rest, nil
	case '-':
		return nil, fmt.Errorf("redis error: %s", rest)
	case ':':
		v, err := strconv.ParseInt(rest, 10, 64)
		return v, err
	case '$':
		n, err := strconv.Atoi(rest)
		if err != nil {
			return nil, err
		}
		if n < 0 {
			return "", nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		return string(buf[:n]), nil
	case '*':
		n, err := strconv.Atoi(rest)
		if err != nil {
			return nil, err
		}
		if n < 0 {
			return []any(nil), nil
		}
		arr := make([]any, 0, n)
		for i := 0; i < n; i++ {
			v, err := redisReadValue(r)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		return arr, nil
	default:
		return rest, nil
	}
}

func redisExec(conn net.Conn, r *bufio.Reader, args ...string) (string, error) {
	if err := redisWriteCmd(conn, args...); err != nil {
		return "", err
	}
	val, err := redisReadValue(r)
	if err != nil {
		return "", err
	}
	if s, ok := val.(string); ok {
		return s, nil
	}
	return fmt.Sprintf("%v", val), nil
}

type redisSlowLogItem struct {
	ID             int64
	Timestamp      time.Time
	DurationMicros int64
	Command        string
}

func redisSlowLog(conn net.Conn, r *bufio.Reader, count int) ([]redisSlowLogItem, error) {
	if err := redisWriteCmd(conn, "SLOWLOG", "GET", strconv.Itoa(count)); err != nil {
		return nil, err
	}
	val, err := redisReadValue(r)
	if err != nil {
		return nil, err
	}
	arr, ok := val.([]any)
	if !ok {
		return nil, nil
	}
	var out []redisSlowLogItem
	for _, raw := range arr {
		entry, ok := raw.([]any)
		if !ok || len(entry) < 4 {
			continue
		}
		id, _ := entry[0].(int64)
		tsSec, _ := entry[1].(int64)
		dur, _ := entry[2].(int64)
		var cmdParts []string
		if cmdArr, ok := entry[3].([]any); ok {
			for _, p := range cmdArr {
				if s, ok := p.(string); ok {
					cmdParts = append(cmdParts, s)
				}
			}
		}
		out = append(out, redisSlowLogItem{
			ID:             id,
			Timestamp:      time.Unix(tsSec, 0),
			DurationMicros: dur,
			Command:        strings.Join(cmdParts, " "),
		})
	}
	return out, nil
}

func parseRedisInfoInt64(info, prefix string) int64 {
	for _, line := range strings.Split(info, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			raw := strings.TrimSpace(strings.TrimPrefix(line, prefix))
			if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
				return v
			}
		}
	}
	return 0
}
