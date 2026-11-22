//! IP Guard control plane.
//!
//! Owns the browser session (login/logout/me + Redis-backed token), serves the
//! static Next.js export, and forwards the remaining control/data-plane calls
//! to the Go engine. The Go service trusts requests that carry the internal
//! token, so it never needs the browser cookie again.

#![allow(clippy::result_large_err)] // axum handlers return Response directly by design.

use std::collections::HashMap;
use std::env;
use std::net::SocketAddr;
use std::path::PathBuf;
use std::sync::{Mutex, OnceLock};
use std::time::{Duration, Instant};

mod rest;

use axum::body::{Body, Bytes};
use axum::extract::{OriginalUri, Request, State};
use axum::http::{header, HeaderMap, HeaderValue, Method, StatusCode};
use axum::response::{IntoResponse, Response};
use axum::routing::{any, get, post};
use axum::{Json, Router};
use base64::engine::general_purpose::STANDARD as BASE64;
use base64::Engine;
use rand::RngCore;
use deadpool_postgres::{Manager, ManagerConfig, Pool, RecyclingMethod};
use redis::AsyncCommands;
use serde::{Deserialize, Serialize};
use serde_json::{json, Map, Value};
use tokio_postgres::NoTls;
use tower_http::services::ServeDir;

const COOKIE: &str = "ipfilter_session";
const SESSION_TTL: u64 = 24 * 60 * 60;

/// Collapse repeated operational errors so a transient database or proxy
/// outage cannot fill the journal with thousands of identical lines. The
/// suppressed count is emitted with the next line for the same error.
fn log_repeated(kind: &str, detail: &str) {
    const WINDOW: Duration = Duration::from_secs(60);
    const MAX_KEYS: usize = 64;

    static STATE: OnceLock<Mutex<HashMap<String, (Instant, u32)>>> = OnceLock::new();
    let now = Instant::now();
    let state = STATE.get_or_init(|| Mutex::new(HashMap::new()));
    let mut state = match state.lock() {
        Ok(state) => state,
        Err(poisoned) => poisoned.into_inner(),
    };
    if state.len() >= MAX_KEYS {
        state.retain(|_, (last, _)| now.saturating_duration_since(*last) < WINDOW);
    }
    let key = format!("{kind}\0{detail}");
    if let Some((last, suppressed)) = state.get_mut(&key) {
        if now.saturating_duration_since(*last) < WINDOW {
            *suppressed = suppressed.saturating_add(1);
            return;
        }
        let count = *suppressed;
        *last = now;
        *suppressed = 0;
        if count == 0 {
            eprintln!("{kind}: {detail}");
        } else {
            eprintln!("{kind}: {detail} ({count} 条重复日志已合并)");
        }
        return;
    }
    eprintln!("{kind}: {detail}");
    state.insert(key, (now, 0));
}

