package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/alexedwards/scs/goredisstore"
	"github.com/alexedwards/scs/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
	"github.com/ulule/limiter/v3"
	redisstore "github.com/ulule/limiter/v3/drivers/store/redis"
	"golang.org/x/crypto/bcrypt"
)

//go:embed schema.sql
var schema string

const cookieName = "ipfilter_session"
const sessionTTL = 24 * time.Hour

type app struct {
	db           *pgxpool.Pool
	redis        *redis.Client
	sessions     *scs.SessionManager
	loginLimiter *limiter.Limiter
	scanContext  context.Context
	origin       string
}
type user struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}
type userContextKey struct{}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	_ = godotenv.Load("../.env")
	_ = godotenv.Load(".env")
	initialize := flag.Bool("init", false, "初始化数据库和管理员后退出")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("请配置 DATABASE_URL")
	}
	dbConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		log.Fatal(err)
	}
	slowThreshold, err := time.ParseDuration(env("DB_SLOW_QUERY_THRESHOLD", "500ms"))
	if err != nil || slowThreshold <= 0 {
		log.Fatal("DB_SLOW_QUERY_THRESHOLD 必须是正数时长，例如 500ms")
	}
	dbConfig.ConnConfig.Tracer = &databaseTracer{threshold: slowThreshold, logf: log.Printf}
	db, err := pgxpool.NewWithConfig(ctx, dbConfig)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err = db.Ping(startup); err != nil {
		log.Fatal("PostgreSQL 连接失败: ", err)
	}
	if *initialize {
		if err = initDB(startup, db); err != nil {
			log.Fatal(err)
		}
		return
	}
	options, err := redis.ParseURL(env("REDIS_URL", "redis://localhost:6379/0"))
	if err != nil {
		log.Fatal(err)
	}
	options.DialTimeout = 3 * time.Second
	options.ReadTimeout = 3 * time.Second
	options.WriteTimeout = 3 * time.Second
	options.MaxRetries = 1
	rdb := redis.NewClient(options)
	defer rdb.Close()
	if err = rdb.Ping(startup).Err(); err != nil {
		log.Fatal("Redis 连接失败: ", err)
	}
	a, err := newApp(db, rdb)
	if err != nil {
		log.Fatal(err)
	}
	a.scanContext = ctx
	go a.revalidateLegacyCertificates(ctx)
	go a.resumeNodeSchedules(ctx)
	go a.maintainAutomaticScans(ctx)
	go a.maintainNetworkMetadata(ctx)
	go a.maintainDNS(ctx)
	server := &http.Server{Addr: env("API_ADDR", "127.0.0.1:8081"), Handler: a.routes(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("Go API 已启动：http://%s", server.Addr)
	if err = server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func newApp(db *pgxpool.Pool, rdb *redis.Client) (*app, error) {
	sessions := scs.New()
	sessions.Store = goredisstore.NewWithPrefix(rdb, "ipfilter:session:")
	sessions.Lifetime = sessionTTL
	sessions.Cookie.Name = cookieName
	sessions.Cookie.Secure = os.Getenv("APP_ENV") == "production"
	sessions.Cookie.HttpOnly = true
	sessions.Cookie.SameSite = http.SameSiteLaxMode
	sessions.Cookie.Persist = true
	sessions.ErrorFunc = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("会话服务错误: %v", err)
		apiError(w, 503, "登录服务暂时不可用，请稍后重试", nil)
	}
	store, err := redisstore.NewStoreWithOptions(rdb, limiter.StoreOptions{Prefix: "ipfilter:login"})
	if err != nil {
		return nil, err
	}
	a := &app{scanContext: context.Background(), db: db, redis: rdb, sessions: sessions, loginLimiter: limiter.New(store, limiter.Rate{Period: 15 * time.Minute, Limit: 10}), origin: env("APP_ORIGIN", "http://localhost:3000")}
	return a, nil
}

func initDB(ctx context.Context, db *pgxpool.Pool) error {
	username, password := env("ADMIN_USERNAME", "admin"), os.Getenv("ADMIN_PASSWORD")
	if validate.Var(username, "required,min=3,max=64,printascii,excludesall= ") != nil {
		return fmt.Errorf("用户名需为 3–64 位可打印 ASCII 字符，不含空格")
	}
	if utf8Length := len([]rune(password)); utf8Length < 12 || len(password) > 72 {
		return fmt.Errorf("ADMIN_PASSWORD 需至少 12 个字符且不超过 72 字节")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return err
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, schema); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, "INSERT INTO users (username,password_hash) VALUES ($1,$2) ON CONFLICT (username) DO NOTHING", username, string(hash))
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if result.RowsAffected() == 1 {
		log.Printf("数据库已初始化，已创建管理员 %s", username)
	} else {
		log.Print("数据库已初始化，保留现有管理员密码")
	}
	return nil
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if err := a.db.Ping(r.Context()); err != nil {
			apiError(w, 503, "数据库连接失败", nil)
			return
		}
		if err := a.redis.Ping(r.Context()).Err(); err != nil {
			apiError(w, 503, "Redis 连接失败", nil)
			return
		}
		respond(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /auth/login", a.login)
	mux.Handle("POST /auth/logout", a.protected(http.HandlerFunc(a.logout)))
	mux.Handle("GET /auth/me", a.protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { respond(w, 200, r.Context().Value(userContextKey{})) })))
	for _, kind := range []string{"nodes", "networks"} {
		mux.Handle("GET /"+kind, a.protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.list(w, r, kind) })))
		mux.Handle("POST /"+kind, a.protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.save(w, r, kind, "") })))
		mux.Handle("PUT /"+kind+"/{id}", a.protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.save(w, r, kind, r.PathValue("id")) })))
		mux.Handle("DELETE /"+kind+"/{id}", a.protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.delete(w, r, kind) })))
	}
	mux.Handle("GET /cf-targets", a.protected(http.HandlerFunc(a.listCFTargets)))
	mux.Handle("POST /cf-targets", a.protected(http.HandlerFunc(a.createCFTarget)))
	mux.Handle("GET /cf-targets/{id}", a.protected(http.HandlerFunc(a.getCFTarget)))
	mux.Handle("DELETE /cf-targets/{id}", a.protected(http.HandlerFunc(a.deleteCFTarget)))
	mux.Handle("POST /cf-targets/{id}/scans", a.protected(http.HandlerFunc(a.startCFScan)))
	mux.Handle("GET /cf-scan", a.protected(http.HandlerFunc(a.latestCFScan)))
	mux.Handle("GET /cf-pool", a.protected(http.HandlerFunc(a.cfIPPool)))
	mux.Handle("POST /nodes/import", a.protected(http.HandlerFunc(a.importNodes)))
	mux.Handle("GET /available-ips/links", a.protected(http.HandlerFunc(a.generateIPLinks)))
	mux.Handle("GET /available-ips/location", a.protected(http.HandlerFunc(a.availableIPLocation)))
	mux.Handle("DELETE /protocol-networks/{id}", a.protected(http.HandlerFunc(a.restoreProtocolNetwork)))
	mux.Handle("GET /certificate-issues", a.protected(http.HandlerFunc(a.certificateIssues)))
	mux.Handle("GET /available-ips", a.protected(http.HandlerFunc(a.availableIPs)))
	mux.Handle("GET /dns", a.protected(http.HandlerFunc(a.getDNS)))
	mux.Handle("PUT /dns", a.protected(http.HandlerFunc(a.saveDNS)))
	mux.Handle("POST /dns/{mode}", a.protected(http.HandlerFunc(a.requestDNS)))
	mux.Handle("GET /latency-probes", a.protected(http.HandlerFunc(a.listLatencyProbes)))
	mux.Handle("POST /latency-probes", a.protected(http.HandlerFunc(a.createLatencyProbe)))
	mux.Handle("PUT /latency-probes/{id}", a.protected(http.HandlerFunc(a.updateLatencyProbe)))
	mux.Handle("DELETE /latency-probes/{id}", a.protected(http.HandlerFunc(a.deleteLatencyProbe)))
	mux.Handle("POST /latency-probes/{id}/token", a.protected(http.HandlerFunc(a.rotateLatencyProbeToken)))
	mux.Handle("GET /latency-probes/{id}/deployment", a.protected(http.HandlerFunc(a.probeDeployment)))
	mux.Handle("PUT /latency-probes/{id}/deployment", a.protected(http.HandlerFunc(a.probeDeployment)))
	mux.Handle("GET /latency-probes/{id}/history", a.protected(http.HandlerFunc(a.latencyProbeHistory)))
	mux.HandleFunc("GET /probe-agent/targets", a.probeAgentTargets)
	mux.HandleFunc("POST /probe-agent/results", a.reportProbeResults)
	mux.HandleFunc("POST /probe-agent/heartbeat", a.probeAgentHeartbeat)
	mux.Handle("GET /blacklist", a.protected(http.HandlerFunc(a.listBlacklist)))
	mux.Handle("GET /dns/{kind}", a.protected(http.HandlerFunc(a.listDNSItems)))
	mux.Handle("POST /blacklist", a.protected(http.HandlerFunc(a.addBlacklist)))
	mux.Handle("DELETE /blacklist", a.protected(http.HandlerFunc(a.deleteBlacklist)))
	mux.Handle("POST /networks/{id}/recheck", a.protected(http.HandlerFunc(a.recheckNetwork)))
	mux.Handle("POST /scans", a.protected(http.HandlerFunc(a.startScan)))
	mux.Handle("GET /scan-records", a.protected(http.HandlerFunc(a.listScanRecords)))
	mux.Handle("GET /scan-records/{id}", a.protected(http.HandlerFunc(a.getScanRecord)))
	mux.Handle("GET /scans/latest", a.protected(http.HandlerFunc(a.latestScan)))
	mux.Handle("GET /scans/{id}", a.protected(http.HandlerFunc(a.getScan)))
	mux.Handle("POST /scans/{id}/cancel", a.protected(http.HandlerFunc(a.cancelScan)))
	handler := a.sessions.LoadAndSave(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// 浏览器跨站请求不可携带任意 Origin；Next.js 服务端代理也使用相同来源。
		if r.Method != "GET" && !strings.HasPrefix(r.URL.Path, "/probe-agent/") && r.Header.Get("Origin") != a.origin {
			apiError(w, 403, "请求来源不受信任", nil)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		_, pattern := mux.Handler(r)
		ctx = context.WithValue(ctx, databaseRequestKey{}, pattern)
		handler.ServeHTTP(w, r.WithContext(ctx))
	})
}

