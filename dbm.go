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
	"sync"
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
	ArchitectureMetadata  map[string]any `json:"architecture_metadata,omitempty"`
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

	if shouldCollectArchitecture(dbCfg.ID) {
		if arch := collectPostgresArchitecture(ctx, db, maxConns); len(arch) > 0 {
			metric.ArchitectureMetadata = arch
			markArchitectureCollected(dbCfg.ID)
		}
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

	if shouldCollectArchitecture(dbCfg.ID) {
		if arch := collectRedisArchitecture(infoStr, conn, reader); len(arch) > 0 {
			metric.ArchitectureMetadata = arch
			markArchitectureCollected(dbCfg.ID)
		}
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

var (
	archMu            sync.Mutex
	archLastCollected = make(map[int]time.Time)
)

const archCollectInterval = 5 * time.Minute

func shouldCollectArchitecture(instanceID int) bool {
	archMu.Lock()
	defer archMu.Unlock()
	last, ok := archLastCollected[instanceID]
	if !ok {
		return true
	}
	return time.Since(last) >= archCollectInterval
}

func markArchitectureCollected(instanceID int) {
	archMu.Lock()
	defer archMu.Unlock()
	archLastCollected[instanceID] = time.Now()
}

type pgArchTable struct {
	Schema      string         `json:"schema"`
	Name        string         `json:"name"`
	RowEstimate int64          `json:"row_estimate"`
	TotalBytes  int64          `json:"total_bytes"`
	Columns     []pgArchColumn `json:"columns"`
}

type pgArchColumn struct {
	Name       string `json:"name"`
	DataType   string `json:"data_type"`
	IsNullable bool   `json:"is_nullable"`
	IsPK       bool   `json:"is_pk"`
}

type pgArchRelation struct {
	ConstraintName string `json:"constraint_name"`
	FromTable      string `json:"from_table"`
	FromColumn     string `json:"from_column"`
	ToTable        string `json:"to_table"`
	ToColumn       string `json:"to_column"`
}

type pgArchExtension struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func collectPostgresArchitecture(ctx context.Context, db *sql.DB, maxConns int) map[string]any {
	var pgVersion string
	_ = db.QueryRowContext(ctx, "SELECT version()").Scan(&pgVersion)

	var dbSizeBytes int64
	_ = db.QueryRowContext(ctx, "SELECT pg_database_size(current_database())").Scan(&dbSizeBytes)

	var totalViews, totalIndexes int
	_ = db.QueryRowContext(ctx, "SELECT count(*) FROM information_schema.views WHERE table_schema NOT IN ('pg_catalog', 'information_schema')").Scan(&totalViews)
	_ = db.QueryRowContext(ctx, "SELECT count(*) FROM pg_indexes WHERE schemaname NOT IN ('pg_catalog', 'information_schema')").Scan(&totalIndexes)

	extensions := make([]pgArchExtension, 0)
	if extRows, err := db.QueryContext(ctx, "SELECT extname, extversion FROM pg_extension ORDER BY extname"); err == nil {
		defer extRows.Close()
		for extRows.Next() {
			var name, ver string
			if err := extRows.Scan(&name, &ver); err == nil {
				extensions = append(extensions, pgArchExtension{Name: name, Version: ver})
			}
		}
	}

	pkSet := make(map[string]bool)
	if pkRows, err := db.QueryContext(ctx, `
		SELECT tc.table_schema, tc.table_name, kcu.column_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
		  ON tc.constraint_name = kcu.constraint_name
		 AND tc.table_schema = kcu.table_schema
		WHERE tc.constraint_type = 'PRIMARY KEY'
		  AND tc.table_schema NOT IN ('pg_catalog', 'information_schema')
	`); err == nil {
		defer pkRows.Close()
		for pkRows.Next() {
			var schema, table, col string
			if err := pkRows.Scan(&schema, &table, &col); err == nil {
				pkSet[schema+"."+table+"."+col] = true
			}
		}
	}

	colMap := make(map[string][]pgArchColumn)
	if colRows, err := db.QueryContext(ctx, `
		SELECT table_schema, table_name, column_name, data_type, is_nullable
		FROM information_schema.columns
		WHERE table_schema NOT IN ('pg_catalog', 'information_schema')
		ORDER BY table_schema, table_name, ordinal_position
	`); err == nil {
		defer colRows.Close()
		for colRows.Next() {
			var schema, table, col, dtype, nullable string
			if err := colRows.Scan(&schema, &table, &col, &dtype, &nullable); err == nil {
				key := schema + "." + table
				colMap[key] = append(colMap[key], pgArchColumn{
					Name:       col,
					DataType:   dtype,
					IsNullable: strings.EqualFold(nullable, "YES"),
					IsPK:       pkSet[key+"."+col],
				})
			}
		}
	}

	tables := make([]pgArchTable, 0)
	var totalTables int
	_ = db.QueryRowContext(ctx, "SELECT count(*) FROM pg_stat_user_tables").Scan(&totalTables)

	if tblRows, err := db.QueryContext(ctx, `
		SELECT schemaname, relname, COALESCE(n_live_tup, 0), pg_total_relation_size(relid)
		FROM pg_stat_user_tables
		ORDER BY pg_total_relation_size(relid) DESC, relname ASC
		LIMIT 60
	`); err == nil {
		defer tblRows.Close()
		for tblRows.Next() {
			var schema, name string
			var rowEst, totalBytes int64
			if err := tblRows.Scan(&schema, &name, &rowEst, &totalBytes); err == nil {
				key := schema + "." + name
				cols := colMap[key]
				if cols == nil {
					cols = []pgArchColumn{}
				}
				tables = append(tables, pgArchTable{
					Schema:      schema,
					Name:        name,
					RowEstimate: rowEst,
					TotalBytes:  totalBytes,
					Columns:     cols,
				})
			}
		}
	}

	relations := make([]pgArchRelation, 0)
	if relRows, err := db.QueryContext(ctx, `
		SELECT tc.constraint_name,
		       tc.table_schema || '.' || tc.table_name AS from_table,
		       kcu.column_name AS from_column,
		       ccu.table_schema || '.' || ccu.table_name AS to_table,
		       ccu.column_name AS to_column
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
		  ON tc.constraint_name = kcu.constraint_name
		 AND tc.table_schema = kcu.table_schema
		JOIN information_schema.constraint_column_usage ccu
		  ON ccu.constraint_name = tc.constraint_name
		 AND ccu.table_schema = tc.table_schema
		WHERE tc.constraint_type = 'FOREIGN KEY'
		  AND tc.table_schema NOT IN ('pg_catalog', 'information_schema')
		ORDER BY from_table, kcu.column_name
		LIMIT 200
	`); err == nil {
		defer relRows.Close()
		for relRows.Next() {
			var cname, fromT, fromC, toT, toC string
			if err := relRows.Scan(&cname, &fromT, &fromC, &toT, &toC); err == nil {
				relations = append(relations, pgArchRelation{
					ConstraintName: cname,
					FromTable:      fromT,
					FromColumn:     fromC,
					ToTable:        toT,
					ToColumn:       toC,
				})
			}
		}
	}

	if totalTables == 0 {
		totalTables = len(tables)
	}

	return map[string]any{
		"engine":              "postgresql",
		"version":             pgVersion,
		"database_size_bytes": dbSizeBytes,
		"max_connections":     maxConns,
		"total_tables":        totalTables,
		"total_views":         totalViews,
		"total_indexes":       totalIndexes,
		"extensions":          extensions,
		"tables":              tables,
		"relations":           relations,
		"collected_at":        time.Now().UTC().Format(time.RFC3339),
	}
}

func parseRedisInfoMap(info string) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(info, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if idx := strings.IndexByte(line, ':'); idx > 0 {
			k := strings.TrimSpace(line[:idx])
			v := strings.TrimSpace(line[idx+1:])
			out[k] = v
		}
	}
	return out
}

func collectRedisArchitecture(infoStr string, conn net.Conn, reader *bufio.Reader) map[string]any {
	info := parseRedisInfoMap(infoStr)

	mode := info["redis_mode"]
	if mode == "" {
		mode = "standalone"
	}
	role := info["role"]
	if role == "" {
		role = "master"
	}
	connectedSlaves, _ := strconv.ParseInt(info["connected_slaves"], 10, 64)
	maxMemoryBytes, _ := strconv.ParseInt(info["maxmemory"], 10, 64)
	uptimeSeconds, _ := strconv.ParseInt(info["uptime_in_seconds"], 10, 64)
	rdbLastSaveTime, _ := strconv.ParseInt(info["rdb_last_save_time"], 10, 64)
	rdbChanges, _ := strconv.ParseInt(info["rdb_changes_since_last_save"], 10, 64)
	aofEnabled := info["aof_enabled"] == "1"
	rdbEnabled := info["rdb_last_bgsave_status"] != "" || info["rdb_last_save_time"] != ""

	maxMemoryPolicy := info["maxmemory_policy"]
	if maxMemoryPolicy == "" {
		if err := redisWriteCmd(conn, "CONFIG", "GET", "maxmemory-policy"); err == nil {
			if val, err := redisReadValue(reader); err == nil {
				if arr, ok := val.([]any); ok && len(arr) >= 2 {
					if s, ok := arr[1].(string); ok {
						maxMemoryPolicy = s
					}
				}
			}
		}
	}

	type redisKeyspace struct {
		DB     string `json:"db"`
		Keys   int64  `json:"keys"`
		Expires int64 `json:"expires"`
		AvgTTL int64  `json:"avg_ttl"`
	}
	keyspaces := make([]redisKeyspace, 0)
	var totalKeys int64
	for k, v := range info {
		if !strings.HasPrefix(k, "db") || len(k) < 3 {
			continue
		}
		if _, err := strconv.Atoi(k[2:]); err != nil {
			continue
		}
		ks := redisKeyspace{DB: k}
		for _, part := range strings.Split(v, ",") {
			kv := strings.SplitN(part, "=", 2)
			if len(kv) != 2 {
				continue
			}
			val, _ := strconv.ParseInt(kv[1], 10, 64)
			switch kv[0] {
			case "keys":
				ks.Keys = val
			case "expires":
				ks.Expires = val
			case "avg_ttl":
				ks.AvgTTL = val
			}
		}
		totalKeys += ks.Keys
		keyspaces = append(keyspaces, ks)
	}

	return map[string]any{
		"engine":                "redis",
		"version":               info["redis_version"],
		"mode":                  mode,
		"role":                  role,
		"os":                    info["os"],
		"uptime_seconds":        uptimeSeconds,
		"connected_slaves":      connectedSlaves,
		"aof_enabled":           aofEnabled,
		"rdb_enabled":           rdbEnabled,
		"rdb_last_save_time":    rdbLastSaveTime,
		"rdb_changes_unsaved":   rdbChanges,
		"maxmemory_bytes":       maxMemoryBytes,
		"maxmemory_policy":      maxMemoryPolicy,
		"total_keys":            totalKeys,
		"keyspaces":             keyspaces,
		"collected_at":          time.Now().UTC().Format(time.RFC3339),
	}
}