#[derive(Clone)]
struct AppState {
    go: String,
    origin: String,
    internal: String,
    web: PathBuf,
    probe_bin: PathBuf,
    probe_scripts: PathBuf,
    database: Option<Pool>,
    redis: Option<redis::Client>,
    secure_cookie: bool,
    client: reqwest::Client,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
struct User {
    id: String,
    username: String,
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    // Local development reads the project .env. Production EnvironmentFile values
    // are already present, so dotenvy leaves them untouched.
    dotenvy::dotenv().ok();
    let addr: SocketAddr = env::var("CONTROL_ADDR")
        .unwrap_or_else(|_| "127.0.0.1:8088".into())
        .parse()?;
    let web = PathBuf::from(env::var("WEB_DIR").unwrap_or_else(|_| "../../out".into()));
    let web = web.canonicalize().unwrap_or(web);
    let redis = redis::Client::open(
        env::var("REDIS_URL").unwrap_or_else(|_| "redis://localhost:6379/0".into()),
    )
    .ok();
    let database = match env::var("DATABASE_URL") {
        Ok(url) if !url.trim().is_empty() => Some(database_pool(&url)?),
        _ => None,
    };
    let state = AppState {
        go: env::var("WORKER_URL")
            .or_else(|_| env::var("API_URL"))
            .unwrap_or_else(|_| "http://127.0.0.1:8082".into()),
        origin: env::var("APP_ORIGIN").unwrap_or_else(|_| "http://localhost:3000".into()),
        internal: env::var("CONTROL_INTERNAL_TOKEN")
            .unwrap_or_else(|_| "dev-internal-token".into()),
        web: web.clone(),
        probe_bin: PathBuf::from(
            env::var("PROBE_BIN_DIR").unwrap_or_else(|_| "bin/latency-probe".into()),
        ),
        probe_scripts: PathBuf::from(
            env::var("PROBE_SCRIPT_DIR").unwrap_or_else(|_| "scripts/latency-probe".into()),
        ),
        database,
        redis,
        secure_cookie: env::var("APP_ENV").map(|v| v == "production").unwrap_or(false),
        client: reqwest::Client::builder()
            .timeout(Duration::from_secs(120))
            .redirect(reqwest::redirect::Policy::none())
            .build()?,
    };

    let api = Router::new()
        .route("/auth/login", post(login))
        .route("/auth/logout", post(logout))
        .route("/auth/me", get(me))
        .route("/blacklist", post(add_blacklist).delete(delete_blacklist))
        .route("/lists/:kind", get(lists))
        .route("/probe-config", get(probe_config))
        .merge(rest::router())
        .fallback(any(proxy))
        .with_state(state.clone());

    let app = Router::new()
        .route("/health", get(health))
        .route("/probe-downloads/:file", get(probe_download))
        .nest("/api", api)
        .fallback_service(ServeDir::new(&state.web).append_index_html_on_directories(true))
        .with_state(state);

