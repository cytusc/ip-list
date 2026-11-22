//! Native Rust control-plane endpoints.
//!
//! The browser never reaches PostgreSQL directly. These handlers own the
//! configuration CRUD, list aggregation and probe reporting; the Go worker is
//! left with mihomo scans and external network execution.

use super::*;
use tokio_postgres::error::SqlState;
use tokio_postgres::types::ToSql;

const DEFAULT_PAGE_SIZE: i64 = 50;

fn query_value(uri: &str, name: &str) -> String {
    uri.split('&')
        .filter_map(|part| part.split_once('='))
        .find(|(key, _)| *key == name)
        .map(|(_, value)| url_decode(value))
        .unwrap_or_default()
}

fn page_number(uri: &str) -> Result<i64, Response> {
    let raw = query_value(uri, "page");
    if raw.is_empty() {
        return Ok(1);
    }
    match raw.parse::<i64>() {
        Ok(value) if value >= 1 => Ok(value),
        _ => Err(error(StatusCode::UNPROCESSABLE_ENTITY, "页码必须是正整数")),
    }
}

fn page_offset(page: i64, size: i64) -> i64 {
    (page - 1) * size
}

async fn database(state: &AppState) -> Result<deadpool_postgres::Client, Response> {
    db_client(state).await
}

fn db_failure(err: tokio_postgres::Error) -> Response {
    crate::log_repeated("database error", &err.to_string());
    if err.code() == Some(&SqlState::UNIQUE_VIOLATION) {
        return error(
            StatusCode::CONFLICT,
            "配置已存在，请检查节点名称、地址端口或重复 CIDR 网段",
        );
    }
    if err.code() == Some(&SqlState::FOREIGN_KEY_VIOLATION) {
        return error(StatusCode::CONFLICT, "关联配置已变更，请刷新后重试");
    }
    error(StatusCode::SERVICE_UNAVAILABLE, "数据库暂时不可用，请稍后重试")
}

async fn count(client: &tokio_postgres::Client, sql: &str, params: &[&(dyn ToSql + Sync)]) -> Result<i64, Response> {
    client
        .query_one(sql, params)
        .await
        .map(|row| row.get::<_, i64>(0))
        .map_err(db_failure)
}

async fn json_rows(client: &tokio_postgres::Client, sql: &str, params: &[&(dyn ToSql + Sync)]) -> Result<Value, Response> {
    let text: String = client
        .query_one(sql, params)
        .await
        .map(|row| row.get(0))
        .map_err(db_failure)?;
    serde_json::from_str(&text)
        .map_err(|_| error(StatusCode::SERVICE_UNAVAILABLE, "数据库返回内容无法解析"))
}

fn page_headers<R: IntoResponse>(response: R, total: i64, page: i64, size: i64) -> Response {
    let mut response = response.into_response();
    let headers = response.headers_mut();
    headers.insert("x-total-count", HeaderValue::from_str(&total.to_string()).unwrap());
    headers.insert("x-page", HeaderValue::from_str(&page.to_string()).unwrap());
    headers.insert("x-page-size", HeaderValue::from_str(&size.to_string()).unwrap());
    headers.insert(header::CACHE_CONTROL, HeaderValue::from_static("no-store"));
    response
}

fn page_object(items: Value, total: i64, page: i64, size: i64) -> Value {
    json!({
        "items": items,
        "total": total,
        "page": page,
        "pageSize": size,
    })
}

fn normalized_page(page: i64, total: i64, size: i64) -> i64 {
    page.min(((total + size - 1) / size).max(1))
}

struct Page {
    page: i64,
    size: i64,
    offset: i64,
    total: i64,
}

async fn load_page(
    client: &tokio_postgres::Client,
    uri: &str,
    count_sql: &str,
    params: &[&(dyn ToSql + Sync)],
    size: i64,
) -> Result<Page, Response> {
    let requested = page_number(uri)?;
    let total = count(client, count_sql, params).await?;
    let page = normalized_page(requested, total, size);
    Ok(Page {
        page,
        size,
        offset: page_offset(page, size),
        total,
    })
}

fn ids_param(uri: &str) -> String {
    query_value(uri, "ids")
}

fn connectivity_filter(uri: &str) -> bool {
    query_value(uri, "connectivity") == "grpc"
}

// ---------------------------------------------------------------------------
// Nodes and networks
// ---------------------------------------------------------------------------

struct ConfigList {
    items: Value,
    total: i64,
    all_total: i64,
    enabled_count: i64,
    page: i64,
    size: i64,
}

async fn node_configs(client: &tokio_postgres::Client, uri: &str) -> Result<ConfigList, Response> {
    let search = query_value(uri, "search");
    let status = query_value(uri, "status");
    let ids = ids_param(uri);
    let grpc = connectivity_filter(uri);
    let mut where_sql = String::from(
        " FROM nodes n WHERE ($1='' OR position(lower($1) in lower(n.name||' '||n.address||' '||n.description))>0) \
         AND ($2='' OR $2='all' OR ($2='enabled' AND n.enabled) OR ($2='disabled' AND NOT n.enabled)) \
         AND ($3='' OR n.id::text=ANY(string_to_array($3,',')))",
    );
    if grpc {
        where_sql.push_str(
            " AND n.enabled AND n.share_uri ~ '^(vless|trojan)://[^?]+[?]([^#]*&)?type=grpc(&|#|$)'",
        );
    }
    let count_sql = format!("SELECT count(*){where_sql}");
    let p = load_page(
        client,
        uri,
        &count_sql,
        &[&search, &status, &ids],
        DEFAULT_PAGE_SIZE,
    )
    .await?;
    let data_sql = format!(
        "SELECT COALESCE(json_agg(t),'[]'::json)::text FROM (SELECT n.*{where_sql} \
         ORDER BY n.created_at DESC,n.id DESC LIMIT $4 OFFSET $5) t"
    );
    let items = json_rows(
        client,
        &data_sql,
        &[&search, &status, &ids, &p.size, &p.offset],
    )
    .await?;
    let totals: (i64, i64) = client
        .query_one(
            "SELECT count(*),count(*) FILTER(WHERE enabled) FROM nodes",
            &[],
        )
        .await
        .map(|row| (row.get(0), row.get(1)))
        .map_err(db_failure)?;
    Ok(ConfigList {
        items,
        total: p.total,
        all_total: totals.0,
        enabled_count: totals.1,
        page: p.page,
        size: p.size,
    })
}

async fn network_configs(client: &tokio_postgres::Client, uri: &str) -> Result<ConfigList, Response> {
    let search = query_value(uri, "search");
    let status = query_value(uri, "status");
    let ids = ids_param(uri);
    let where_sql = " FROM networks n WHERE ($1='' OR position(lower($1) in lower(n.name||' '||n.cidr::text||' '||n.description||' '||n.asn||' '||n.provider||' '||n.registry_country||' '||n.registry_holder||' '||COALESCE((SELECT string_agg(p.protocol,' ') FROM protocol_networks p WHERE p.network_id=n.id AND p.cidr=n.cidr),'')))>0) \
         AND ($2='' OR $2='all' OR ($2='enabled' AND n.enabled) OR ($2='disabled' AND NOT n.enabled) OR ($2='pool-disabled' AND EXISTS(SELECT 1 FROM protocol_networks p WHERE p.network_id=n.id AND p.cidr=n.cidr))) \
         AND ($3='' OR n.id::text=ANY(string_to_array($3,',')))";
    let count_sql = format!("SELECT count(*){where_sql}");
    let p = load_page(
        client,
        uri,
        &count_sql,
        &[&search, &status, &ids],
        DEFAULT_PAGE_SIZE,
    )
    .await?;
    let data_sql = format!(
        "SELECT COALESCE(json_agg(t),'[]'::json)::text FROM (SELECT n.*,\
         COALESCE((SELECT json_agg(p.protocol ORDER BY p.protocol) FROM protocol_networks p WHERE p.network_id=n.id AND p.cidr=n.cidr),'[]'::json) AS disabled_protocols,\
         (SELECT count(*) FROM current_node_ip_pool p WHERE p.protocol='grpc' AND p.available AND p.ip <<= n.cidr AND NOT EXISTS(SELECT 1 FROM ip_blacklist b WHERE b.ip=p.ip)) AS available_count\
         {where_sql} ORDER BY available_count DESC,n.created_at DESC,n.id DESC LIMIT $4 OFFSET $5) t"
    );
    let items = json_rows(
        client,
        &data_sql,
        &[&search, &status, &ids, &p.size, &p.offset],
    )
    .await?;
    let totals: (i64, i64) = client
        .query_one(
            "SELECT count(*),count(*) FILTER(WHERE enabled) FROM networks",
            &[],
        )
        .await
        .map(|row| (row.get(0), row.get(1)))
        .map_err(db_failure)?;
    Ok(ConfigList {
        items,
        total: p.total,
        all_total: totals.0,
        enabled_count: totals.1,
        page: p.page,
        size: p.size,
    })
}