func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func apiError(w http.ResponseWriter, status int, message string, fields map[string]string) {
	respond(w, status, map[string]any{"error": message, "fields": fields})
}
func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		apiError(w, 415, "请使用 JSON 请求", nil)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		apiError(w, 400, "请求格式错误，请检查字段类型和内容", nil)
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		apiError(w, 400, "请求只能包含一个 JSON 对象", nil)
		return false
	}
	return true
}
func dbError(w http.ResponseWriter, err error) {
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		if pgError.Code == "23505" {
			apiError(w, 409, "配置已存在，请检查节点名称、地址端口或重复 CIDR 网段", nil)
			return
		}
		if pgError.Code == "23503" || pgError.Code == "23001" {
			apiError(w, 409, "关联配置已变更，请刷新后重试", nil)
			return
		}
	}
	log.Printf("数据库操作失败: %v", err)
	apiError(w, 503, "数据库暂时不可用，请稍后重试", nil)
}
func (a *app) protected(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := a.sessions.GetString(r.Context(), "user_id")
		if id == "" {
			apiError(w, 401, "登录已过期，请重新登录", nil)
			return
		}
		current := user{ID: id, Username: a.sessions.GetString(r.Context(), "username")}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userContextKey{}, current)))
	})
}
func (a *app) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Username = strings.TrimSpace(input.Username)
	if input.Username == "" || len(input.Username) > 64 || input.Password == "" || len(input.Password) > 128 {
		apiError(w, 400, "请输入有效的用户名和密码", nil)
		return
	}
	digest := sha256.Sum256([]byte(strings.ToLower(input.Username)))
	key := hex.EncodeToString(digest[:])
	limit, err := a.loginLimiter.Get(r.Context(), key)
	if err != nil {
		apiError(w, 503, "登录服务暂时不可用，请稍后重试", nil)
		return
	}
	if limit.Reached {
		apiError(w, 429, "登录尝试过于频繁，请 15 分钟后重试", nil)
		return
	}
	var current user
	hash := "$2a$12$LQv3c1yqBWVHxkd0LHAkCOYz6TtxGJeCsXOBqjXvEoQMikpkKJz.a"
	err = a.db.QueryRow(r.Context(), "SELECT id::text,username,password_hash FROM users WHERE username=$1", input.Username).Scan(&current.ID, &current.Username, &hash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		dbError(w, err)
		return
	}
	valid := bcrypt.CompareHashAndPassword([]byte(hash), []byte(input.Password)) == nil
	if err != nil || !valid {
		apiError(w, 401, "用户名或密码错误，请检查后重试", nil)
		return
	}
	if err = a.sessions.RenewToken(r.Context()); err != nil {
		apiError(w, 503, "无法创建登录会话，请重试", nil)
		return
	}
	a.sessions.Put(r.Context(), "user_id", current.ID)
	a.sessions.Put(r.Context(), "username", current.Username)
	_, _ = a.loginLimiter.Reset(r.Context(), key)
	respond(w, 200, current)
}
func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	if err := a.sessions.Destroy(r.Context()); err != nil {
		apiError(w, 503, "退出失败，请重试", nil)
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}