    println!("control listening on http://{addr} (web={})", web.display());
    let listener = tokio::net::TcpListener::bind(addr).await?;
    axum::serve(listener, app)
        .with_graceful_shutdown(async {
            let _ = tokio::signal::ctrl_c().await;
        })
        .await?;
    Ok(())
}

fn database_pool(url: &str) -> Result<Pool, Box<dyn std::error::Error>> {
    let pg_config: tokio_postgres::Config = url.parse()?;
    let manager = Manager::from_config(
        pg_config,
        NoTls,
        ManagerConfig {
            recycling_method: RecyclingMethod::Fast,
        },
    );
    let max_size = env::var("CONTROL_DB_POOL_SIZE")
        .ok()
        .and_then(|value| value.parse::<usize>().ok())
        .filter(|value| *value > 0)
        .unwrap_or(8);
    Ok(Pool::builder(manager).max_size(max_size).build()?)
}

async fn health() -> Json<Value> {
    Json(json!({"ok": true}))
}

async fn probe_config(
    State(state): State<AppState>,
    headers: HeaderMap,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    Json(json!({"tls_pin": env::var("PROBE_TLS_PIN").unwrap_or_default()})).into_response()
}

async fn probe_download(
    State(state): State<AppState>,
    axum::extract::Path(file): axum::extract::Path<String>,
    headers: HeaderMap,
) -> Response {
    let script = file == "install.sh" || file == "install.ps1";
    let valid_binary = file.starts_with("latency-probe-")
        && (file.ends_with(".exe")
            || file.ends_with("linux-amd64")
            || file.ends_with("linux-arm64")
            || file.ends_with("darwin-amd64")
            || file.ends_with("darwin-arm64")
            || file.ends_with("windows-amd64.exe")
            || file.ends_with("windows-arm64.exe"));
    if !script && !valid_binary {
        return error(StatusCode::NOT_FOUND, "Not found");
    }
    let wants_gzip = headers
        .get(header::ACCEPT_ENCODING)
        .and_then(|value| value.to_str().ok())
        .map(|value| value.split(',').any(|part| part.trim().starts_with("gzip")))
        .unwrap_or(false);
    let base = if script {
        state.probe_scripts.join(&file)
    } else {
        state.probe_bin.join(&file)
    };
    let gz = state.probe_bin.join(format!("{file}.gz"));
    let (path, compressed) = if !script && wants_gzip && gz.exists() {
        (gz, true)
    } else {
        (base, false)
    };
    let content = match tokio::fs::read(&path).await {
        Ok(value) => value,
        Err(_) => {
            return error(
                StatusCode::SERVICE_UNAVAILABLE,
                "安装文件暂未发布，请联系管理员构建探针程序",
            )
        }
    };
    let mut response = Response::builder()
        .status(StatusCode::OK)
        .header(
            header::CONTENT_TYPE,
            if script { "text/plain; charset=utf-8" } else { "application/octet-stream" },
        )
        .header(
            header::CONTENT_DISPOSITION,
            format!("attachment; filename=\"{file}\""),
        )
        .header(header::CACHE_CONTROL, "no-store")
        .header(header::VARY, "Accept-Encoding")
        .header("X-Content-Type-Options", "nosniff")
        .body(Body::from(content))
        .unwrap_or_else(|_| error(StatusCode::INTERNAL_SERVER_ERROR, "响应构造失败"));
    if compressed {
        response
            .headers_mut()
            .insert(header::CONTENT_ENCODING, HeaderValue::from_static("gzip"));
    }
    response
}

async fn login(State(state): State<AppState>, body: Bytes) -> Response {
    let input: Value = match serde_json::from_slice(&body) {
        Ok(value) => value,
        Err(_) => return error(StatusCode::BAD_REQUEST, "请求格式错误，请检查字段类型和内容"),
    };
    let username = input
        .get("username")
        .and_then(Value::as_str)
        .unwrap_or("")
        .trim()
        .to_string();
    let password = input.get("password").and_then(Value::as_str).unwrap_or("");
    if username.is_empty() || username.len() > 64 || password.is_empty() || password.len() > 128 {
        return error(StatusCode::BAD_REQUEST, "请输入有效的用户名和密码");
    }
    let Some(redis) = state.redis.clone() else {
        return error(StatusCode::SERVICE_UNAVAILABLE, "登录服务暂时不可用，请稍后重试");
    };
    let rate_key = format!("ipguard:login:{username}");
    let attempts: i64 = async {
        let mut connection = redis.get_multiplexed_async_connection().await.ok()?;
        let value: i64 = connection.incr(&rate_key, 1).await.ok()?;
        if value == 1 {
            let _: Result<(), _> = connection.expire(&rate_key, 900).await;
        }
        Some(value)
    }
    .await
    .unwrap_or(0);
    if attempts > 10 {
        return error(StatusCode::TOO_MANY_REQUESTS, "登录尝试过于频繁，请 15 分钟后重试");
    }
    let Some(pool) = state.database.as_ref() else {
        return error(StatusCode::SERVICE_UNAVAILABLE, "数据库暂时不可用，请稍后重试");
    };

    let client = match pool.get().await {
        Ok(client) => client,
        Err(err) => {
            crate::log_repeated("login pool error", &err.to_string());
            return error(StatusCode::SERVICE_UNAVAILABLE, "数据库暂时不可用，请稍后重试");
        }
    };
    let statement = match client
        .prepare_cached("SELECT id::text, username, password_hash FROM users WHERE username=$1")
        .await
    {
        Ok(statement) => statement,
        Err(err) => {
            crate::log_repeated("login prepare error", &err.to_string());
            return error(StatusCode::SERVICE_UNAVAILABLE, "数据库暂时不可用，请稍后重试");
        }
    };
    let query = client.query_opt(&statement, &[&username]).await;

    let (id, name, hash) = match query {
        Ok(Some(row)) => (row.get::<_, String>(0), row.get::<_, String>(1), row.get::<_, String>(2)),
        Ok(None) => (
            String::new(),
            username.clone(),
            "$2a$12$LQv3c1yqBWVHxkd0LHAkCOYz6TtxGJeCsXOBqjXvEoQMikpkKJz.a".to_string(),
        ),
        Err(err) => {
            crate::log_repeated("login database error", &err.to_string());
            return error(StatusCode::SERVICE_UNAVAILABLE, "数据库暂时不可用，请稍后重试");
        }
    };
    let password = password.to_string();
    let valid = tokio::task::spawn_blocking(move || bcrypt::verify(password, &hash))
        .await
        .ok()
        .and_then(Result::ok)
        .unwrap_or(false);
    if id.is_empty() || !valid {
        return error(StatusCode::UNAUTHORIZED, "用户名或密码错误，请检查后重试");
    }
    if let Some(redis) = state.redis.clone() {
        let _: Result<(), _> = async {
            let mut connection = redis.get_multiplexed_async_connection().await?;
            connection.del::<_, ()>(&rate_key).await
        }
        .await;
    }

    let Some(redis) = state.redis.clone() else {
        return error(StatusCode::SERVICE_UNAVAILABLE, "登录服务暂时不可用，请稍后重试");
    };
    let token = random_token();    let payload = serde_json::to_string(&User { id: id.clone(), username: name.clone() })
        .unwrap_or_default();
    let stored: Result<(), _> = async {
        let mut connection = redis.get_multiplexed_async_connection().await?;
        connection
            .set_ex::<_, _, ()>(format!("ipguard:session:{token}"), payload, SESSION_TTL)
            .await
    }
    .await;
    if stored.is_err() {
        return error(StatusCode::SERVICE_UNAVAILABLE, "无法创建登录会话，请重试");
    }

    let mut response = Json(User { id, username: name }).into_response();
    response.headers_mut().insert(
        header::SET_COOKIE,
        HeaderValue::from_str(&cookie_header(&token, state.secure_cookie, SESSION_TTL))
            .unwrap_or_else(|_| HeaderValue::from_static("")),
    );
    response
}

async fn me(State(state): State<AppState>, headers: HeaderMap) -> Response {
    match authenticate(&state, &headers).await {
        Ok(user) => Json(user).into_response(),
        Err(response) => response,
    }
}

async fn logout(State(state): State<AppState>, headers: HeaderMap) -> Response {
    if let Some(token) = cookie_token(&headers) {
        if let Some(redis) = state.redis.clone() {
            let _: Result<(), _> = async {
                let mut connection = redis.get_multiplexed_async_connection().await?;
                connection.del::<_, ()>(format!("ipguard:session:{token}")).await
            }
            .await;
        }
    }
    let mut response = Json(json!({"ok": true})).into_response();
    response.headers_mut().insert(
        header::SET_COOKIE,
        HeaderValue::from_str(&cookie_header("", state.secure_cookie, 0))
            .unwrap_or_else(|_| HeaderValue::from_static("")),
    );
    response
}

fn cookie_header(token: &str, secure: bool, max_age: u64) -> String {
    format!(
        "{COOKIE}={token}; Path=/; HttpOnly; SameSite=Lax; Max-Age={max_age}{}",
        if secure { "; Secure" } else { "" }
    )
}

fn random_token() -> String {
    let mut bytes = [0u8; 32];
    rand::thread_rng().fill_bytes(&mut bytes);
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}

fn cookie_token(headers: &HeaderMap) -> Option<String> {
    let cookie = headers.get(header::COOKIE)?.to_str().ok()?;
    cookie.split(';').find_map(|part| {
        part.trim()
            .strip_prefix(&format!("{COOKIE}="))
            .map(|value| value.to_string())
    })
}

async fn authenticate(state: &AppState, headers: &HeaderMap) -> Result<User, Response> {
    let Some(token) = cookie_token(headers) else {
        return Err(error(StatusCode::UNAUTHORIZED, "登录已过期，请重新登录"));
    };
    let Some(redis) = state.redis.clone() else {
        return Err(error(StatusCode::SERVICE_UNAVAILABLE, "会话服务暂时不可用"));
    };
    let value: Option<String> = async {
        let mut connection = redis.get_multiplexed_async_connection().await.ok()?;
        connection
            .get(format!("ipguard:session:{token}"))
            .await
            .ok()
    }
    .await
    .flatten();
    let Some(value) = value else {
        return Err(error(StatusCode::UNAUTHORIZED, "登录已过期，请重新登录"));
    };
    serde_json::from_str::<User>(&value)
        .map_err(|_| error(StatusCode::UNAUTHORIZED, "登录已过期，请重新登录"))
}

async fn add_blacklist(State(state): State<AppState>, headers: HeaderMap, body: Bytes) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    let input: Value = match serde_json::from_slice(&body) {
        Ok(value) => value,
        Err(_) => return error(StatusCode::BAD_REQUEST, "请求格式错误，请检查字段类型和内容"),
    };
    let Some(items) = input.get("ips").and_then(Value::as_array) else {
        return error(StatusCode::UNPROCESSABLE_ENTITY, "请输入 1–500 个 IP");
    };
    if items.is_empty() || items.len() > 500 {
        return error(StatusCode::UNPROCESSABLE_ENTITY, "请输入 1–500 个 IP");
    }
    let mut unique: Vec<String> = Vec::new();
    for item in items {
        let raw = item.as_str().unwrap_or("");
        let Ok(ip) = raw.parse::<std::net::IpAddr>() else {
            return error_fields(
                StatusCode::UNPROCESSABLE_ENTITY,
                "黑名单只接受单个 IPv4 或 IPv6 地址",
                "ip",
                raw,
            );
        };
        let canonical = ip.to_string();
        if !unique.contains(&canonical) {
            unique.push(canonical);
        }
    }
    if state.database.is_none() {
        return error(StatusCode::SERVICE_UNAVAILABLE, "数据库暂时不可用，请稍后重试");
    }
    let client = match db_client(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let mut added = 0i64;
    for ip in &unique {
        match client
            .execute(
                "INSERT INTO ip_blacklist(ip) VALUES($1::text::inet) ON CONFLICT(ip) DO NOTHING",
                &[ip],
            )
            .await
        {
            Ok(rows) => added += rows as i64,
            Err(err) => {
                crate::log_repeated("blacklist insert error", &err.to_string());
                return error(StatusCode::SERVICE_UNAVAILABLE, "数据库暂时不可用，请稍后重试");
            }
        }
    }
    Json(json!({"added": added, "skipped": items.len() as i64 - added})).into_response()
}

async fn delete_blacklist(State(state): State<AppState>, headers: HeaderMap, body: Bytes) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    let input: Value = match serde_json::from_slice(&body) {
        Ok(value) => value,
        Err(_) => return error(StatusCode::BAD_REQUEST, "请求格式错误，请检查字段类型和内容"),
    };
    let raw = input.get("ip").and_then(Value::as_str).unwrap_or("");
    let Ok(ip) = raw.parse::<std::net::IpAddr>() else {
        return error_fields(
            StatusCode::UNPROCESSABLE_ENTITY,
            "黑名单只接受单个 IPv4 或 IPv6 地址",
            "ip",
            raw,
        );
    };
    if state.database.is_none() {
        return error(StatusCode::SERVICE_UNAVAILABLE, "数据库暂时不可用，请稍后重试");
    }
    let client = match db_client(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    if let Err(err) = client
        .execute("DELETE FROM ip_blacklist WHERE ip=$1::text::inet", &[&ip.to_string()])
        .await
    {
        crate::log_repeated("blacklist delete error", &err.to_string());
        return error(StatusCode::SERVICE_UNAVAILABLE, "数据库暂时不可用，请稍后重试");
    }
    Json(json!({"ok": true})).into_response()
}

async fn db_client(state: &AppState) -> Result<deadpool_postgres::Client, Response> {
    let pool = state
        .database
        .as_ref()
        .ok_or_else(|| error(StatusCode::SERVICE_UNAVAILABLE, "数据库暂时不可用，请稍后重试"))?;
    pool.get().await.map_err(|err| {
        crate::log_repeated("database pool error", &err.to_string());
        error(StatusCode::SERVICE_UNAVAILABLE, "数据库暂时不可用，请稍后重试")
    })
}

fn error_fields(status: StatusCode, message: &str, field: &str, value: &str) -> Response {
    let mut fields = Map::new();
    fields.insert(field.to_string(), json!(value));
    (status, Json(json!({"error": message, "fields": fields})).into_response()).into_response()
}

async fn lists(
    State(state): State<AppState>,
    axum::extract::Path(kind): axum::extract::Path<String>,
    OriginalUri(uri): OriginalUri,
    headers: HeaderMap,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    let query = uri.query().unwrap_or("");
    if kind == "scan-records" {
        return rest::scan_records_list(&state, &headers, query).await;
    }
    if let Some(response) = rest::list_payload(&state, &kind, query).await {
        return response;
    }
    let path = match kind.as_str() {
        "nodes" => "/nodes".to_string(),
        "networks" => "/networks".to_string(),
        "blacklist" => "/blacklist".to_string(),
        "available-ips" => "/available-ips".to_string(),
        "latency-probes" => "/latency-probes".to_string(),
        "scan-records" => "/scan-records".to_string(),
        "cf-targets" => "/cf-targets".to_string(),
        "cf-pool" => "/cf-pool".to_string(),
        "dns-records" => "/dns/records".to_string(),
        "dns-candidates" => "/dns/candidates".to_string(),
        "dns-history" => "/dns/history".to_string(),
        "certificate-issues" => "/certificate-issues".to_string(),
        "cf-detail" => {
            let id = query_param(query, "id").unwrap_or_default();
            if !valid_uuid(&id) {
                return error(StatusCode::NOT_FOUND, "列表不存在");
            }
            format!("/cf-targets/{id}")
        }
        "scan-detail" => {
            let id = query_param(query, "id").unwrap_or_default();
            if !id.chars().all(|c| c.is_ascii_digit()) || id.is_empty() {
                return error(StatusCode::NOT_FOUND, "列表不存在");
            }
            format!("/scan-records/{id}")
        }
        _ => return error(StatusCode::NOT_FOUND, "列表不存在"),
    };

    let mut go_query = query.to_string();
    if kind == "scan-detail" {
        go_query = if go_query.is_empty() {
            "paged=1".into()
        } else {
            format!("{go_query}&paged=1")
        };
    }
    let url = join_url(&state.go, &path, &go_query);
    let upstream = match state
        .client
        .get(url)
        .headers(internal_headers(&state, &headers, false))
        .send()
        .await
    {
        Ok(value) => value,
        Err(err) => {
            crate::log_repeated("lists proxy error", &err.to_string());
            return error(StatusCode::SERVICE_UNAVAILABLE, "列表加载失败，请重试");
        }
    };
    let status = upstream.status();
    let upstream_headers = upstream.headers().clone();
    let body = match upstream.bytes().await {
        Ok(value) => value,
        Err(_) => return error(StatusCode::SERVICE_UNAVAILABLE, "列表加载失败，请重试"),
    };
    if !status.is_success() {
        return raw_response(status, upstream_headers, body);
    }

    let data: Value = match serde_json::from_slice(&body) {
        Ok(value) => value,
        Err(_) => return error(StatusCode::SERVICE_UNAVAILABLE, "列表加载失败，请重试"),
    };
    let mut page = Map::new();
    page.insert(
        "total".into(),
        json!(header_number(&upstream_headers, "x-total-count", array_len(&data))),
    );
    page.insert("page".into(), json!(header_number(&upstream_headers, "x-page", 1)));
    page.insert(
        "pageSize".into(),
        json!(header_number(&upstream_headers, "x-page-size", 50)),
    );
    if let Some(value) = header_number_opt(&upstream_headers, "x-all-total") {
        page.insert("allTotal".into(), json!(value));
    }
    if let Some(value) = header_number_opt(&upstream_headers, "x-enabled-count") {
        page.insert("enabledCount".into(), json!(value));
    }
    if let Some(meta) = upstream_headers.get("x-list-meta").and_then(|v| v.to_str().ok()) {
        if let Ok(decoded) = BASE64.decode(meta) {
            if let Ok(Value::Object(extra)) = serde_json::from_slice::<Value>(&decoded) {
                for (key, value) in extra {
                    page.insert(key, value);
                }
            }
        }
    }

    let payload = match data {
        Value::Array(items) => {
            let mut object = Map::new();
            object.insert("items".into(), Value::Array(items));
            object.extend(page);
            Value::Object(object)
        }
        Value::Object(mut object) => {
            for (key, value) in page {
                object.entry(key).or_insert(value);
            }
            Value::Object(object)
        }
        _ => return error(StatusCode::SERVICE_UNAVAILABLE, "列表加载失败，请重试"),
    };
    let mut response = Json(payload).into_response();
    for (key, value) in upstream_headers.iter() {
        if key.as_str().starts_with("x-") {
            response.headers_mut().insert(key, value.clone());
        }
    }
    response
        .headers_mut()
        .insert(header::CACHE_CONTROL, HeaderValue::from_static("no-store"));
    response
}

fn array_len(value: &Value) -> i64 {
    value.as_array().map(|items| items.len() as i64).unwrap_or(0)
}

fn header_number(headers: &HeaderMap, name: &str, fallback: i64) -> i64 {
    header_number_opt(headers, name).unwrap_or(fallback)
}

fn header_number_opt(headers: &HeaderMap, name: &str) -> Option<i64> {
    headers
        .get(name)
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.parse().ok())
}