async fn list_nodes_response(state: &AppState, uri: &str) -> Response {
    let client = match database(state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    match node_configs(&client, uri).await {
        Ok(list) => page_headers(Json(list.items), list.total, list.page, list.size),
        Err(response) => response,
    }
}

async fn list_networks_response(state: &AppState, uri: &str) -> Response {
    let client = match database(state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    match network_configs(&client, uri).await {
        Ok(list) => page_headers(Json(list.items), list.total, list.page, list.size),
        Err(response) => response,
    }
}

async fn node_list(
    State(state): State<AppState>,
    OriginalUri(uri): OriginalUri,
    headers: HeaderMap,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    list_nodes_response(&state, uri.query().unwrap_or("")).await
}

async fn network_list(
    State(state): State<AppState>,
    OriginalUri(uri): OriginalUri,
    headers: HeaderMap,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    list_networks_response(&state, uri.query().unwrap_or("")).await
}

#[derive(Deserialize)]
struct ConfigInput {
    #[serde(default)]
    name: String,
    #[serde(default)]
    address: String,
    #[serde(default)]
    port: i64,
    #[serde(default)]
    enabled: bool,
    #[serde(default)]
    description: String,
    #[serde(default)]
    share_uri: String,
    #[serde(default)]
    cidr: String,
    #[serde(default)]
    asn: String,
    #[serde(default)]
    provider: String,
}

fn validate_node(input: &ConfigInput) -> Option<Response> {
    if input.name.trim().is_empty() || input.name.chars().count() > 80 {
        return Some(error_fields(
            StatusCode::UNPROCESSABLE_ENTITY,
            "请检查表单中的内容",
            "name",
            "名称长度需为 1–80 个字符",
        ));
    }
    if input.address.trim().is_empty()
        || input.address.len() > 253
        || input.address.contains(' ')
    {
        return Some(error_fields(
            StatusCode::UNPROCESSABLE_ENTITY,
            "请检查表单中的内容",
            "address",
            "地址格式无效",
        ));
    }
    if !(1..=65535).contains(&input.port) {
        return Some(error_fields(
            StatusCode::UNPROCESSABLE_ENTITY,
            "请检查表单中的内容",
            "port",
            "端口需为 1–65535",
        ));
    }
    if input.description.chars().count() > 500 || input.share_uri.len() > 8192 {
        return Some(error_fields(
            StatusCode::UNPROCESSABLE_ENTITY,
            "请检查表单中的内容",
            "description",
            "描述或分享链接过长",
        ));
    }
    None
}

fn validate_network(input: &ConfigInput) -> Option<Response> {
    if input.name.trim().is_empty() || input.name.chars().count() > 80 {
        return Some(error_fields(
            StatusCode::UNPROCESSABLE_ENTITY,
            "请检查表单中的内容",
            "name",
            "名称长度需为 1–80 个字符",
        ));
    }
    if input.cidr.trim().is_empty() || !input.cidr.contains('/') {
        return Some(error_fields(
            StatusCode::UNPROCESSABLE_ENTITY,
            "请检查表单中的内容",
            "cidr",
            "网段格式无效",
        ));
    }
    if input.description.chars().count() > 500
        || input.asn.chars().count() > 255
        || input.provider.chars().count() > 100
    {
        return Some(error_fields(
            StatusCode::UNPROCESSABLE_ENTITY,
            "请检查表单中的内容",
            "description",
            "描述、ASN 或服务商过长",
        ));
    }
    None
}

async fn save_node(State(state): State<AppState>, headers: HeaderMap, body: Bytes) -> Response {
    save_config(state, headers, body, "nodes", None).await
}

async fn update_node(
    State(state): State<AppState>,
    axum::extract::Path(id): axum::extract::Path<String>,
    headers: HeaderMap,
    body: Bytes,
) -> Response {
    save_config(state, headers, body, "nodes", Some(id)).await
}

async fn save_network(State(state): State<AppState>, headers: HeaderMap, body: Bytes) -> Response {
    save_config(state, headers, body, "networks", None).await
}

async fn update_network(
    State(state): State<AppState>,
    axum::extract::Path(id): axum::extract::Path<String>,
    headers: HeaderMap,
    body: Bytes,
) -> Response {
    save_config(state, headers, body, "networks", Some(id)).await
}

async fn save_config(
    state: AppState,
    headers: HeaderMap,
    body: Bytes,
    kind: &str,
    id: Option<String>,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    if let Some(id) = &id {
        if !valid_uuid(id) {
            return error(StatusCode::BAD_REQUEST, "配置 ID 无效");
        }
    }
    let input: ConfigInput = match serde_json::from_slice(&body) {
        Ok(value) => value,
        Err(_) => return error(StatusCode::BAD_REQUEST, "请求格式错误，请检查字段类型和内容"),
    };
    let validation = if kind == "nodes" {
        validate_node(&input)
    } else {
        validate_network(&input)
    };
    if let Some(response) = validation {
        return response;
    }
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let saved: Result<String, Response> = if kind == "nodes" {
        if let Some(id) = &id {
            match client
                .query_opt(
                    "UPDATE nodes SET name=$1,address=$2,port=$3,enabled=$4,description=$5,share_uri=$6,updated_at=now() WHERE id=$7::text::uuid RETURNING id::text",
                    &[
                        &input.name.trim(),
                        &input.address.trim(),
                        &(input.port as i32),
                        &input.enabled,
                        &input.description,
                        &input.share_uri,
                        &id,
                    ],
                )
                .await
            {
                Ok(Some(row)) => Ok(row.get(0)),
                Ok(None) => Err(error(StatusCode::NOT_FOUND, "配置已被删除，请刷新列表")),
                Err(err) => Err(db_failure(err)),
            }
        } else {
            client
                .query_one(
                    "INSERT INTO nodes(name,address,port,enabled,description,share_uri) VALUES($1,$2,$3,$4,$5,$6) RETURNING id::text",
                    &[
                        &input.name.trim(),
                        &input.address.trim(),
                        &(input.port as i32),
                        &input.enabled,
                        &input.description,
                        &input.share_uri,
                    ],
                )
                .await
                .map(|row| row.get(0))
                .map_err(db_failure)
        }
    } else {
        if let Some(id) = &id {
            match client
                .query_opt(
                    "UPDATE networks SET name=$1,cidr=$2::text::cidr,enabled=$3,description=$4,asn=$5,provider=$6,updated_at=now() WHERE id=$7::text::uuid RETURNING id::text",
                    &[
                        &input.name.trim(),
                        &input.cidr.trim(),
                        &input.enabled,
                        &input.description,
                        &input.asn,
                        &input.provider,
                        &id,
                    ],
                )
                .await
            {
                Ok(Some(row)) => Ok(row.get(0)),
                Ok(None) => Err(error(StatusCode::NOT_FOUND, "配置已被删除，请刷新列表")),
                Err(err) => Err(db_failure(err)),
            }
        } else {
            client
                .query_one(
                    "INSERT INTO networks(name,cidr,enabled,description,asn,provider,registry_country,registry_holder) VALUES($1,$2::text::cidr,$3,$4,$5,$6,'','') RETURNING id::text",
                    &[
                        &input.name.trim(),
                        &input.cidr.trim(),
                        &input.enabled,
                        &input.description,
                        &input.asn,
                        &input.provider,
                    ],
                )
                .await
                .map(|row| row.get(0))
                .map_err(db_failure)
        }
    };
    match saved {
        Ok(saved_id) => {
            let status = if id.is_some() {
                StatusCode::OK
            } else {
                StatusCode::CREATED
            };
            (status, Json(json!({"id": saved_id, "warning": ""}))).into_response()
        }
        Err(response) => response,
    }
}

async fn delete_node(
    State(state): State<AppState>,
    axum::extract::Path(id): axum::extract::Path<String>,
    headers: HeaderMap,
) -> Response {
    delete_config(state, headers, "nodes", id).await
}

async fn delete_network(
    State(state): State<AppState>,
    axum::extract::Path(id): axum::extract::Path<String>,
    headers: HeaderMap,
) -> Response {
    delete_config(state, headers, "networks", id).await
}

async fn delete_config(state: AppState, headers: HeaderMap, kind: &str, id: String) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    if !valid_uuid(&id) {
        return error(StatusCode::BAD_REQUEST, "配置 ID 无效");
    }
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let sql = format!("DELETE FROM {kind} WHERE id=$1::text::uuid");
    match client.execute(&sql, &[&id]).await {
        Ok(0) => error(StatusCode::NOT_FOUND, "配置已被删除，请刷新列表"),
        Ok(_) => Json(json!({"ok": true})).into_response(),
        Err(err) => db_failure(err),
    }
}

// ---------------------------------------------------------------------------
// Blacklist
// ---------------------------------------------------------------------------

async fn blacklist_list(
    State(state): State<AppState>,
    OriginalUri(uri): OriginalUri,
    headers: HeaderMap,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    blacklist_response(&client, uri.query().unwrap_or("")).await
}

async fn blacklist_response(client: &tokio_postgres::Client, uri: &str) -> Response {
    let search = query_value(uri, "search");
    let where_sql = " FROM ip_blacklist WHERE ($1='' OR position($1 in host(ip))>0)";
    let p = match load_page(
        client,
        uri,
        &format!("SELECT count(*){where_sql}"),
        &[&search],
        DEFAULT_PAGE_SIZE,
    )
    .await
    {
        Ok(value) => value,
        Err(response) => return response,
    };
    let data_sql = format!(
        "SELECT COALESCE(json_agg(t),'[]'::json)::text FROM (SELECT host(ip) AS ip,created_at{where_sql} ORDER BY ip LIMIT $2 OFFSET $3) t"
    );
    match json_rows(client, &data_sql, &[&search, &p.size, &p.offset]).await {
        Ok(items) => page_headers(Json(items), p.total, p.page, p.size),
        Err(response) => response,
    }
}

// ---------------------------------------------------------------------------
// CF targets and shared pool
// ---------------------------------------------------------------------------

const CF_STATS: &str = "(SELECT count(*) FROM cf_results r WHERE r.target_id=t.id AND r.available) AS available_count,\
 (SELECT count(*) FROM cf_ip_pool WHERE available) AS shared_count,\
 COALESCE((SELECT round(100.0*success/NULLIF(total,0),2) FROM cf_target_rounds k WHERE k.target_id=t.id ORDER BY finished_at DESC LIMIT 1),\
 (SELECT round(100.0*count(*) FILTER (WHERE r.available)/NULLIF(count(*),0),2) FROM cf_results r WHERE r.target_id=t.id)) AS availability,\
 COALESCE((SELECT round(100.0*sum(success)/NULLIF(sum(total),0),2) FROM cf_target_rounds k WHERE k.target_id=t.id),\
 (SELECT round(100.0*count(*) FILTER (WHERE r.available)/NULLIF(count(*),0),2) FROM cf_results r WHERE r.target_id=t.id)) AS overall,\
 (SELECT count(*) FROM cf_target_rounds k WHERE k.target_id=t.id) AS rounds,\
 COALESCE((SELECT sum(total) FROM cf_target_rounds k WHERE k.target_id=t.id),\
 (SELECT count(*) FROM cf_results r WHERE r.target_id=t.id)) AS checked_total,\
 COALESCE((SELECT sum(success) FROM cf_target_rounds k WHERE k.target_id=t.id),\
 (SELECT count(*) FROM cf_results r WHERE r.target_id=t.id AND r.available)) AS checked_success";

async fn cf_targets_response(client: &tokio_postgres::Client, uri: &str) -> Response {
    let search = query_value(uri, "search");
    let where_sql = " FROM cf_targets t WHERE ($1='' OR position(lower($1) in lower(t.name||' '||t.url))>0)";
    let p = match load_page(
        client,
        uri,
        &format!("SELECT count(*){where_sql}"),
        &[&search],
        DEFAULT_PAGE_SIZE,
    )
    .await
    {
        Ok(value) => value,
        Err(response) => return response,
    };
    let data_sql = format!(
        "WITH selected AS MATERIALIZED (SELECT t.*{where_sql} ORDER BY t.created_at DESC,t.id DESC LIMIT $2 OFFSET $3)\
         SELECT COALESCE(json_agg(t),'[]'::json)::text FROM (SELECT t.*,{CF_STATS} FROM selected t ORDER BY t.created_at DESC,t.id DESC) t"
    );
    match json_rows(client, &data_sql, &[&search, &p.size, &p.offset]).await {
        Ok(items) => page_headers(Json(items), p.total, p.page, p.size),
        Err(response) => response,
    }
}

async fn cf_target_list(
    State(state): State<AppState>,
    OriginalUri(uri): OriginalUri,
    headers: HeaderMap,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    cf_targets_response(&client, uri.query().unwrap_or("")).await
}

async fn cf_pool_response(client: &tokio_postgres::Client, uri: &str) -> Response {
    let search = query_value(uri, "search");
    let where_sql = " FROM cf_ip_pool s WHERE s.available AND NOT EXISTS(SELECT 1 FROM ip_blacklist b WHERE b.ip=s.ip) AND ($1='' OR position($1 in host(s.ip))>0)";
    let p = match load_page(
        client,
        uri,
        &format!("SELECT count(*){where_sql}"),
        &[&search],
        20,
    )
    .await
    {
        Ok(value) => value,
        Err(response) => return response,
    };
    let data_sql = format!(
        "WITH selected AS MATERIALIZED (SELECT s.ip,s.http_status,s.checked_at{where_sql} ORDER BY s.ip LIMIT $2 OFFSET $3)\
         SELECT COALESCE(json_agg(r),'[]'::json)::text FROM (SELECT host(s.ip) AS ip,s.http_status,s.checked_at,\
         COALESCE(info.asn,'') AS asn,COALESCE(info.provider,'') AS provider FROM selected s \
         LEFT JOIN LATERAL (SELECT w.asn,w.provider FROM networks w WHERE w.enabled AND s.ip <<= w.cidr AND (w.asn<>'' OR w.provider<>'') ORDER BY masklen(w.cidr) DESC,w.updated_at DESC,w.id LIMIT 1) info ON true ORDER BY s.ip) r"
    );
    match json_rows(client, &data_sql, &[&search, &p.size, &p.offset]).await {
        Ok(items) => page_headers(Json(items), p.total, p.page, p.size),
        Err(response) => response,
    }
}

async fn cf_pool_list(
    State(state): State<AppState>,
    OriginalUri(uri): OriginalUri,
    headers: HeaderMap,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    cf_pool_response(&client, uri.query().unwrap_or("")).await
}

async fn create_cf_target(
    State(state): State<AppState>,
    headers: HeaderMap,
    body: Bytes,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    let input: Value = match serde_json::from_slice(&body) {
        Ok(value) => value,
        Err(_) => return error(StatusCode::BAD_REQUEST, "请求格式错误，请检查字段类型和内容"),
    };
    let name = input.get("name").and_then(Value::as_str).unwrap_or("").trim();
    let raw_url = input.get("url").and_then(Value::as_str).unwrap_or("").trim();
    if name.is_empty()
        || name.chars().count() > 100
        || raw_url.len() > 2048
        || !raw_url.starts_with("https://")
        || raw_url.contains('@')
        || raw_url.contains('#')
        || raw_url.split("://").nth(1).unwrap_or("").is_empty()
    {
        return error(
            StatusCode::UNPROCESSABLE_ENTITY,
            "请输入名称及有效的 HTTPS 网址（不含账号或片段）",
        );
    }
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    match client
        .query_opt(
            "INSERT INTO cf_targets(name,url) VALUES($1,$2) ON CONFLICT(url) DO NOTHING RETURNING id::text",
            &[&name, &raw_url],
        )
        .await
    {
        Ok(Some(row)) => {
            let id: String = row.get(0);
            (StatusCode::CREATED, Json(json!({"id": id}))).into_response()
        }
        Ok(None) => error(StatusCode::CONFLICT, "目标地址已存在，请返回列表查看"),
        Err(err) => db_failure(err),
    }
}

async fn delete_cf_target(
    State(state): State<AppState>,
    axum::extract::Path(id): axum::extract::Path<String>,
    headers: HeaderMap,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    if !valid_uuid(&id) {
        return error(StatusCode::NOT_FOUND, "目标地址不存在");
    }
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    match client
        .execute("DELETE FROM cf_targets WHERE id=$1::text::uuid", &[&id])
        .await
    {
        Ok(0) => error(StatusCode::NOT_FOUND, "目标地址不存在"),
        Ok(_) => Json(json!({"ok": true})).into_response(),
        Err(err) => db_failure(err),
    }
}

async fn get_cf_target(
    State(state): State<AppState>,
    axum::extract::Path(id): axum::extract::Path<String>,
    OriginalUri(uri): OriginalUri,
    headers: HeaderMap,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    if !valid_uuid(&id) {
        return error(StatusCode::NOT_FOUND, "目标地址不存在");
    }
    let search = query_value(uri.query().unwrap_or(""), "search");
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let target_sql = format!(
        "SELECT COALESCE(row_to_json(t)::text,'null') FROM (SELECT t.*,{CF_STATS} FROM cf_targets t WHERE id=$1::text::uuid) t"
    );
    let target_text: String = match client.query_one(&target_sql, &[&id]).await {
        Ok(row) => row.get(0),
        Err(err) => return db_failure(err),
    };
    let target: Value = match serde_json::from_str(&target_text) {
        Ok(Value::Null) => return error(StatusCode::NOT_FOUND, "目标地址不存在"),
        Ok(value) => value,
        Err(_) => return error(StatusCode::SERVICE_UNAVAILABLE, "数据库暂时不可用，请稍后重试"),
    };
    let page = match page_number(uri.query().unwrap_or("")) {
        Ok(value) => value,
        Err(response) => return response,
    };
    let count_sql = "SELECT count(*) FROM cf_results WHERE target_id=$1::text::uuid AND available AND ($2='' OR position($2 in host(ip))>0)";
    let total = match count(&client, count_sql, &[&id, &search]).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let page = normalized_page(page, total, 20);
    let items_sql = "WITH selected AS MATERIALIZED (SELECT ip,http_status,checked_at FROM cf_results WHERE target_id=$1::text::uuid AND available AND ($2='' OR position($2 in host(ip))>0) ORDER BY ip LIMIT $3 OFFSET $4)\
         SELECT COALESCE(json_agg(r),'[]'::json)::text FROM (SELECT host(s.ip) AS ip,s.http_status,s.checked_at,COALESCE(info.asn,'') AS asn,COALESCE(info.provider,'') AS provider FROM selected s LEFT JOIN LATERAL (SELECT w.asn,w.provider FROM networks w WHERE w.enabled AND s.ip <<= w.cidr AND (w.asn<>'' OR w.provider<>'') ORDER BY masklen(w.cidr) DESC,w.updated_at DESC,w.id LIMIT 1) info ON true ORDER BY s.ip) r";
    let items = match json_rows(&client, items_sql, &[&id, &search, &20i64, &page_offset(page, 20)]).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let history_sql = "SELECT COALESCE(json_agg(k),'[]'::json)::text FROM (SELECT finished_at,total,success,status FROM cf_target_rounds WHERE target_id=$1::text::uuid ORDER BY finished_at DESC LIMIT 12) k";
    let history = match json_rows(&client, history_sql, &[&id]).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    page_headers(
        Json(json!({"target": target, "items": items, "history": history})),
        total,
        page,
        20,
    )
}

// ---------------------------------------------------------------------------
// Latency probes
// ---------------------------------------------------------------------------

const PROBE_ONLINE: &str = "COALESCE(enabled AND last_seen>now()-make_interval(secs=>CASE WHEN heartbeat_at IS NOT NULL THEN 90 ELSE GREATEST(90,interval_seconds*3) END),false)";
const PROBE_TARGET_POOL: &str = "SELECT ip FROM shared_node_ip_pool WHERE available UNION SELECT ip FROM cf_ip_pool WHERE available";
const PROBE_WINDOW: &str = "(interval_seconds + CEIL((SELECT count(*) FROM (SELECT ip FROM shared_node_ip_pool WHERE available UNION SELECT ip FROM cf_ip_pool WHERE available) targets)::numeric / concurrency)::integer * (CEIL(timeout_ms::numeric/1000)::integer+2) + 30)";

async fn latency_probes_response(client: &tokio_postgres::Client, uri: &str) -> Response {
    let id = query_value(uri, "id");
    let ids = query_value(uri, "ids");
    let search = query_value(uri, "search").trim().to_string();
    let filter = " WHERE ($1='' OR id::text=$1) AND ($2='' OR id::text=ANY(string_to_array($2,','))) AND ($3='' OR name ILIKE '%'||$3||'%')";
    let p = match load_page(
        client,
        uri,
        &format!("SELECT count(*) FROM latency_probes{filter}"),
        &[&id, &ids, &search],
        DEFAULT_PAGE_SIZE,
    )
    .await
    {
        Ok(value) => value,
        Err(response) => return response,
    };
    let sql = format!(
        "SELECT COALESCE(json_agg(t),'[]'::json)::text FROM (SELECT id::text,name,mode,tcp_port,interval_seconds,timeout_ms,concurrency,interface_name,enabled,last_seen,created_at,{PROBE_ONLINE} AS online,{PROBE_WINDOW} AS sample_window_seconds FROM latency_probes{filter} ORDER BY created_at DESC,id DESC LIMIT $4 OFFSET $5) t"
    );
    match json_rows(client, &sql, &[&id, &ids, &search, &p.size, &p.offset]).await {
        Ok(items) => page_headers(Json(items), p.total, p.page, p.size),
        Err(response) => response,
    }
}

async fn latency_probe_list(
    State(state): State<AppState>,
    OriginalUri(uri): OriginalUri,
    headers: HeaderMap,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    latency_probes_response(&client, uri.query().unwrap_or("")).await
}

// ---------------------------------------------------------------------------
// Shared list payload for the browser
// ---------------------------------------------------------------------------

async fn cf_detail_payload(state: &AppState, uri: &str) -> Response {
    let id = query_value(uri, "id");
    if !valid_uuid(&id) {
        return error(StatusCode::NOT_FOUND, "目标地址不存在");
    }
    let client = match database(state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let target_sql = format!(
        "SELECT COALESCE(row_to_json(t)::text,'null') FROM (SELECT t.*,{CF_STATS} FROM cf_targets t WHERE id=$1::text::uuid) t"
    );
    let target_text: String = match client.query_one(&target_sql, &[&id]).await {
        Ok(row) => row.get(0),
        Err(err) => return db_failure(err),
    };
    let target: Value = match serde_json::from_str(&target_text) {
        Ok(Value::Null) => return error(StatusCode::NOT_FOUND, "目标地址不存在"),
        Ok(value) => value,
        Err(_) => return error(StatusCode::SERVICE_UNAVAILABLE, "数据库暂时不可用，请稍后重试"),
    };
    let search = query_value(uri, "search");
    let requested = match page_number(uri) {
        Ok(value) => value,
        Err(response) => return response,
    };
    let count_sql = "SELECT count(*) FROM cf_results WHERE target_id=$1::text::uuid AND available AND ($2='' OR position($2 in host(ip))>0)";
    let total = match count(&client, count_sql, &[&id, &search]).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let page = normalized_page(requested, total, 20);
    let items_sql = "WITH selected AS MATERIALIZED (SELECT ip,http_status,checked_at FROM cf_results WHERE target_id=$1::text::uuid AND available AND ($2='' OR position($2 in host(ip))>0) ORDER BY ip LIMIT $3 OFFSET $4)\
         SELECT COALESCE(json_agg(r),'[]'::json)::text FROM (SELECT host(s.ip) AS ip,s.http_status,s.checked_at,COALESCE(info.asn,'') AS asn,COALESCE(info.provider,'') AS provider FROM selected s LEFT JOIN LATERAL (SELECT w.asn,w.provider FROM networks w WHERE w.enabled AND s.ip <<= w.cidr AND (w.asn<>'' OR w.provider<>'') ORDER BY masklen(w.cidr) DESC,w.updated_at DESC,w.id LIMIT 1) info ON true ORDER BY s.ip) r";
    let items = match json_rows(&client, items_sql, &[&id, &search, &20i64, &page_offset(page, 20)]).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let history_sql = "SELECT COALESCE(json_agg(k),'[]'::json)::text FROM (SELECT finished_at,total,success,status FROM cf_target_rounds WHERE target_id=$1::text::uuid ORDER BY finished_at DESC LIMIT 12) k";
    let history = match json_rows(&client, history_sql, &[&id]).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    page_headers(
        Json(json!({"target": target, "items": items, "history": history})),
        total,
        page,
        20,
    )
}

async fn scan_detail_payload(state: &AppState, uri: &str) -> Response {
    let raw = query_value(uri, "id");
    let Ok(id) = raw.parse::<i64>() else {
        return error(StatusCode::NOT_FOUND, "检测记录不存在或已被清理");
    };
    let client = match database(state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let sql = "SELECT row_to_json(t)::text FROM (SELECT id::text,job_id,round,protocol,node_name,scope,url,cidrs,input,started_at,finished_at,total,completed,success,status,error,results,updated_at,false AS live FROM node_scan_rounds WHERE id=$1) t";
    let text: String = match client.query_one(sql, &[&id]).await {
        Ok(row) => row.get(0),
        Err(err) => return db_failure(err),
    };
    match serde_json::from_str::<Value>(&text) {
        Ok(Value::Null) => error(StatusCode::NOT_FOUND, "检测记录不存在或已被清理"),
        Ok(value) => Json(value).into_response(),
        Err(_) => error(StatusCode::SERVICE_UNAVAILABLE, "数据库暂时不可用，请稍后重试"),
    }
}

pub async fn list_payload(state: &AppState, kind: &str, uri: &str) -> Option<Response> {
    let client = match database(state).await {
        Ok(value) => value,
        Err(response) => return Some(response),
    };
    match kind {
        "nodes" => match node_configs(&client, uri).await {
            Ok(list) => Some(page_headers(
                Json({
                    let mut object = page_object(list.items, list.total, list.page, list.size);
                    if let Value::Object(map) = &mut object {
                        map.insert("allTotal".into(), json!(list.all_total));
                        map.insert("enabledCount".into(), json!(list.enabled_count));
                    }
                    object
                }),
                list.total,
                list.page,
                list.size,
            )),
            Err(response) => Some(response),
        },
        "networks" => match network_configs(&client, uri).await {
            Ok(list) => Some(page_headers(
                Json({
                    let mut object = page_object(list.items, list.total, list.page, list.size);
                    if let Value::Object(map) = &mut object {
                        map.insert("allTotal".into(), json!(list.all_total));
                        map.insert("enabledCount".into(), json!(list.enabled_count));
                    }
                    object
                }),
                list.total,
                list.page,
                list.size,
            )),
            Err(response) => Some(response),
        },
        "blacklist" => Some(blacklist_response(&client, uri).await),
        "cf-targets" => Some(cf_targets_response(&client, uri).await),
        "cf-pool" => Some(cf_pool_response(&client, uri).await),
        "latency-probes" => Some(latency_probes_response(&client, uri).await),
        "certificate-issues" => match certificate_issues_data(&client, uri).await {
            Ok((items, total, page, size)) => Some(Json(page_object(items, total, page, size)).into_response()),
            Err(response) => Some(response),
        },
        "available-ips" => match available_ip_data(&client, uri).await {
            Ok((items, total, page, size, meta)) => {
                let mut object = page_object(items, total, page, size);
                if let (Value::Object(map), Value::Object(extra)) = (&mut object, meta) {
                    for (key, value) in extra {
                        map.insert(key, value);
                    }
                }
                Some(Json(object).into_response())
            }
            Err(response) => Some(response),
        },
        "dns-records" | "dns-candidates" | "dns-history" => {
            let kind = kind.trim_start_matches("dns-");
            Some(match dns_items_data(&client, kind, uri).await {
                Ok((items, total, page, size)) => Json(page_object(items, total, page, size)).into_response(),
                Err(response) => response,
            })
        }
        "cf-detail" => Some(cf_detail_payload(state, uri).await),
        "scan-detail" => Some(scan_detail_payload(state, uri).await),
        _ => None,
    }
}

// ---------------------------------------------------------------------------
// Latency probe CRUD, deployment credentials and agent reporting
// ---------------------------------------------------------------------------

use aes_gcm::aead::{Aead, KeyInit};
use aes_gcm::{Aes256Gcm, Nonce};
use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use sha2::{Digest, Sha256};

struct ProbeInput {
    name: String,
    mode: String,
    tcp_port: i64,
    interval_seconds: i64,
    timeout_ms: i64,
    concurrency: i64,
    interface_name: String,
    enabled: bool,
}

fn probe_input(value: &Value) -> ProbeInput {
    let mode = value
        .get("mode")
        .and_then(Value::as_str)
        .filter(|v| !v.is_empty())
        .unwrap_or("icmp")
        .to_string();
    let mut tcp_port = value.get("tcp_port").and_then(Value::as_i64).unwrap_or(0);
    if tcp_port == 0 {
        tcp_port = 443;
    }
    ProbeInput {
        name: value
            .get("name")
            .and_then(Value::as_str)
            .unwrap_or("")
            .trim()
            .to_string(),
        mode,
        tcp_port,
        interval_seconds: value.get("interval_seconds").and_then(Value::as_i64).unwrap_or(20),
        timeout_ms: value.get("timeout_ms").and_then(Value::as_i64).unwrap_or(1000),
        concurrency: value.get("concurrency").and_then(Value::as_i64).unwrap_or(64),
        interface_name: value
            .get("interface_name")
            .and_then(Value::as_str)
            .unwrap_or("")
            .trim()
            .to_string(),
        enabled: value.get("enabled").and_then(Value::as_bool).unwrap_or(true),
    }
}

fn validate_probe(input: &ProbeInput, create: bool) -> Option<Response> {
    if input.name.is_empty() || input.name.chars().count() > 80 {
        return Some(error_fields(
            StatusCode::UNPROCESSABLE_ENTITY,
            "请检查探针配置",
            "name",
            "名称长度需为 1–80 个字符",
        ));
    }
    if input.mode != "icmp" && input.mode != "tcp" {
        return Some(error_fields(StatusCode::UNPROCESSABLE_ENTITY, "请检查探针配置", "mode", "检测模式需为 ICMP 或 TCP"));
    }
    if !(1..=65535).contains(&input.tcp_port) {
        return Some(error_fields(StatusCode::UNPROCESSABLE_ENTITY, "请检查探针配置", "tcp_port", "TCP 端口需为 1–65535"));
    }
    if !(15..=86400).contains(&input.interval_seconds) {
        return Some(error_fields(StatusCode::UNPROCESSABLE_ENTITY, "请检查探针配置", "interval_seconds", "检测间隔需为 15–86400 秒"));
    }
    if !(250..=10000).contains(&input.timeout_ms) {
        return Some(error_fields(StatusCode::UNPROCESSABLE_ENTITY, "请检查探针配置", "timeout_ms", "超时需为 250–10000 毫秒"));
    }
    if !(1..=512).contains(&input.concurrency) {
        return Some(error_fields(StatusCode::UNPROCESSABLE_ENTITY, "请检查探针配置", "concurrency", "并发数需为 1–512"));
    }
    if input.interface_name.chars().count() > 100 {
        return Some(error_fields(StatusCode::UNPROCESSABLE_ENTITY, "请检查探针配置", "interface_name", "网卡名称不能超过 100 个字符"));
    }
    if create && !input.enabled {
        // create forces enabled in the SQL below
    }
    None
}

fn probe_cipher() -> Result<Aes256Gcm, String> {
    let raw = std::env::var("PROBE_CREDENTIAL_KEY")
        .map_err(|_| "请配置固定的 PROBE_CREDENTIAL_KEY（32 字节 base64），用于加密保存探针密钥".to_string())?;
    let key = BASE64
        .decode(raw)
        .map_err(|_| "PROBE_CREDENTIAL_KEY 必须是 32 字节 base64".to_string())?;
    if key.len() != 32 {
        return Err("PROBE_CREDENTIAL_KEY 必须是 32 字节 base64".to_string());
    }
    Aes256Gcm::new_from_slice(&key).map_err(|_| "PROBE_CREDENTIAL_KEY 无效".to_string())
}

fn encrypt_probe_token(token: &str) -> Result<Vec<u8>, String> {
    let cipher = probe_cipher()?;
    let mut nonce = [0u8; 12];
    rand::thread_rng().fill_bytes(&mut nonce);
    let sealed = cipher
        .encrypt(Nonce::from_slice(&nonce), token.as_bytes())
        .map_err(|_| "探针密钥加密失败".to_string())?;
    let mut value = Vec::with_capacity(nonce.len() + sealed.len());
    value.extend_from_slice(&nonce);
    value.extend_from_slice(&sealed);
    Ok(value)
}

fn decrypt_probe_token(encrypted: &[u8]) -> Result<String, String> {
    if encrypted.is_empty() {
        return Ok(String::new());
    }
    let cipher = probe_cipher()?;
    if encrypted.len() < 12 {
        return Err("探针密钥密文无效".to_string());
    }
    let plain = cipher
        .decrypt(Nonce::from_slice(&encrypted[..12]), &encrypted[12..])
        .map_err(|_| "探针密钥解密失败，请检查服务器加密配置".to_string())?;
    String::from_utf8(plain).map_err(|_| "探针密钥解密失败".to_string())
}

fn new_probe_token() -> (String, Vec<u8>) {
    let mut secret = [0u8; 32];
    rand::thread_rng().fill_bytes(&mut secret);
    let token = URL_SAFE_NO_PAD.encode(secret);
    let digest = Sha256::digest(token.as_bytes()).to_vec();
    (token, digest)
}

fn probe_token_hash(token: &str) -> Vec<u8> {
    Sha256::digest(token.as_bytes()).to_vec()
}

async fn probe_json(client: &tokio_postgres::Client, id: &str) -> Result<Option<Value>, Response> {
    let sql = format!(
        "SELECT row_to_json(t)::text FROM (SELECT id::text,name,mode,tcp_port,interval_seconds,timeout_ms,concurrency,interface_name,enabled,last_seen,created_at,{PROBE_ONLINE} AS online FROM latency_probes WHERE id=$1::text::uuid) t"
    );
    let text: String = client
        .query_one(&sql, &[&id])
        .await
        .map(|row| row.get(0))
        .map_err(db_failure)?;
    match serde_json::from_str::<Value>(&text) {
        Ok(Value::Null) => Ok(None),
        Ok(value) => Ok(Some(value)),
        Err(_) => Err(error(StatusCode::SERVICE_UNAVAILABLE, "数据库暂时不可用，请稍后重试")),
    }
}

async fn create_probe(State(state): State<AppState>, headers: HeaderMap, body: Bytes) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    let value: Value = match serde_json::from_slice(&body) {
        Ok(value) => value,
        Err(_) => return error(StatusCode::BAD_REQUEST, "请求格式错误，请检查字段类型和内容"),
    };
    let input = probe_input(&value);
    if let Some(response) = validate_probe(&input, true) {
        return response;
    }
    let (token, token_hash) = new_probe_token();
    let encrypted = match encrypt_probe_token(&token) {
        Ok(value) => value,
        Err(message) => return error(StatusCode::SERVICE_UNAVAILABLE, &message),
    };
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let row = client
        .query_one(
            "INSERT INTO latency_probes(name,token_hash,interval_seconds,timeout_ms,concurrency,interface_name,enabled,token_encrypted,mode,tcp_port) VALUES($1,$2,$3,$4,$5,$6,true,$7,$8,$9) RETURNING id::text",
            &[
                &input.name,
                &token_hash.as_slice(),
                &(input.interval_seconds as i32),
                &(input.timeout_ms as i32),
                &(input.concurrency as i32),
                &input.interface_name,
                &encrypted,
                &input.mode,
                &(input.tcp_port as i32),
            ],
        )
        .await;
    let id: String = match row {
        Ok(row) => row.get(0),
        Err(err) => return db_failure(err),
    };
    match probe_json(&client, &id).await {
        Ok(Some(probe)) => (StatusCode::CREATED, Json(json!({"probe": probe, "token": token}))).into_response(),
        Ok(None) => error(StatusCode::SERVICE_UNAVAILABLE, "数据库暂时不可用，请稍后重试"),
        Err(response) => response,
    }
}

async fn update_probe(
    State(state): State<AppState>,
    axum::extract::Path(id): axum::extract::Path<String>,
    headers: HeaderMap,
    body: Bytes,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    if !valid_uuid(&id) {
        return error(StatusCode::BAD_REQUEST, "探针 ID 无效");
    }
    let value: Value = match serde_json::from_slice(&body) {
        Ok(value) => value,
        Err(_) => return error(StatusCode::BAD_REQUEST, "请求格式错误，请检查字段类型和内容"),
    };
    let input = probe_input(&value);
    if let Some(response) = validate_probe(&input, false) {
        return response;
    }
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let updated = client
        .query_opt(
            "UPDATE latency_probes SET name=$2,interval_seconds=$3,timeout_ms=$4,concurrency=$5,interface_name=$6,enabled=$7,mode=$8,tcp_port=$9 WHERE id=$1::text::uuid RETURNING id::text",
            &[
                &id,
                &input.name,
                &(input.interval_seconds as i32),
                &(input.timeout_ms as i32),
                &(input.concurrency as i32),
                &input.interface_name,
                &input.enabled,
                &input.mode,
                &(input.tcp_port as i32),
            ],
        )
        .await;
    match updated {
        Ok(Some(_)) => match probe_json(&client, &id).await {
            Ok(Some(probe)) => Json(probe).into_response(),
            Ok(None) => error(StatusCode::NOT_FOUND, "探针不存在"),
            Err(response) => response,
        },
        Ok(None) => error(StatusCode::NOT_FOUND, "探针不存在"),
        Err(err) => db_failure(err),
    }
}

async fn delete_probe(
    State(state): State<AppState>,
    axum::extract::Path(id): axum::extract::Path<String>,
    headers: HeaderMap,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    if !valid_uuid(&id) {
        return error(StatusCode::BAD_REQUEST, "探针 ID 无效");
    }
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    match client.execute("DELETE FROM latency_probes WHERE id=$1::text::uuid", &[&id]).await {
        Ok(0) => error(StatusCode::NOT_FOUND, "探针不存在"),
        Ok(_) => Json(json!({"ok": true})).into_response(),
        Err(err) => db_failure(err),
    }
}

async fn rotate_probe_token(
    State(state): State<AppState>,
    axum::extract::Path(id): axum::extract::Path<String>,
    headers: HeaderMap,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    if !valid_uuid(&id) {
        return error(StatusCode::BAD_REQUEST, "探针 ID 无效");
    }
    let (token, token_hash) = new_probe_token();
    let encrypted = match encrypt_probe_token(&token) {
        Ok(value) => value,
        Err(message) => return error(StatusCode::SERVICE_UNAVAILABLE, &message),
    };
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    match client
        .execute(
            "UPDATE latency_probes SET token_hash=$2,token_encrypted=$3 WHERE id=$1::text::uuid",
            &[&id, &token_hash.as_slice(), &encrypted],
        )
        .await
    {
        Ok(0) => error(StatusCode::NOT_FOUND, "探针不存在"),
        Ok(_) => Json(json!({"token": token})).into_response(),
        Err(err) => db_failure(err),
    }
}

async fn probe_deployment(
    State(state): State<AppState>,
    axum::extract::Path(id): axum::extract::Path<String>,
    method: Method,
    headers: HeaderMap,
    body: Bytes,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    if !valid_uuid(&id) {
        return error(StatusCode::BAD_REQUEST, "探针 ID 无效");
    }
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    if method == Method::PUT {
        let value: Value = match serde_json::from_slice(&body) {
            Ok(value) => value,
            Err(_) => return error(StatusCode::BAD_REQUEST, "请求格式错误，请检查字段类型和内容"),
        };
        let token = value.get("token").and_then(Value::as_str).unwrap_or("");
        let platform = value.get("platform").and_then(Value::as_str).unwrap_or("");
        if !matches!(platform, "darwin" | "linux" | "windows") {
            return error(StatusCode::UNPROCESSABLE_ENTITY, "请选择有效系统");
        }
        let encrypted = if token.is_empty() {
            None
        } else {
            match encrypt_probe_token(token) {
                Ok(value) => Some(value),
                Err(message) => return error(StatusCode::SERVICE_UNAVAILABLE, &message),
            }
        };
        let result = if let Some(encrypted) = encrypted {
            client
                .execute(
                    "UPDATE latency_probes SET deployment_platform=$2,token_encrypted=$3 WHERE id=$1::text::uuid AND token_hash=$4",
                    &[&id, &platform, &encrypted, &probe_token_hash(token)],
                )
                .await
        } else {
            client
                .execute(
                    "UPDATE latency_probes SET deployment_platform=$2 WHERE id=$1::text::uuid",
                    &[&id, &platform],
                )
                .await
        };
        match result {
            Ok(0) => return error(StatusCode::UNPROCESSABLE_ENTITY, "探针不存在或填写的密钥与当前密钥不符；保存不会更换密钥"),
            Ok(_) => {}
            Err(err) => return db_failure(err),
        }
    }
    let row = client
        .query_one(
            "SELECT token_encrypted,deployment_platform FROM latency_probes WHERE id=$1::text::uuid",
            &[&id],
        )
        .await;
    let (encrypted, platform): (Option<Vec<u8>>, String) = match row {
        Ok(row) => (row.get(0), row.get(1)),
        Err(err) => return db_failure(err),
    };
    let token = match decrypt_probe_token(encrypted.as_deref().unwrap_or(&[])) {
        Ok(value) => value,
        Err(message) => return error(StatusCode::SERVICE_UNAVAILABLE, &message),
    };
    Json(json!({"token": token, "platform": platform})).into_response()
}

async fn probe_history(
    State(state): State<AppState>,
    axum::extract::Path(id): axum::extract::Path<String>,
    OriginalUri(uri): OriginalUri,
    headers: HeaderMap,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    if !valid_uuid(&id) {
        return error(StatusCode::BAD_REQUEST, "探针 ID 无效");
    }
    let raw_ip = query_value(uri.query().unwrap_or(""), "ip");
    let Ok(ip) = raw_ip.parse::<std::net::IpAddr>() else {
        return error(StatusCode::BAD_REQUEST, "IP 地址无效");
    };
    let hours = query_value(uri.query().unwrap_or(""), "hours")
        .parse::<i32>()
        .ok()
        .filter(|h| (1..=24).contains(h))
        .unwrap_or(24);
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let exists = client
        .query_one("SELECT EXISTS(SELECT 1 FROM latency_probes WHERE id=$1::text::uuid)", &[&id])
        .await
        .map(|row| row.get::<_, bool>(0));
    match exists {
        Ok(false) => return error(StatusCode::NOT_FOUND, "探针不存在"),
        Err(err) => return db_failure(err),
        _ => {}
    }
    let sql = "SELECT COALESCE(json_agg(t),'[]'::json)::text FROM (SELECT r.id,host(r.ip) AS ip,r.delay_ms,r.success,r.checked_at FROM latency_probe_results r JOIN latency_probes p ON r.probe_id=p.id AND r.mode=p.mode AND r.tcp_port=CASE WHEN p.mode='tcp' THEN p.tcp_port ELSE 0 END WHERE r.probe_id=$1::text::uuid AND r.ip=$2::text::inet AND r.checked_at>=now()-make_interval(hours=>$3) ORDER BY r.checked_at,r.id LIMIT 1000) t";
    match json_rows(&client, sql, &[&id, &ip.to_string(), &hours]).await {
        Ok(items) => Json(items).into_response(),
        Err(response) => response,
    }
}

async fn authorize_probe(
    state: &AppState,
    headers: &HeaderMap,
) -> Result<(deadpool_postgres::Client, String), Response> {
    let value = headers
        .get(header::AUTHORIZATION)
        .and_then(|v| v.to_str().ok())
        .unwrap_or("")
        .trim();
    if value.len() < 8 || !value[..7].eq_ignore_ascii_case("bearer ") {
        return Err(error(StatusCode::UNAUTHORIZED, "探针密钥无效或已停用"));
    }
    let token = value[7..].trim();
    if !(32..=128).contains(&token.len()) {
        return Err(error(StatusCode::UNAUTHORIZED, "探针密钥无效或已停用"));
    }
    let client = database(state).await?;
    let row = client
        .query_opt(
            "SELECT id::text FROM latency_probes WHERE token_hash=$1 AND enabled",
            &[&probe_token_hash(token).as_slice()],
        )
        .await
        .map_err(db_failure)?;
    match row {
        Some(row) => Ok((client, row.get(0))),
        None => Err(error(StatusCode::UNAUTHORIZED, "探针密钥无效或已停用")),
    }
}

async fn agent_targets(State(state): State<AppState>, headers: HeaderMap) -> Response {
    let (client, id) = match authorize_probe(&state, &headers).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let settings = client
        .query_one(
            "UPDATE latency_probes SET last_seen=now() WHERE id=$1::text::uuid RETURNING interval_seconds,timeout_ms,concurrency,interface_name,mode,tcp_port",
            &[&id],
        )
        .await;
    let (interval, timeout, concurrency, interface_name, mode, port): (i32, i32, i32, String, String, i32) =
        match settings {
            Ok(row) => (row.get(0), row.get(1), row.get(2), row.get(3), row.get(4), row.get(5)),
            Err(err) => return db_failure(err),
        };
    if mode == "tcp"
        && headers.get("x-probe-features").and_then(|v| v.to_str().ok()) != Some("tcp-ping-v1")
    {
        return error(StatusCode::CONFLICT, "当前配置为 TCP Ping，请重新执行一键部署命令升级探针");
    }
    let sql = format!(
        "SELECT COALESCE(json_agg(host(ip) ORDER BY ip),'[]'::json)::text FROM (SELECT DISTINCT ip FROM ({PROBE_TARGET_POOL}) p WHERE NOT EXISTS(SELECT 1 FROM ip_blacklist b WHERE b.ip=p.ip)) t"
    );
    let text: String = match client.query_one(&sql, &[]).await {
        Ok(row) => row.get(0),
        Err(err) => return db_failure(err),
    };
    let ips: Value = match serde_json::from_str(&text) {
        Ok(value) => value,
        Err(_) => return error(StatusCode::SERVICE_UNAVAILABLE, "数据库暂时不可用，请稍后重试"),
    };
    Json(json!({
        "ips": ips,
        "interval_seconds": interval,
        "timeout_ms": timeout,
        "concurrency": concurrency,
        "interface_name": interface_name,
        "mode": mode,
        "tcp_port": port,
        "retention_days": 1,
        "raw_retention_hours": 2,
        "history_bucket_seconds": 900,
    }))
    .into_response()
}

#[derive(serde::Deserialize)]
struct AgentResultInput {
    ip: Option<String>,
    delay_ms: Option<f64>,
    success: Option<bool>,
    checked_at: Option<String>,
}

#[derive(serde::Deserialize)]
struct AgentResultsBatch {
    #[serde(default)]
    results: Vec<AgentResultInput>,
    #[serde(default)]
    mode: Option<String>,
    #[serde(default)]
    tcp_port: Option<i64>,
}

#[derive(serde::Serialize)]
struct AgentResultRow<'a> {
    ip: &'a str,
    delay_ms: f64,
    success: bool,
    checked_at: String,
}

async fn agent_results(State(state): State<AppState>, headers: HeaderMap, body: Bytes) -> Response {
    let (client, id) = match authorize_probe(&state, &headers).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let input: AgentResultsBatch = match serde_json::from_slice(&body) {
        Ok(value) => value,
        Err(_) => return error(StatusCode::BAD_REQUEST, "上报数据格式错误"),
    };
    if input.results.is_empty() || input.results.len() > 10000 {
        return error(StatusCode::UNPROCESSABLE_ENTITY, "每批需包含 1–10000 条检测结果");
    }
    let mode = input.mode.as_deref().unwrap_or("icmp");
    let port = input.tcp_port.unwrap_or(0);
    if (mode != "icmp" && mode != "tcp") || (mode == "icmp" && port != 0) || (mode == "tcp" && !(1..=65535).contains(&port)) {
        return error(StatusCode::UNPROCESSABLE_ENTITY, "检测模式或端口无效");
    }
    let probe_settings = client
        .query_one(
            "SELECT mode,CASE WHEN mode='tcp' THEN tcp_port ELSE 0 END FROM latency_probes WHERE id=$1::text::uuid AND enabled",
            &[&id],
        )
        .await;
    let (probe_mode, probe_port): (String, i32) = match probe_settings {
        Ok(row) => (row.get(0), row.get(1)),
        Err(err) => return db_failure(err),
    };
    if probe_mode != mode || probe_port as i64 != port {
        return error(StatusCode::CONFLICT, "检测配置已变更，请读取最新任务后重新检测");
    }
    // Parse into a typed shape once. Keep the submitted IP text when it
    // validates, so a large batch avoids one extra canonical-string allocation
    // and JSON object per row. Delay normalization stays in the set-based SQL.
    let mut clean = Vec::with_capacity(input.results.len());
    for item in &input.results {
        let Some(ip) = item.ip.as_deref() else {
            return error(StatusCode::UNPROCESSABLE_ENTITY, "检测结果包含无效 IP 或延迟");
        };
        if ip.parse::<std::net::IpAddr>().is_err() {
            return error(StatusCode::UNPROCESSABLE_ENTITY, "检测结果包含无效 IP 或延迟");
        }
        let Some(delay_ms) = item.delay_ms else {
            return error(StatusCode::UNPROCESSABLE_ENTITY, "检测结果包含无效 IP 或延迟");
        };
        let Some(success) = item.success else {
            return error(StatusCode::UNPROCESSABLE_ENTITY, "检测结果包含无效 IP 或延迟");
        };
        let checked_at = item
            .checked_at
            .as_deref()
            .filter(|value| !value.is_empty())
            .map(str::to_owned)
            .unwrap_or_else(|| chrono::Utc::now().to_rfc3339());
        clean.push(AgentResultRow {
            ip,
            delay_ms,
            success,
            checked_at,
        });
    }
    let body_text = match serde_json::to_string(&clean) {
        Ok(value) => value,
        Err(_) => return error(StatusCode::BAD_REQUEST, "上报数据格式错误"),
    };
    let sql = format!(
        "WITH target_ips AS MATERIALIZED (SELECT ip FROM ({PROBE_TARGET_POOL}) pool), received AS (SELECT r.ip::inet AS ip,CASE WHEN r.success AND r.delay_ms=0 THEN 0.01 ELSE r.delay_ms END AS delay_ms,r.success AS success,CASE WHEN r.checked_at IS NULL OR r.checked_at='' THEN now() ELSE r.checked_at::timestamptz END AS checked_at FROM jsonb_to_recordset($2::text::jsonb) AS r(ip text,delay_ms double precision,success boolean,checked_at text)), inserted AS (INSERT INTO latency_probe_results(probe_id,ip,delay_ms,success,checked_at,mode,tcp_port) SELECT $1::text::uuid,received.ip,received.delay_ms,received.success,received.checked_at,$3,$4 FROM received JOIN target_ips pool ON pool.ip=received.ip AND NOT EXISTS(SELECT 1 FROM ip_blacklist b WHERE b.ip=received.ip) RETURNING 1), seen AS (UPDATE latency_probes SET last_seen=now() WHERE id=$1::text::uuid RETURNING id) SELECT count(*) FROM inserted"
    );
    match client
        .query_one(&sql, &[&id, &body_text, &mode, &(port as i32)])
        .await
    {
        Ok(row) => Json(json!({"saved": row.get::<_, i64>(0)})).into_response(),
        Err(err) => db_failure(err),
    }
}

async fn agent_heartbeat(State(state): State<AppState>, headers: HeaderMap) -> Response {
    let (client, id) = match authorize_probe(&state, &headers).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    match client
        .execute(
            "UPDATE latency_probes SET last_seen=now(),heartbeat_at=now() WHERE id=$1::text::uuid",
            &[&id],
        )
        .await
    {
        Ok(_) => Json(json!({"ok": true})).into_response(),
        Err(err) => db_failure(err),
    }
}


// ---------------------------------------------------------------------------
// Certificate issues and available-IP aggregation
// ---------------------------------------------------------------------------

async fn certificate_issues_data(
    client: &tokio_postgres::Client,
    uri: &str,
) -> Result<(Value, i64, i64, i64), Response> {
    let size = 50i64;
    let requested = page_number(uri)?;
    let total = count(
        client,
        "WITH active AS MATERIALIZED (SELECT DISTINCT md5(share_uri) AS config_hash FROM nodes WHERE enabled AND share_uri<>'') \
         SELECT count(*) FROM node_ip_pool p JOIN active n ON n.config_hash=p.config_hash WHERE p.certificate->>'status'='invalid'",
        &[],
    )
    .await?;
    let page = normalized_page(requested, total, size);
    let sql = "WITH active AS MATERIALIZED (SELECT DISTINCT ON (md5(share_uri)) md5(share_uri) AS config_hash,id,name FROM nodes WHERE enabled AND share_uri<>'' ORDER BY md5(share_uri),id), \
         selected AS MATERIALIZED (SELECT p.checked_at,p.ip,p.protocol,p.config_hash FROM node_ip_pool p WHERE p.certificate->>'status'='invalid' AND p.config_hash=ANY(ARRAY(SELECT config_hash FROM active)) ORDER BY p.checked_at DESC,p.ip,p.protocol LIMIT $1 OFFSET $2) \
         SELECT COALESCE(json_agg(t),'[]'::json)::text FROM (SELECT host(p.ip) AS ip,p.protocol,n.id::text AS node_id,n.name AS node_name,p.certificate,p.checked_at FROM selected s JOIN node_ip_pool p ON p.protocol=s.protocol AND p.ip=s.ip JOIN active n ON n.config_hash=s.config_hash ORDER BY s.checked_at DESC,s.ip,s.protocol) t";
    let items = json_rows(client, sql, &[&size, &page_offset(page, size)]).await?;
    Ok((items, total, page, size))
}

async fn certificate_issues(
    State(state): State<AppState>,
    OriginalUri(uri): OriginalUri,
    headers: HeaderMap,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    match certificate_issues_data(&client, uri.query().unwrap_or("")).await {
        Ok((items, total, page, size)) => page_headers(Json(items), total, page, size),
        Err(response) => response,
    }
}

async fn available_ip_data(
    client: &tokio_postgres::Client,
    uri: &str,
) -> Result<(Value, i64, i64, i64, Value), Response> {
    let search = query_value(uri, "search");
    let protocol = query_value(uri, "protocol");
    let region = query_value(uri, "region");
    let provider = query_value(uri, "provider");
    let size = 20i64;
    let requested = page_number(uri)?;
    let base = "WITH candidates AS MATERIALIZED (\
         SELECT s.ip,COALESCE(info.asn,'') AS asn,COALESCE(info.provider,'') AS provider,COALESCE(o.country,l.country,'') AS country \
         FROM (SELECT DISTINCT ip FROM shared_node_ip_pool WHERE available) s \
         LEFT JOIN LATERAL (SELECT w.asn,w.provider FROM networks w WHERE w.enabled AND s.ip <<= w.cidr AND (w.asn<>'' OR w.provider<>'') ORDER BY masklen(w.cidr) DESC,w.updated_at DESC,w.id LIMIT 1) info ON true \
         LEFT JOIN dns_ip_locations l ON l.ip=s.ip AND l.policy_version=3 \
         LEFT JOIN ip_location_overrides o ON o.ip=s.ip \
         WHERE NOT EXISTS(SELECT 1 FROM ip_blacklist b WHERE b.ip=s.ip)), filtered AS (\
         SELECT c.* FROM candidates c WHERE ($1='' OR position(lower($1) in lower(host(c.ip)||' '||c.asn||' '||c.provider||' 协议共用 IP 池'))>0) \
         AND ($2='' OR $2='all' OR EXISTS(SELECT 1 FROM shared_node_ip_pool p WHERE p.ip=c.ip AND p.available AND p.protocol=$2)) \
         AND ($3='' OR $3='all' OR ($3='unknown' AND c.country='') OR c.country=$3) \
         AND ($4='' OR $4='all' OR ($4='missing' AND c.provider='') OR c.provider=substring($4 from 10)))";
    let total = count(client, &format!("{base} SELECT count(*) FROM filtered"), &[&search, &protocol, &region, &provider]).await?;
    let page = normalized_page(requested, total, size);
    let data_sql = format!(
        "{base} SELECT COALESCE(json_agg(t),'[]'::json)::text FROM (SELECT host(c.ip) AS ip,c.asn,c.provider,\
         (SELECT COALESCE(json_agg(json_build_object('protocol',p.protocol,'delay_ms',p.delay_ms,'node_id','','node_name','协议共用 IP 池','checked_at',p.checked_at) ORDER BY p.protocol),'[]'::json) FROM shared_node_ip_pool p WHERE p.ip=c.ip AND p.available) AS checks,\
         '[]'::json AS probe_delays,\
         (SELECT json_build_object('duration_ms',st.duration_ms,'speed_mib_s',st.speed_mib_s,'checked_at',st.checked_at,'success',st.success,'last_success_at',st.last_success_at) FROM available_ip_speed_tests st WHERE st.ip=c.ip) AS speed_test,\
         COALESCE((SELECT st.success FROM available_ip_speed_tests st WHERE st.ip=c.ip),false) AS speed_passed,\
         'unknown'::text AS node_dns_status,'unknown'::text AS url_dns_status \
         FROM filtered c ORDER BY c.ip LIMIT $5 OFFSET $6) t"
    );
    let items = json_rows(client, &data_sql, &[&search, &protocol, &region, &provider, &size, &page_offset(page, size)]).await?;
    let meta_sql = format!(
        "{base} SELECT json_build_object('allTotal',(SELECT count(*) FROM candidates),\
         'countries',(SELECT COALESCE(json_agg(country ORDER BY country),'[]') FROM (SELECT DISTINCT country FROM candidates WHERE country<>'') c),\
         'providers',(SELECT COALESCE(json_agg(provider ORDER BY provider),'[]') FROM (SELECT DISTINCT provider FROM candidates WHERE provider<>'') c))::text"
    );
    let meta_text: String = client
        .query_one(&meta_sql, &[&search, &protocol, &region, &provider])
        .await
        .map(|row| row.get(0))
        .map_err(db_failure)?;
    let meta = serde_json::from_str(&meta_text)
        .map_err(|_| error(StatusCode::SERVICE_UNAVAILABLE, "数据库暂时不可用，请稍后重试"))?;
    Ok((items, total, page, size, meta))
}

async fn available_ips(
    State(state): State<AppState>,
    OriginalUri(uri): OriginalUri,
    headers: HeaderMap,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    match available_ip_data(&client, uri.query().unwrap_or("")).await {
        Ok((items, total, page, size, meta)) => {
            let mut response = page_headers(Json(items), total, page, size);
            if let Ok(value) = HeaderValue::from_str(&BASE64.encode(meta.to_string())) {
                response.headers_mut().insert("x-list-meta", value);
            }
            response
        }
        Err(response) => response,
    }
}

async fn ip_location(
    State(state): State<AppState>,
    OriginalUri(uri): OriginalUri,
    headers: HeaderMap,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    let raw = query_value(uri.query().unwrap_or(""), "ip");
    let Ok(ip) = raw.parse::<std::net::IpAddr>() else {
        return error(StatusCode::BAD_REQUEST, "请输入公网 IP 地址");
    };
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let row = client
        .query_opt(
            "SELECT COALESCE(o.country,l.country,''),COALESCE(o.region,''),COALESCE(o.city,''),COALESCE(l.location,'{}'::jsonb)::text FROM (SELECT $1::text::inet AS ip) q LEFT JOIN ip_location_overrides o ON o.ip=q.ip LEFT JOIN dns_ip_locations l ON l.ip=q.ip",
            &[&ip.to_string()],
        )
        .await;
    match row {
        Ok(Some(row)) => {
            let (country, region, city, location): (String, String, String, String) =
                (row.get(0), row.get(1), row.get(2), row.get(3));
            if !country.is_empty() {
                if let Ok(value) = serde_json::from_str::<Value>(&location) {
                    if value.get("status").is_some() {
                        return Json(value).into_response();
                    }
                }
                return Json(json!({"status": "consensus", "country": country, "region": region, "city": city}))
                    .into_response();
            }
            Json(json!({"status": "unavailable", "country": "", "region": "", "city": ""})).into_response()
        }
        Ok(None) => Json(json!({"status": "unavailable", "country": "", "region": "", "city": ""})).into_response(),
        Err(err) => db_failure(err),
    }
}

async fn save_ip_location(
    State(state): State<AppState>,
    OriginalUri(uri): OriginalUri,
    headers: HeaderMap,
    body: Bytes,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    let raw = query_value(uri.query().unwrap_or(""), "ip");
    let Ok(ip) = raw.parse::<std::net::IpAddr>() else {
        return error(StatusCode::BAD_REQUEST, "请输入公网 IP 地址");
    };
    let input: Value = match serde_json::from_slice(&body) {
        Ok(value) => value,
        Err(_) => return error(StatusCode::BAD_REQUEST, "请求格式错误，请检查字段类型和内容"),
    };
    let country = input.get("country").and_then(Value::as_str).unwrap_or("").trim().to_uppercase();
    let region = input.get("region").and_then(Value::as_str).unwrap_or("").trim();
    let city = input.get("city").and_then(Value::as_str).unwrap_or("").trim();
    if country.len() != 2 || region.chars().count() > 200 || city.chars().count() > 200 {
        return error(StatusCode::UNPROCESSABLE_ENTITY, "请选择有效的国家或地区，省市信息最多 200 字节");
    }
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    match client
        .execute(
            "INSERT INTO ip_location_overrides(ip,country,region,city) VALUES($1::text::inet,$2,$3,$4) ON CONFLICT(ip) DO UPDATE SET country=excluded.country,region=excluded.region,city=excluded.city,updated_at=now()",
            &[&ip.to_string(), &country, &region, &city],
        )
        .await
    {
        Ok(_) => Json(json!({"status":"manual","source":"人工纠正","country":country,"region":region,"city":city})).into_response(),
        Err(err) => db_failure(err),
    }
}

async fn delete_ip_location(
    State(state): State<AppState>,
    OriginalUri(uri): OriginalUri,
    headers: HeaderMap,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    let raw = query_value(uri.query().unwrap_or(""), "ip");
    let Ok(ip) = raw.parse::<std::net::IpAddr>() else {
        return error(StatusCode::BAD_REQUEST, "请输入公网 IP 地址");
    };
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    match client.execute("DELETE FROM ip_location_overrides WHERE ip=$1::text::inet", &[&ip.to_string()]).await {
        Ok(_) => Json(json!({"status": "unavailable", "country": "", "region": "", "city": ""})).into_response(),
        Err(err) => db_failure(err),
    }
}

async fn ip_locations(
    State(state): State<AppState>,
    OriginalUri(uri): OriginalUri,
    headers: HeaderMap,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    let ips: Vec<String> = uri
        .query()
        .unwrap_or("")
        .split('&')
        .filter_map(|part| part.split_once('='))
        .filter(|(key, _)| *key == "ip")
        .filter_map(|(_, value)| url_decode(value).parse::<std::net::IpAddr>().ok().map(|ip| ip.to_string()))
        .collect();
    if ips.is_empty() {
        return Json(json!([])).into_response();
    }
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let sql = "SELECT COALESCE(json_agg(t),'[]'::json)::text FROM (SELECT host(q.ip::inet) AS ip,CASE WHEN o.ip IS NULL AND COALESCE(l.country,'')='' THEN NULL ELSE json_build_object('status',CASE WHEN o.ip IS NOT NULL THEN 'manual' ELSE COALESCE(l.location->>'status','consensus') END,'country',COALESCE(o.country,l.country,''),'region',COALESCE(o.region,l.location->>'region',''),'city',COALESCE(o.city,l.location->>'city','')) END AS location FROM unnest($1::text[]) AS q(ip) LEFT JOIN ip_location_overrides o ON o.ip=q.ip::inet LEFT JOIN dns_ip_locations l ON l.ip=q.ip::inet ORDER BY host(q.ip::inet)) t";
    match json_rows(&client, sql, &[&ips]).await {
        Ok(items) => Json(items).into_response(),
        Err(response) => response,
    }
}

// ---------------------------------------------------------------------------
// DNS policy API and plan-queue
// ---------------------------------------------------------------------------

fn dns_credentials_ready() -> bool {
    std::env::var("HUAWEICLOUD_SDK_AK").map(|v| !v.is_empty()).unwrap_or(false)
        && std::env::var("HUAWEICLOUD_SDK_SK").map(|v| !v.is_empty()).unwrap_or(false)
}

async fn get_dns(
    State(state): State<AppState>,
    OriginalUri(uri): OriginalUri,
    headers: HeaderMap,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let row = client
        .query_one(
            "SELECT config::text,running,requested,next_run_at::text,report::text FROM dns_settings WHERE id",
            &[],
        )
        .await;
    let (config, running, requested, next_run, report): (String, bool, String, String, String) =
        match row {
            Ok(row) => (row.get(0), row.get(1), row.get(2), row.get(3), row.get(4)),
            Err(err) => return db_failure(err),
        };
    let config: Value = serde_json::from_str(&config).unwrap_or_else(|_| json!({}));
    let mut report: Value = serde_json::from_str(&report).unwrap_or_else(|_| json!({}));
    if query_value(uri.query().unwrap_or(""), "summary") == "1" {
        if let Value::Object(map) = &mut report {
            let count = map
                .get("candidates")
                .and_then(Value::as_array)
                .map(|items| items.len())
                .unwrap_or(0);
            map.remove("records");
            map.remove("candidates");
            map.insert("candidate_count".into(), json!(count));
        }
    }
    let history = if query_value(uri.query().unwrap_or(""), "summary") == "1" {
        json!([])
    } else {
        let sql = "SELECT COALESCE(json_agg(h),'[]'::json)::text FROM (SELECT id,started_at,report FROM dns_sync_runs ORDER BY id DESC LIMIT 10) h";
        match json_rows(&client, sql, &[]).await {
            Ok(value) => value,
            Err(response) => return response,
        }
    };
    Json(json!({
        "config": config,
        "credentials_ready": dns_credentials_ready(),
        "running": running,
        "requested": requested,
        "next_run_at": next_run,
        "report": report,
        "history": history,
    }))
    .into_response()
}

async fn save_dns(State(state): State<AppState>, headers: HeaderMap, body: Bytes) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    let config: Value = match serde_json::from_slice(&body) {
        Ok(value) => value,
        Err(_) => return error(StatusCode::BAD_REQUEST, "请求格式错误，请检查字段类型和内容"),
    };
    let interval = config
        .get("interval_seconds")
        .and_then(Value::as_i64)
        .filter(|value| (60..=86400).contains(value))
        .unwrap_or(300);
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let raw = match serde_json::to_string(&config) {
        Ok(value) => value,
        Err(_) => return error(StatusCode::BAD_REQUEST, "请求格式错误，请检查字段类型和内容"),
    };
    match client
        .execute(
            "UPDATE dns_settings SET config=$1::text::jsonb,requested='',next_run_at=now()+make_interval(secs=>$2) WHERE id",
            &[&raw, &interval],
        )
        .await
    {
        Ok(_) => Json(json!({"ok": true})).into_response(),
        Err(err) => db_failure(err),
    }
}