func (a *app) list(w http.ResponseWriter, r *http.Request, kind string) {
	search, status := r.URL.Query().Get("search"), r.URL.Query().Get("status")
	text := "n.name||' '||n.address||' '||n.description"
	extra := ""
	order := "n.created_at DESC,n.id DESC"
	if kind == "networks" {
		text = "n.name||' '||n.cidr::text||' '||n.description||' '||n.asn||' '||n.provider||' '||COALESCE((SELECT string_agg(p.protocol,' ') FROM protocol_networks p WHERE p.network_id=n.id AND p.cidr=n.cidr),'')"
		extra = ",COALESCE((SELECT json_agg(p.protocol ORDER BY p.protocol) FROM protocol_networks p WHERE p.network_id=n.id AND p.cidr=n.cidr),'[]'::json) AS disabled_protocols"
		extra += `,(SELECT count(*) FROM current_node_ip_pool p
          WHERE p.protocol='grpc' AND p.available AND p.ip <<= n.cidr
          AND NOT EXISTS(SELECT 1 FROM ip_blacklist b WHERE b.ip=p.ip)) AS available_count`
		order = "available_count DESC,n.created_at DESC,n.id DESC"
	}
	where := " FROM " + kind + " n WHERE ($1='' OR position(lower($1) in lower(" + text + "))>0) AND ($2='' OR $2='all' OR ($2='enabled' AND n.enabled) OR ($2='disabled' AND NOT n.enabled)"
	if kind == "networks" {
		where += " OR ($2='pool-disabled' AND EXISTS(SELECT 1 FROM protocol_networks p WHERE p.network_id=n.id AND p.cidr=n.cidr))"
	}
	where += ") AND ($3='' OR n.id::text=ANY(string_to_array($3,',')))"
	if kind == "nodes" && r.URL.Query().Get("connectivity") == "grpc" {
		where += ` AND n.enabled AND n.share_uri ~ '^(vless|trojan)://[^?]+[?]([^#]*&)?type=grpc(&|#|$)'`
	}
	ids := r.URL.Query().Get("ids")
	if len(ids) > 20000 {
		apiError(w, 422, "选择的配置过多", nil)
		return
	}
	p, ok := a.listPage(w, r, 50, "SELECT count(*)"+where, search, status, ids)
	if !ok {
		return
	}
	var total, enabled int
	if err := a.db.QueryRow(r.Context(), "SELECT count(*),count(*) FILTER(WHERE enabled) FROM "+kind).Scan(&total, &enabled); err != nil {
		dbError(w, err)
		return
	}
	w.Header().Set("X-All-Total", strconv.Itoa(total))
	w.Header().Set("X-Enabled-Count", strconv.Itoa(enabled))
	query := "SELECT COALESCE(json_agg(t),'[]'::json) FROM (SELECT n.*" + extra + where + " ORDER BY " + order + " LIMIT $4 OFFSET $5) t"
	var data json.RawMessage
	if err := a.db.QueryRow(r.Context(), query, search, status, ids, p.Size, p.Offset()).Scan(&data); err != nil {
		dbError(w, err)
		return
	}
	respond(w, 200, data)
}
func (a *app) save(w http.ResponseWriter, r *http.Request, kind, id string) {
	if id != "" && validate.Var(id, "uuid") != nil {
		apiError(w, 400, "配置 ID 无效", nil)
		return
	}
	var input ConfigInput
	if !decode(w, r, &input) {
		return
	}
	if fields := input.validate(kind); len(fields) > 0 {
		apiError(w, 422, "请检查表单中的内容", fields)
		return
	}
	warning := ""
	autoScan := kind == "networks" && input.Enabled && id == ""
	if kind == "networks" {
		if id != "" {
			var oldCIDR string
			var oldEnabled bool
			if err := a.db.QueryRow(r.Context(), "SELECT cidr::text,enabled FROM networks WHERE id=$1", id).Scan(&oldCIDR, &oldEnabled); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					apiError(w, 404, "配置已被删除，请刷新列表", nil)
				} else {
					dbError(w, err)
				}
				return
			}
			autoScan = input.Enabled && (oldCIDR != input.CIDR || !oldEnabled)
			// Metadata from a previous address range must not follow an edited CIDR.
			if oldCIDR != input.CIDR {
				input.ASN, input.Provider = "", ""
			}
		}
		prefix := netip.MustParsePrefix(input.CIDR)
		if (input.ASN == "" || input.Provider == "") && publicMetadataPrefix(prefix) {
			metadata, err := a.networkMetadata(r.Context(), prefix)
			if err != nil {
				warning = "网段已保存，暂未获取到 ASN 和服务商；后台会自动重试，也可手动补充"
			} else if input.ASN == "" || input.ASN == metadata.ASN {
				// Preserve manual values; never attach a holder to a different manual ASN.
				if input.ASN == "" {
					input.ASN = metadata.ASN
				}
				if input.Provider == "" {
					input.Provider = metadata.Provider
				}
			} else {
				warning = "网段已保存，手填 ASN 与查询结果不同，请手动确认服务商"
			}
		}
	}
	var query string
	var values []any
	if kind == "nodes" {
		values = []any{input.Name, input.Address, input.Port, input.Enabled, input.Description, input.ShareURI}
		query = "INSERT INTO nodes(name,address,port,enabled,description,share_uri) VALUES($1,$2,$3,$4,$5,$6)"
		if id != "" {
			query = "UPDATE nodes SET name=$1,address=$2,port=$3,enabled=$4,description=$5,share_uri=$6,updated_at=now() WHERE id=$7"
		}
	} else {
		values = []any{input.Name, input.CIDR, input.Enabled, input.Description, input.ASN, input.Provider}
		query = "INSERT INTO networks(name,cidr,enabled,description,asn,provider) VALUES($1,$2::cidr,$3,$4,$5,$6)"
		if id != "" {
			query = "UPDATE networks SET name=$1,cidr=$2::cidr,enabled=$3,description=$4,asn=$5,provider=$6,updated_at=now() WHERE id=$7"
		}
	}
	if id != "" {
		values = append(values, id)
	}
	query += " RETURNING id::text"
	var savedID string
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		dbError(w, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	err = tx.QueryRow(r.Context(), query, values...).Scan(&savedID)
	if errors.Is(err, pgx.ErrNoRows) {
		apiError(w, 404, "配置已被删除，请刷新列表", nil)
		return
	}
	if err != nil {
		dbError(w, err)
		return
	}
	if autoScan {
		if err = enqueueAutomaticScans(r.Context(), tx, savedID, input.CIDR, r.Context().Value(userContextKey{}).(user).ID); err != nil {
			dbError(w, err)
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		dbError(w, err)
		return
	}
	status := 200
	if id == "" {
		status = 201
	}
	respond(w, status, map[string]string{"id": savedID, "warning": warning})
}
func (a *app) delete(w http.ResponseWriter, r *http.Request, kind string) {
	id := r.PathValue("id")
	if validate.Var(id, "uuid") != nil {
		apiError(w, 400, "配置 ID 无效", nil)
		return
	}
	// kind 由路由内部常量传入，用户输入使用参数化查询。
	result, err := a.db.Exec(r.Context(), "DELETE FROM "+kind+" WHERE id=$1", id)
	if err != nil {
		dbError(w, err)
		return
	}
	if result.RowsAffected() == 0 {
		apiError(w, 404, "配置已被删除，请刷新列表", nil)
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}