async fn proxy(
    State(state): State<AppState>,
    OriginalUri(uri): OriginalUri,
    request: Request,
) -> Response {
    let path = uri.path().strip_prefix("/api").unwrap_or(uri.path());
    let is_probe = path.starts_with("/probe-agent/");
    let user = if is_probe {
        if request.headers().get(header::AUTHORIZATION).is_none() {
            return error(StatusCode::UNAUTHORIZED, "缺少探针密钥");
        }
        None
    } else {
        match authenticate(&state, request.headers()).await {
            Ok(user) => Some(user),
            Err(response) => return response,
        }
    };

    proxy_simple(
        &state,
        request.method().clone(),
        path,
        uri.query().unwrap_or(""),
        request,
        user,
    )
    .await
}

async fn proxy_simple(
    state: &AppState,
    method: Method,
    path: &str,
    query: &str,
    request: Request,
    user: Option<User>,
) -> Response {
    let headers = request.headers().clone();
    let body = match axum::body::to_bytes(request.into_body(), 2 * 1024 * 1024).await {
        Ok(value) => value,
        Err(_) => return error(StatusCode::PAYLOAD_TOO_LARGE, "上报数据过大"),
    };
    let url = join_url(&state.go, path, query);
    let mut builder = state.client.request(method.clone(), url);
    builder = builder.headers(internal_headers(state, &headers, true));
    if let Some(user) = user {
        builder = builder
            .header("X-Internal-User-ID", user.id)
            .header("X-Internal-Username", user.username);
    }
    if !body.is_empty() {
        builder = builder.body(body);
    }
    match builder.send().await {
        Ok(upstream) => {
            let status = upstream.status();
            let headers = upstream.headers().clone();
            match upstream.bytes().await {
                Ok(body) => raw_response(status, headers, body),
                Err(_) => error(StatusCode::SERVICE_UNAVAILABLE, "上游服务暂时不可用"),
            }
        }
        Err(err) => {
            crate::log_repeated("proxy error", &format!("{path}: {err}"));
            error(StatusCode::SERVICE_UNAVAILABLE, "上游服务暂时不可用")
        }
    }
}