async fn request_dns(
    State(state): State<AppState>,
    axum::extract::Path(mode): axum::extract::Path<String>,
    headers: HeaderMap,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    if mode != "preview" && mode != "sync" {
        return error(StatusCode::NOT_FOUND, "未知 DNS 操作");
    }
    if !dns_credentials_ready() {
        return error(StatusCode::UNPROCESSABLE_ENTITY, "请先在服务器配置华为云 AK/SK 并重启服务");
    }
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    match client
        .execute(
            "UPDATE dns_settings SET requested=$1 WHERE id AND requested=''",
            &[&mode],
        )
        .await
    {
        Ok(0) => error(StatusCode::CONFLICT, "DNS 任务已排队，请等待完成"),
        Ok(_) => (StatusCode::ACCEPTED, Json(json!({"ok": true}))).into_response(),
        Err(err) => db_failure(err),
    }
}

async fn dns_items(
    State(state): State<AppState>,
    axum::extract::Path(kind): axum::extract::Path<String>,
    OriginalUri(uri): OriginalUri,
    headers: HeaderMap,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    if !matches!(kind.as_str(), "records" | "candidates" | "history") {
        return error(StatusCode::NOT_FOUND, "列表不存在");
    }
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    dns_items_response(&client, &kind, uri.query().unwrap_or("")).await
}