fn internal_headers(state: &AppState, original: &HeaderMap, with_body: bool) -> HeaderMap {
    let mut headers = HeaderMap::new();
    for (key, value) in original.iter() {
        let name = key.as_str();
        if matches!(
            name,
            "host" | "content-length" | "connection" | "transfer-encoding" | "cookie" | "origin"
        ) {
            continue;
        }
        headers.insert(key, value.clone());
    }
    if !with_body {
        headers.remove(header::CONTENT_TYPE);
    }
    headers.insert(
        "X-Internal-Auth",
        HeaderValue::from_str(&state.internal).unwrap_or_else(|_| HeaderValue::from_static("")),
    );
    headers.insert(
        header::ORIGIN,
        HeaderValue::from_str(&state.origin).unwrap_or_else(|_| HeaderValue::from_static("")),
    );
    headers.insert(header::ACCEPT, HeaderValue::from_static("application/json"));
    headers
}

fn join_url(base: &str, path: &str, query: &str) -> String {
    let mut url = format!("{}{}", base.trim_end_matches('/'), path);
    if !query.is_empty() {
        url.push('?');
        url.push_str(query);
    }
    url
}

fn query_param(query: &str, name: &str) -> Option<String> {
    query.split('&').find_map(|pair| {
        let (key, value) = pair.split_once('=').unwrap_or((pair, ""));
        (key == name).then(|| url_decode(value))
    })
}

fn url_decode(value: &str) -> String {
    let encoded = format!("x={value}");
    url::form_urlencoded::parse(encoded.as_bytes())
        .next()
        .map(|(_, value)| value.into_owned())
        .unwrap_or_default()
}

fn valid_uuid(value: &str) -> bool {
    value.len() == 36
        && value.chars().all(|c| c.is_ascii_hexdigit() || c == '-')
}

fn raw_response(status: StatusCode, headers: HeaderMap, body: Bytes) -> Response {
    let mut builder = Response::builder().status(status);
    for (key, value) in headers.iter() {
        if matches!(
            key.as_str(),
            "content-length" | "transfer-encoding" | "connection"
        ) {
            continue;
        }
        builder = builder.header(key, value);
    }
    builder.body(Body::from(body)).unwrap_or_else(|_| {
        error(StatusCode::INTERNAL_SERVER_ERROR, "响应构造失败")
    })
}

fn error(status: StatusCode, message: &str) -> Response {
    (status, Json(json!({"error": message})).into_response()).into_response()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_session_cookie() {
        let mut headers = HeaderMap::new();
        headers.insert(
            header::COOKIE,
            HeaderValue::from_static("theme=dark; ipfilter_session=abc123; other=1"),
        );
        assert_eq!(cookie_token(&headers).as_deref(), Some("abc123"));
    }

    #[test]
    fn ignores_other_cookies() {
        let mut headers = HeaderMap::new();
        headers.insert(header::COOKIE, HeaderValue::from_static("theme=dark"));
        assert!(cookie_token(&headers).is_none());
    }

    #[test]
    fn validates_uuid_shape() {
        assert!(valid_uuid("00000000-0000-0000-0000-000000000001"));
        assert!(!valid_uuid("../../etc/passwd"));
        assert!(!valid_uuid("00000000000000000000000000000000"));
    }

    #[test]
    fn decodes_query_parameter() {
        assert_eq!(
            query_param("id=abc%2D123&ip=1.2.3.4", "id").as_deref(),
            Some("abc-123")
        );
        assert_eq!(query_param("id=abc", "ip"), None);
    }

    #[test]
    fn builds_cookie_header() {
        let production = cookie_header("token", true, SESSION_TTL);
        assert!(production.contains("HttpOnly"));
        assert!(production.contains("SameSite=Lax"));
        assert!(production.contains("Secure"));
        let dev = cookie_header("token", false, SESSION_TTL);
        assert!(!dev.contains("Secure"));
    }
}