async fn dns_items_data(
    client: &tokio_postgres::Client,
    kind: &str,
    uri: &str,
) -> Result<(Value, i64, i64, i64), Response> {
    let page = page_number(uri)?;
    let (count_sql, data_sql): (String, String) = if kind == "history" {
        (
            "SELECT count(*) FROM dns_sync_runs".into(),
            "SELECT COALESCE(json_agg(h),'[]'::json)::text FROM (SELECT id,started_at,report-'records'-'candidates' AS report FROM dns_sync_runs ORDER BY id DESC LIMIT $1 OFFSET $2) h".into(),
        )
    } else {
        let source = format!(
            " FROM dns_settings CROSS JOIN LATERAL jsonb_array_elements(COALESCE(report->'{kind}','[]'::jsonb)) WITH ORDINALITY AS e(value,ord) WHERE id"
        );
        (
            format!("SELECT count(*){source}"),
            format!("SELECT COALESCE(jsonb_agg(value ORDER BY ord),'[]'::jsonb)::text FROM (SELECT value,ord{source} ORDER BY ord LIMIT $1 OFFSET $2) selected"),
        )
    };
    let total = count(client, &count_sql, &[]).await?;
    let page = normalized_page(page, total, 50);
    let items = json_rows(client, &data_sql, &[&50i64, &page_offset(page, 50)]).await?;
    Ok((items, total, page, 50))
}

async fn dns_items_response(client: &tokio_postgres::Client, kind: &str, uri: &str) -> Response {
    match dns_items_data(client, kind, uri).await {
        Ok((items, total, page, size)) => page_headers(Json(items), total, page, size),
        Err(response) => response,
    }
}

// ---------------------------------------------------------------------------
// Scan queue and durable round history
// ---------------------------------------------------------------------------

use redis::AsyncCommands;

const SCAN_TTL_SECONDS: u64 = 24 * 60 * 60;

async fn redis_connection(state: &AppState) -> Result<redis::aio::MultiplexedConnection, Response> {
    let Some(redis) = state.redis.clone() else {
        return Err(error(StatusCode::SERVICE_UNAVAILABLE, "任务服务暂时不可用"));
    };
    redis
        .get_multiplexed_async_connection()
        .await
        .map_err(|_| error(StatusCode::SERVICE_UNAVAILABLE, "任务服务暂时不可用"))
}

fn share_is_grpc(raw: &str) -> bool {
    let query = raw.split_once('?').map(|(_, query)| query).unwrap_or("");
    query
        .split('&')
        .any(|pair| pair == "type=grpc" || pair == "type%3Dgrpc")
}

async fn start_scan(State(state): State<AppState>, headers: HeaderMap, body: Bytes) -> Response {
    let user = match authenticate(&state, &headers).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let input: Value = match serde_json::from_slice(&body) {
        Ok(value) => value,
        Err(_) => return error(StatusCode::BAD_REQUEST, "请求格式错误，请检查字段类型和内容"),
    };
    let node_id = input.get("node_id").and_then(Value::as_str).unwrap_or("");
    let networks = input
        .get("network_ids")
        .and_then(Value::as_array)
        .map(|items| items.iter().filter_map(Value::as_str).map(str::to_string).collect::<Vec<_>>())
        .unwrap_or_default();
    if !valid_uuid(node_id) || networks.is_empty() || networks.len() > 64 || networks.iter().any(|id| !valid_uuid(id)) {
        return error(StatusCode::UNPROCESSABLE_ENTITY, "请选择节点和 1–64 个网段");
    }
    let incremental = input.get("incremental").and_then(Value::as_bool).unwrap_or(false);
    let interval = input.get("interval_minutes").and_then(Value::as_i64).unwrap_or(0);
    let full_every = input.get("full_every").and_then(Value::as_i64).unwrap_or(0);
    if incremental && interval != 0 {
        return error(StatusCode::UNPROCESSABLE_ENTITY, "增量检测请使用单次检测；自动巡检请使用全量检测");
    }
    if interval != 0 && (!(1..=1440).contains(&interval) || !(1..=288).contains(&full_every)) {
        return error(StatusCode::UNPROCESSABLE_ENTITY, "巡检间隔为 1–1440 分钟，全量扫描频率为 1–288 轮");
    }
    let timeout = input.get("timeout_ms").and_then(Value::as_i64).unwrap_or(5000);
    let concurrency = input.get("concurrency").and_then(Value::as_i64).unwrap_or(64);
    if !(1000..=15000).contains(&timeout) || !(1..=64).contains(&concurrency) {
        return error(StatusCode::UNPROCESSABLE_ENTITY, "超时范围 1000–15000 毫秒，并发范围 1–64");
    }
    let url = input.get("url").and_then(Value::as_str).unwrap_or("").trim();
    let Ok(parsed) = url::Url::parse(url) else {
        return error(StatusCode::UNPROCESSABLE_ENTITY, "检测网址需为有效的 HTTP/HTTPS 地址，不含账号和片段");
    };
    if (parsed.scheme() != "http" && parsed.scheme() != "https")
        || parsed.host_str().is_none()
        || parsed.username() != ""
        || parsed.password().is_some()
        || parsed.fragment().is_some()
        || url.len() > 2048
    {
        return error(StatusCode::UNPROCESSABLE_ENTITY, "检测网址需为有效的 HTTP/HTTPS 地址，不含账号和片段");
    }
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let node = client
        .query_opt(
            "SELECT share_uri,name FROM nodes WHERE id=$1::text::uuid AND enabled AND share_uri<>''",
            &[&node_id],
        )
        .await;
    let (share, node_name): (String, String) = match node {
        Ok(Some(row)) => (row.get(0), row.get(1)),
        Ok(None) => return error(StatusCode::UNPROCESSABLE_ENTITY, "节点不存在或已停用"),
        Err(err) => return db_failure(err),
    };
    let (scheme, _) = share.split_once("://").unwrap_or(("", ""));
    let protocol = if scheme == "vless" && share_is_grpc(&share) {
        "vless/grpc".to_string()
    } else if scheme == "trojan" && share_is_grpc(&share) {
        "trojan/grpc".to_string()
    } else {
        String::new()
    };
    if protocol.is_empty() {
        return error(StatusCode::UNPROCESSABLE_ENTITY, "连通性检测仅使用 gRPC 节点，结果供所有协议共用");
    }
    let cidrs = client
        .query(
            "SELECT cidr::text FROM networks WHERE id=ANY($1::text[]::uuid[]) AND enabled AND NOT EXISTS(SELECT 1 FROM protocol_networks p WHERE p.network_id=networks.id AND p.cidr=networks.cidr AND p.protocol=$2) ORDER BY cidr",
            &[&networks, &protocol],
        )
        .await;
    let cidrs: Vec<String> = match cidrs {
        Ok(rows) => rows.iter().map(|row| row.get(0)).collect(),
        Err(err) => return db_failure(err),
    };
    if cidrs.is_empty() {
        return error(StatusCode::UNPROCESSABLE_ENTITY, "所选网段在当前协议池中均已禁用或删除，请恢复网段后重试");
    }
    let mut connection = match redis_connection(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let mut id_bytes = [0u8; 16];
    rand::thread_rng().fill_bytes(&mut id_bytes);
    let id = id_bytes.iter().map(|byte| format!("{byte:02x}")).collect::<String>();
    let lock_key = format!("ipfilter:scan:active:{protocol}");
    let locked: Option<String> = match redis::cmd("SET")
        .arg(&lock_key)
        .arg(&id)
        .arg("NX")
        .arg("EX")
        .arg(45)
        .query_async(&mut connection)
        .await
    {
        Ok(value) => value,
        Err(_) => return error(StatusCode::SERVICE_UNAVAILABLE, "任务服务暂时不可用"),
    };
    if locked.is_none() {
        return error(StatusCode::CONFLICT, "已有测试正在运行，请等待完成或停止当前任务");
    }
    let started_at = chrono::Utc::now();
    let job = json!({
        "id": id,
        "input": {
            "incremental": incremental,
            "node_id": node_id,
            "network_ids": networks,
            "url": url,
            "timeout_ms": timeout,
            "concurrency": concurrency,
            "interval_minutes": interval,
            "full_every": full_every,
        },
        "protocol": protocol,
        "round": 1,
        "scope": if incremental { "incremental" } else { "full" },
        "started_at": started_at,
        "user_id": user.id,
        "node_name": node_name,
        "url": url,
        "cidrs": cidrs,
        "status": "running",
        "total": 0,
        "completed": 0,
        "success": 0,
        "results": [],
        "updated_at": started_at,
    });
    let data = serde_json::to_string(&job).unwrap_or_default();
    let job_key = format!("ipfilter:scan:job:{id}");
    let latest_key = format!("ipfilter:scan:latest:{}", user.id);
    let latest_protocol_key = format!("{latest_key}:{protocol}");
    let stored: Result<(), redis::RedisError> = async {
        let _: () = connection.set_ex(&job_key, &data, SCAN_TTL_SECONDS).await?;
        let _: () = connection.set_ex(&latest_key, &id, SCAN_TTL_SECONDS).await?;
        let _: () = connection.set_ex(&latest_protocol_key, &id, SCAN_TTL_SECONDS).await?;
        Ok(())
    }
    .await;
    if stored.is_err() {
        let _: Result<(), _> = redis::cmd("DEL").arg(&lock_key).query_async(&mut connection).await;
        return error(StatusCode::SERVICE_UNAVAILABLE, "任务保存失败，请重试");
    }
    let queued = client
        .execute("INSERT INTO engine_jobs(job_id,kind) VALUES($1,'node_scan')", &[&id])
        .await;
    if let Err(err) = queued {
        let _: Result<(), _> = redis::cmd("DEL").arg(&job_key).arg(&lock_key).query_async(&mut connection).await;
        return db_failure(err);
    }
    if interval > 0 {
        let _ = client
            .execute(
                "INSERT INTO node_scan_schedules(protocol,job_id,job) VALUES($1,$2,$3::text::jsonb) ON CONFLICT(protocol) DO UPDATE SET job_id=excluded.job_id,job=excluded.job,enabled=true,updated_at=now()",
                &[&protocol, &id, &data],
            )
            .await;
    } else {
        let _ = client
            .execute(
                "UPDATE node_scan_schedules SET enabled=false,updated_at=now() WHERE protocol=$1",
                &[&protocol],
            )
            .await;
    }
    (StatusCode::ACCEPTED, Json(job)).into_response()
}

async fn scan_snapshot(state: &AppState, id: &str) -> Result<Option<Value>, Response> {
    let mut connection = redis_connection(state).await?;
    let data: Option<String> = connection
        .get(format!("ipfilter:scan:job:{id}"))
        .await
        .map_err(|_| error(StatusCode::SERVICE_UNAVAILABLE, "任务服务暂时不可用"))?;
    match data {
        Some(value) => serde_json::from_str(&value)
            .map(Some)
            .map_err(|_| error(StatusCode::SERVICE_UNAVAILABLE, "任务快照无法解析")),
        None => Ok(None),
    }
}

async fn latest_scan(
    State(state): State<AppState>,
    headers: HeaderMap,
) -> Response {
    let user = match authenticate(&state, &headers).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let mut connection = match redis_connection(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let id: Option<String> = connection
        .get(format!("ipfilter:scan:latest:{}", user.id))
        .await
        .ok()
        .flatten();
    let Some(id) = id else {
        return Json(Value::Null).into_response();
    };
    match scan_snapshot(&state, &id).await {
        Ok(Some(job)) if matches!(job.get("status").and_then(Value::as_str), Some("running") | Some("waiting")) => {
            Json(job).into_response()
        }
        Ok(_) => Json(Value::Null).into_response(),
        Err(response) => response,
    }
}

async fn get_scan(
    State(state): State<AppState>,
    axum::extract::Path(id): axum::extract::Path<String>,
    headers: HeaderMap,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    if id.len() != 32 || !id.chars().all(|c| c.is_ascii_hexdigit()) {
        return error(StatusCode::NOT_FOUND, "任务不存在或已过期");
    }
    match scan_snapshot(&state, &id).await {
        Ok(Some(job)) => Json(job).into_response(),
        Ok(None) => error(StatusCode::NOT_FOUND, "任务不存在或已过期"),
        Err(response) => response,
    }
}

async fn cancel_scan(
    State(state): State<AppState>,
    axum::extract::Path(id): axum::extract::Path<String>,
    headers: HeaderMap,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    if id.len() != 32 || !id.chars().all(|c| c.is_ascii_hexdigit()) {
        return error(StatusCode::NOT_FOUND, "任务不存在或已过期");
    }
    let mut connection = match redis_connection(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let cancel_key = format!("ipfilter:scan:job:{id}:cancel");
    if connection.set_ex::<_, _, ()>(&cancel_key, "1", SCAN_TTL_SECONDS).await.is_err() {
        return error(StatusCode::SERVICE_UNAVAILABLE, "任务服务暂时不可用");
    }
    (StatusCode::ACCEPTED, Json(json!({"ok": true}))).into_response()
}

pub async fn scan_records_list(state: &AppState, headers: &HeaderMap, uri: &str) -> Response {
    let user = match authenticate(state, headers).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let client = match database(state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let search = query_value(uri, "search");
    let page = match page_number(uri) {
        Ok(value) => value,
        Err(response) => return response,
    };
    let count_sql = "SELECT count(*) FROM node_scan_rounds WHERE ($1='' OR position(lower($1) in lower(node_name||' '||url||' '||protocol))>0)";
    let total = match count(&client, count_sql, &[&search]).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let page = normalized_page(page, total, 20);
    let sql = "SELECT COALESCE(json_agg(t),'[]'::json)::text FROM (SELECT id::text,job_id,round,protocol,node_name,scope,url,cidrs,input,started_at,finished_at,total,completed,success,status,error,results,updated_at,false AS live FROM node_scan_rounds WHERE ($1='' OR position(lower($1) in lower(node_name||' '||url||' '||protocol))>0) ORDER BY started_at DESC,id DESC LIMIT $2 OFFSET $3) t";
    let items = match json_rows(&client, sql, &[&search, &20i64, &page_offset(page, 20)]).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let active = {
        let mut connection = redis_connection(state).await.ok();
        if let Some(connection) = connection.as_mut() {
            let id: Option<String> = connection
                .get(format!("ipfilter:scan:latest:{}", user.id))
                .await
                .ok()
                .flatten();
            if let Some(id) = id {
                match scan_snapshot(state, &id).await {
                    Ok(Some(job)) if matches!(job.get("status").and_then(Value::as_str), Some("running") | Some("waiting")) => Some(json!({
                        "job_id": job.get("id").cloned().unwrap_or(Value::Null),
                        "protocol": job.get("protocol").cloned().unwrap_or(Value::Null),
                        "node_name": job.get("node_name").cloned().unwrap_or(Value::Null),
                        "round": job.get("round").cloned().unwrap_or(Value::Null),
                        "status": job.get("status").cloned().unwrap_or(Value::Null),
                        "total": job.get("total").cloned().unwrap_or(Value::Null),
                        "completed": job.get("completed").cloned().unwrap_or(Value::Null),
                        "success": job.get("success").cloned().unwrap_or(Value::Null),
                        "interval_minutes": job.get("input").and_then(|v| v.get("interval_minutes")).cloned().unwrap_or(Value::Null),
                        "next_run_at": job.get("next_run_at").cloned().unwrap_or(Value::Null),
                        "cidrs": job.get("cidrs").cloned().unwrap_or(Value::Null),
                    })),
                    _ => None,
                }
            } else {
                None
            }
        } else {
            None
        }
    };
    let networks_sql = "SELECT COALESCE(json_agg(t),'[]'::json)::text FROM (SELECT cidr::text, name, provider, asn FROM networks WHERE enabled ORDER BY cidr) t";
    let networks = match json_rows(&client, networks_sql, &[]).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    Json(json!({
        "items": items,
        "total": total,
        "page": page,
        "pageSize": 20,
        "active": active,
        "networks": networks,
        "network_progress": Value::Null,
    }))
    .into_response()
}

async fn scan_record_detail(
    State(state): State<AppState>,
    axum::extract::Path(id): axum::extract::Path<i64>,
    headers: HeaderMap,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let sql = "SELECT row_to_json(t)::text FROM (SELECT id::text,job_id,round,protocol,node_name,scope,url,cidrs,input,started_at,finished_at,total,completed,success,status,error,results,updated_at,false AS live FROM node_scan_rounds WHERE id=$1) t";
    let text: String = match client.query_one(sql, &[&id]).await {
        Ok(row) => row.get(0),
        Err(err) => return db_failure(err),
    };
    match serde_json::from_str::<Value>(&text) {
        Ok(Value::Null) => error(StatusCode::NOT_FOUND, "检测记录不存在或已被清理"),
        Ok(value) => Json(value).into_response(),
        Err(_) => error(StatusCode::SERVICE_UNAVAILABLE, "数据库暂时不可用，请稍后重试"),
    }
}

// ---------------------------------------------------------------------------
// Cloudflare target scan queue
// ---------------------------------------------------------------------------

const CF_LOCK: &str = "ipfilter:cf:active";
const CF_LATEST: &str = "ipfilter:cf:latest";

async fn start_cf_target_scan(
    State(state): State<AppState>,
    axum::extract::Path(id): axum::extract::Path<String>,
    headers: HeaderMap,
    body: Bytes,
) -> Response {
    let mut value: Value = match serde_json::from_slice(&body) {
        Ok(value) => value,
        Err(_) => return error(StatusCode::BAD_REQUEST, "请求格式错误，请检查字段类型和内容"),
    };
    if let Value::Object(map) = &mut value {
        map.insert("target_id".into(), json!(id));
    }
    let body = Bytes::from(serde_json::to_vec(&value).unwrap_or_default());
    start_cf_scan(State(state), headers, body).await
}

async fn start_cf_scan(State(state): State<AppState>, headers: HeaderMap, body: Bytes) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    let input: Value = match serde_json::from_slice(&body) {
        Ok(value) => value,
        Err(_) => return error(StatusCode::BAD_REQUEST, "请求格式错误，请检查字段类型和内容"),
    };
    let target_id = input.get("target_id").and_then(Value::as_str).unwrap_or("");
    let networks = input
        .get("network_ids")
        .and_then(Value::as_array)
        .map(|items| items.iter().filter_map(Value::as_str).map(str::to_string).collect::<Vec<_>>())
        .unwrap_or_default();
    if !valid_uuid(target_id) || networks.is_empty() || networks.len() > 64 || networks.iter().any(|id| !valid_uuid(id)) {
        return error(StatusCode::UNPROCESSABLE_ENTITY, "请选择 CF 目标和 1–64 个网段");
    }
    let incremental = input.get("incremental").and_then(Value::as_bool).unwrap_or(false);
    let timeout = input.get("timeout_ms").and_then(Value::as_i64).unwrap_or(5000);
    let concurrency = input.get("concurrency").and_then(Value::as_i64).unwrap_or(16);
    if !(1000..=15000).contains(&timeout) || !(1..=64).contains(&concurrency) {
        return error(StatusCode::UNPROCESSABLE_ENTITY, "超时范围 1000–15000 毫秒，并发范围 1–64");
    }
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let target = client
        .query_opt("SELECT url,name FROM cf_targets WHERE id=$1::text::uuid", &[&target_id])
        .await;
    let (url, name): (String, String) = match target {
        Ok(Some(row)) => (row.get(0), row.get(1)),
        Ok(None) => return error(StatusCode::NOT_FOUND, "目标地址不存在"),
        Err(err) => return db_failure(err),
    };
    let cidrs = client
        .query(
            "SELECT cidr::text FROM networks WHERE id=ANY($1::text[]::uuid[]) AND enabled ORDER BY cidr",
            &[&networks],
        )
        .await;
    let cidrs: Vec<String> = match cidrs {
        Ok(rows) => rows.iter().map(|row| row.get(0)).collect(),
        Err(err) => return db_failure(err),
    };
    if cidrs.len() != networks.len() {
        return error(StatusCode::UNPROCESSABLE_ENTITY, "网段已删除或停用，请刷新");
    }
    let mut connection = match redis_connection(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let mut id_bytes = [0u8; 16];
    rand::thread_rng().fill_bytes(&mut id_bytes);
    let id = id_bytes.iter().map(|byte| format!("{byte:02x}")).collect::<String>();
    let locked: Option<String> = match redis::cmd("SET")
        .arg(CF_LOCK)
        .arg(&id)
        .arg("NX")
        .arg("EX")
        .arg(45)
        .query_async(&mut connection)
        .await
    {
        Ok(value) => value,
        Err(_) => return error(StatusCode::SERVICE_UNAVAILABLE, "任务服务暂时不可用"),
    };
    if locked.is_none() {
        return error(StatusCode::CONFLICT, "已有 CF 检测正在运行，请等待完成或停止当前任务");
    }
    let started_at = chrono::Utc::now();
    let job = json!({
        "id": id,
        "target_id": target_id,
        "input": {
            "network_ids": networks,
            "url": url,
            "timeout_ms": timeout,
            "concurrency": concurrency,
            "incremental": incremental,
            "interval_minutes": 0,
            "full_every": 0,
        },
        "round": 1,
        "scope": if incremental { "incremental" } else { "full" },
        "started_at": started_at,
        "node_name": name,
        "url": url,
        "cidrs": cidrs,
        "status": "running",
        "total": 0,
        "completed": 0,
        "success": 0,
        "results": [],
        "updated_at": started_at,
    });
    let data = serde_json::to_string(&job).unwrap_or_default();
    let job_key = format!("ipfilter:scan:job:{id}");
    let stored: Result<(), redis::RedisError> = async {
        let _: () = connection.set_ex(&job_key, &data, SCAN_TTL_SECONDS).await?;
        let _: () = connection.set_ex(CF_LATEST, &id, SCAN_TTL_SECONDS).await?;
        Ok(())
    }
    .await;
    if stored.is_err() {
        let _: Result<(), _> = redis::cmd("DEL").arg(CF_LOCK).query_async(&mut connection).await;
        return error(StatusCode::SERVICE_UNAVAILABLE, "任务保存失败，请重试");
    }
    if let Err(err) = client
        .execute("INSERT INTO engine_jobs(job_id,kind) VALUES($1,'cf_scan')", &[&id])
        .await
    {
        let _: Result<(), _> = redis::cmd("DEL").arg(&job_key).arg(CF_LOCK).query_async(&mut connection).await;
        return db_failure(err);
    }
    (StatusCode::ACCEPTED, Json(job)).into_response()
}

async fn cf_scan_summary(
    State(state): State<AppState>,
    OriginalUri(uri): OriginalUri,
    headers: HeaderMap,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    let mut connection = match redis_connection(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let id: Option<String> = connection.get(CF_LATEST).await.ok().flatten();
    let Some(id) = id else {
        return Json(Value::Null).into_response();
    };
    match scan_snapshot(&state, &id).await {
        Ok(Some(mut job)) => {
            if query_value(uri.query().unwrap_or(""), "summary") == "1" {
                if let Value::Object(map) = &mut job {
                    map.remove("results");
                }
            }
            Json(job).into_response()
        }
        Ok(None) => Json(Value::Null).into_response(),
        Err(response) => response,
    }
}

async fn restore_protocol_network(
    State(state): State<AppState>,
    axum::extract::Path(id): axum::extract::Path<String>,
    OriginalUri(uri): OriginalUri,
    headers: HeaderMap,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    let protocol = query_value(uri.query().unwrap_or(""), "protocol");
    if !valid_uuid(&id) || protocol.is_empty() || protocol.len() > 32 {
        return error(StatusCode::UNPROCESSABLE_ENTITY, "网段或协议无效");
    }
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    match client
        .execute(
            "DELETE FROM protocol_networks WHERE network_id=$1::text::uuid AND protocol=$2",
            &[&id, &protocol],
        )
        .await
    {
        Ok(_) => Json(json!({"ok": true})).into_response(),
        Err(err) => db_failure(err),
    }
}

async fn available_ip_links(
    State(state): State<AppState>,
    OriginalUri(uri): OriginalUri,
    headers: HeaderMap,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    let raw = query_value(uri.query().unwrap_or(""), "ip");
    let Ok(ip) = raw.parse::<std::net::IpAddr>() else {
        return error(StatusCode::UNPROCESSABLE_ENTITY, "请输入有效的 IP 地址");
    };
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let available: bool = match client
        .query_one(
            "SELECT EXISTS(SELECT 1 FROM shared_node_ip_pool WHERE available AND ip=$1::text::inet) OR EXISTS(SELECT 1 FROM cf_ip_pool WHERE available AND ip=$1::text::inet)",
            &[&ip.to_string()],
        )
        .await
    {
        Ok(row) => row.get(0),
        Err(err) => return db_failure(err),
    };
    if !available {
        return error(StatusCode::CONFLICT, "该 IP 已无可用节点，请刷新列表或重新检测");
    }
    let nodes = client
        .query(
            "SELECT id::text,name,share_uri FROM nodes WHERE enabled AND share_uri<>'' ORDER BY name,id",
            &[],
        )
        .await;
    let rows = match nodes {
        Ok(rows) => rows,
        Err(err) => return db_failure(err),
    };
    let mut links = Vec::new();
    for row in rows {
        let node_id: String = row.get(0);
        let name: String = row.get(1);
        let share: String = row.get(2);
        let Ok(mut parsed) = url::Url::parse(&share) else {
            continue;
        };
        let protocol = {
            let transport = parsed
                .query_pairs()
                .find(|(key, _)| key == "type")
                .map(|(_, value)| value.to_string())
                .unwrap_or_else(|| "tcp".into());
            format!("{}/{}", parsed.scheme(), transport)
        };
        if parsed.set_host(Some(&ip.to_string())).is_err() {
            continue;
        }
        let port = parsed.port().unwrap_or(match parsed.scheme() {
            "https" => 443,
            _ => 80,
        });
        if parsed.set_port(Some(port)).is_err() {
            continue;
        }
        parsed.set_fragment(Some(&format!("{name} · {ip}")));
        links.push(json!({"node_id": node_id, "node_name": name, "protocol": protocol, "uri": parsed.to_string()}));
    }
    if links.is_empty() {
        return error(StatusCode::CONFLICT, "该 IP 已无可用节点，请刷新列表或重新检测");
    }
    Json(links).into_response()
}

async fn scan_records(
    State(state): State<AppState>,
    OriginalUri(uri): OriginalUri,
    headers: HeaderMap,
) -> Response {
    scan_records_list(&state, &headers, uri.query().unwrap_or("")).await
}

pub fn router() -> Router<AppState> {
    Router::new()
        .route("/nodes", get(node_list).post(save_node))
        .route("/nodes/import", post(import_nodes))
        .route("/nodes/:id", axum::routing::put(update_node).delete(delete_node))
        .route("/networks", get(network_list).post(save_network))
        .route("/networks/:id", axum::routing::put(update_network).delete(delete_network))
        .route("/protocol-networks/:id", axum::routing::delete(restore_protocol_network))
        .route("/blacklist", get(blacklist_list))
        .route("/cf-targets", get(cf_target_list).post(create_cf_target))
        .route("/cf-targets/:id", get(get_cf_target).delete(delete_cf_target))
        .route("/cf-targets/:id/scans", post(start_cf_target_scan))
        .route("/cf-pool", get(cf_pool_list))
        .route("/cf/scan", get(cf_scan_summary).post(start_cf_scan))
        .route("/cf/pool", get(cf_pool_list))
        .route("/cf-scan", get(cf_scan_summary).post(start_cf_scan))
        .route("/certificate-issues", get(certificate_issues))
        .route("/available-ips", get(available_ips))
        .route("/available-ips/links", get(available_ip_links))
        .route(
            "/available-ips/location",
            get(ip_location).put(save_ip_location).delete(delete_ip_location),
        )
        .route("/available-ips/locations", get(ip_locations))
        .route("/dns", get(get_dns).put(save_dns))
        .route("/dns/:kind", get(dns_items).post(request_dns))
        .route("/scans", post(start_scan))
        .route("/scans/latest", get(latest_scan))
        .route("/scans/:id", get(get_scan))
        .route("/scans/:id/cancel", post(cancel_scan))
        .route("/scan-records", get(scan_records))
        .route("/scan-records/:id", get(scan_record_detail))
        .route("/latency-probes", get(latency_probe_list).post(create_probe))
        .route(
            "/latency-probes/:id",
            axum::routing::put(update_probe).delete(delete_probe),
        )
        .route("/latency-probes/:id/token", post(rotate_probe_token))
        .route(
            "/latency-probes/:id/deployment",
            get(probe_deployment).put(probe_deployment),
        )
        .route("/latency-probes/:id/history", get(probe_history))
        .route("/probe-agent/targets", get(agent_targets))
        .route("/probe-agent/results", post(agent_results))
        .route("/probe-agent/heartbeat", post(agent_heartbeat))
}

async fn import_nodes(
    State(state): State<AppState>,
    headers: HeaderMap,
    body: Bytes,
) -> Response {
    if let Err(response) = authenticate(&state, &headers).await {
        return response;
    }
    let input: Value = match serde_json::from_slice(&body) {
        Ok(value) => value,
        Err(_) => return error(StatusCode::BAD_REQUEST, "请求格式错误，请检查字段类型和内容"),
    };
    let links = input.get("links").and_then(Value::as_str).unwrap_or("").trim();
    if links.is_empty() {
        return error(StatusCode::UNPROCESSABLE_ENTITY, "每次请输入 1–20 条链接，每行一条");
    }
    let lines: Vec<&str> = links.lines().map(str::trim).filter(|line| !line.is_empty()).collect();
    if lines.len() > 20 {
        return error(StatusCode::UNPROCESSABLE_ENTITY, "每次请输入 1–20 条链接，每行一条");
    }
    let client = match database(&state).await {
        Ok(value) => value,
        Err(response) => return response,
    };
    let mut count = 0i64;
    for (index, line) in lines.iter().enumerate() {
        let parsed = match parse_share(line) {
            Ok(value) => value,
            Err(message) => {
                return error(StatusCode::UNPROCESSABLE_ENTITY, &format!("第 {} 行：{message}", index + 1));
            }
        };
        match client
            .execute(
                "INSERT INTO nodes(name,address,port,enabled,share_uri) VALUES($1,$2,$3,true,$4)",
                &[&parsed.name, &parsed.host, &(parsed.port as i32), &line.to_string()],
            )
            .await
        {
            Ok(_) => count += 1,
            Err(err) => return db_failure(err),
        }
    }
    (StatusCode::CREATED, Json(json!({"count": count}))).into_response()
}

struct ParsedShare {
    name: String,
    host: String,
    port: u16,
}

fn parse_share(raw: &str) -> Result<ParsedShare, String> {
    if raw.len() > 8192 || raw.chars().any(char::is_whitespace) {
        return Err("请输入一条有效的 VLESS 或 Trojan 链接".into());
    }
    let (scheme, rest) = raw.split_once("://").ok_or("请输入一条有效的 VLESS 或 Trojan 链接")?;
    if scheme != "vless" && scheme != "trojan" {
        return Err("请输入一条有效的 VLESS 或 Trojan 链接".into());
    }
    let rest = rest.split('#').next().unwrap_or(rest);
    let rest = rest.split('?').next().unwrap_or(rest);
    let (credential, hostport) = rest.split_once('@').ok_or("链接需包含有效的凭据、服务器地址和端口")?;
    if credential.is_empty() {
        return Err("链接需包含有效的凭据、服务器地址和端口".into());
    }
    let hostport = hostport.trim_end_matches('/');
    let (host, port) = if hostport.starts_with('[') {
        let end = hostport.find(']').ok_or("链接需包含有效的服务器地址和端口")?;
        let host = &hostport[1..end];
        let port = hostport[end + 1..].strip_prefix(':').ok_or("链接需包含有效的服务器地址和端口")?;
        (host.to_string(), port.parse::<u16>().map_err(|_| "链接端口无效".to_string())?)
    } else {
        let (host, port) = hostport.rsplit_once(':').ok_or("链接需包含有效的服务器地址和端口")?;
        (host.to_string(), port.parse::<u16>().map_err(|_| "链接端口无效".to_string())?)
    };
    if host.is_empty() || port == 0 {
        return Err("链接需包含有效的服务器地址和端口".into());
    }
    let fragment = raw.split('#').nth(1).unwrap_or("");
    let name = url_decode(fragment);
    let name = if name.is_empty() {
        format!("{host}-{}", "tcp")
    } else {
        name
    };
    Ok(ParsedShare { name, host, port })
}
