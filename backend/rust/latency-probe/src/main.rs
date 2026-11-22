//! IP Guard latency probe.
//!
//! Measures the physical egress of the machine that runs it: ICMP echo (through
//! the system `ping`, matching the OS behaviour) or a TCP handshake. The probe
//! never routes through HTTP/SOCKS proxies, and never falls back to the default
//! route when the configured physical interface is unavailable.

use std::collections::BTreeMap;
use std::env;
use std::fs;
use std::io::Read;
use std::net::{IpAddr, SocketAddr};
use std::process::Command;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::mpsc;
use std::sync::Arc;
use std::thread;
use std::time::{Duration, Instant, SystemTime};

use base64::engine::general_purpose::STANDARD as BASE64;
use base64::Engine;
use chrono::{DateTime, Utc};
use der::{Decode, Encode};
use rustls::client::danger::{HandshakeSignatureValid, ServerCertVerified, ServerCertVerifier};
use rustls::client::WebPkiServerVerifier;
use rustls::pki_types::{CertificateDer, ServerName, UnixTime};
use rustls::{DigitallySignedStruct, RootCertStore, SignatureScheme};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use socket2::{Domain, Protocol, Socket, Type};
use x509_cert::Certificate;

#[derive(Debug, Clone, Deserialize)]
struct Settings {
    #[serde(default)]
    mode: String,
    #[serde(default)]
    tcp_port: i32,
    #[serde(default)]
    ips: Vec<String>,
    #[serde(default)]
    interval_seconds: i64,
    #[serde(default)]
    timeout_ms: i64,
    #[serde(default)]
    concurrency: i32,
    #[serde(default)]
    interface_name: String,
}

#[derive(Debug, Serialize, Clone)]
struct ResultItem {
    ip: String,
    delay_ms: f64,
    success: bool,
    checked_at: DateTime<Utc>,
}

#[derive(Debug, Deserialize, Default)]
struct ConfigFile {
    #[serde(default)]
    server: String,
    #[serde(default)]
    token: String,
    #[serde(default)]
    tls_pin: String,
}

struct Args {
    server: String,
    token: String,
    tls_pin: String,
    config: String,
    once: bool,
    check: bool,
}

fn parse_args() -> Args {
    let mut args = Args {
        server: env::var("PROBE_SERVER").unwrap_or_default(),
        token: env::var("PROBE_TOKEN").unwrap_or_default(),
        tls_pin: String::new(),
        config: String::new(),
        once: false,
        check: false,
    };
    let mut iter = env::args().skip(1);
    while let Some(arg) = iter.next() {
        match arg.as_str() {
            "-server" => args.server = iter.next().unwrap_or_default(),
            "-token" => args.token = iter.next().unwrap_or_default(),
            "-tls-pin" => args.tls_pin = iter.next().unwrap_or_default(),
            "-config" => args.config = iter.next().unwrap_or_default(),
            "-once" => args.once = true,
            "-check" => args.check = true,
            other => {
                eprintln!("未知参数：{other}");
                std::process::exit(2);
            }
        }
    }
    args
}

fn main() {
    let mut args = parse_args();
    if !args.config.is_empty() {
        match fs::read_to_string(&args.config) {
            Ok(content) => match serde_json::from_str::<ConfigFile>(&content) {
                Ok(cfg) => {
                    if !cfg.server.is_empty() {
                        args.server = cfg.server;
                    }
                    if !cfg.token.is_empty() {
                        args.token = cfg.token;
                    }
                    if !cfg.tls_pin.is_empty() {
                        args.tls_pin = cfg.tls_pin;
                    }
                }
                Err(err) => fatal(&format!("配置文件解析失败：{err}")),
            },
            Err(err) => fatal(&format!("读取配置文件失败：{err}")),
        }
    }
    if args.server.is_empty() || args.token.is_empty() {
        fatal("请设置 -server 和 -token，或设置 PROBE_SERVER、PROBE_TOKEN");
    }
    let base = args.server.trim_end_matches('/').to_string();
    let pin = match decode_pin(&args.tls_pin) {
        Ok(value) => value,
        Err(err) => fatal(&err),
    };
    let client = match build_client(pin) {
        Ok(value) => value,
        Err(err) => fatal(&err),
    };

    if args.check {
        let settings = load_settings_with_retry(&client, &base, &args.token);
        let settings = match settings {
            Ok(value) => value,
            Err(err) => fatal(&err),
        };
        if settings.mode != "tcp" && !cfg!(windows) {
            if Command::new("ping").arg("-h").output().is_err() {
                fatal("未找到系统 ping，请先安装 iputils-ping");
            }
        }
        let iface = match measurement_interface(&settings.interface_name) {
            Ok(value) => value,
            Err(err) => fatal(&err),
        };
        log_measurement_interface(&settings, &iface);
        log(&format!(
            "连接和鉴权成功：{} 个目标，间隔 {} 秒",
            settings.ips.len(),
            settings.interval_seconds
        ));
        return;
    }

    if !args.once {
        let heartbeat = client.clone();
        let heartbeat_base = base.clone();
        let heartbeat_token = args.token.clone();
        thread::spawn(move || heartbeat_loop(&heartbeat, &heartbeat_base, &heartbeat_token));
    }

    loop {
        let round_started = Instant::now();
        let mut settings = match load_settings(&client, &base, &args.token) {
            Ok(value) => value,
            Err(err) => {
                if args.once {
                    fatal(&format!("读取探针任务失败：{err}"));
                }
                log(&format!("读取探针任务失败: {err}"));
                thread::sleep(Duration::from_secs(30));
                continue;
            }
        };
        let iface = match measurement_interface(&settings.interface_name) {
            Ok(value) => value,
            Err(err) => {
                if args.once {
                    fatal(&err);
                }
                log(&format!("停止本轮检测，未上报延迟：{err}"));
                thread::sleep(Duration::from_secs(30));
                continue;
            }
        };
        settings.interface_name = iface.name.clone();
        log_measurement_interface(&settings, &iface);

        let results = run_measurements(&settings, &iface);
        if !results.is_empty() {
            let mut report_error: Option<String> = None;
            for chunk in results.chunks(5000) {
                if let Err(err) = report(&client, &base, &args.token, chunk, &settings) {
                    report_error = Some(err);
                    break;
                }
            }
            if let Some(err) = report_error {
                if args.once {
                    fatal(&format!("上报检测结果失败: {err}"));
                }
                log(&format!("上报检测结果失败: {err}"));
            } else {
                let mut ok = 0usize;
                let mut min = f64::INFINITY;
                let mut max = 0.0f64;
                for item in &results {
                    if item.success {
                        ok += 1;
                        min = min.min(item.delay_ms);
                        max = max.max(item.delay_ms);
                    }
                }
                log(&format!(
                    "已检测 {} 个 IP：{} 可达，{} 未响应或不可达",
                    results.len(),
                    ok,
                    results.len() - ok
                ));
                if ok == 0 {
                    log("本轮目标全部未响应；请用系统 ping 检查同一目标，并检查物理出口、VPN/TUN 和防火墙。ICMP 无响应不等于代理端口不可用");
                } else {
                    log(&format!(
                        "物理出口 {} 延迟范围：{:.2}–{:.2} ms",
                        settings.interface_name, min, max
                    ));
                }
            }
        }
        if args.once {
            return;
        }
        let interval = Duration::from_secs(settings.interval_seconds.max(15) as u64);
        let elapsed = round_started.elapsed();
        thread::sleep(interval.saturating_sub(elapsed).max(Duration::from_secs(1)));
    }
}

fn fatal(message: &str) -> ! {
    log(message);
    std::process::exit(1)
}

fn log(message: &str) {
    eprintln!("{} {message}", Utc::now().format("%Y/%m/%d %H:%M:%S"));
}

fn decode_pin(raw: &str) -> Result<Option<[u8; 32]>, String> {
    if raw.is_empty() {
        return Ok(None);
    }
    let bytes = BASE64
        .decode(raw)
        .map_err(|_| "服务器公钥指纹格式无效".to_string())?;
    let array: [u8; 32] = bytes
        .as_slice()
        .try_into()
        .map_err(|_| "服务器公钥指纹格式无效".to_string())?;
    Ok(Some(array))
}

#[derive(Debug)]
struct PinVerifier {
    inner: Arc<dyn ServerCertVerifier>,
    pin: Option<[u8; 32]>,
}

impl ServerCertVerifier for PinVerifier {
    fn verify_server_cert(
        &self,
        end_entity: &CertificateDer<'_>,
        intermediates: &[CertificateDer<'_>],
        server_name: &ServerName<'_>,
        ocsp_response: &[u8],
        now: UnixTime,
    ) -> Result<ServerCertVerified, rustls::Error> {
        if let Some(expected) = self.pin {
            let certificate = Certificate::from_der(end_entity.as_ref())
                .map_err(|_| rustls::Error::General("证书无法解析".into()))?;
            let spki = certificate
                .tbs_certificate
                .subject_public_key_info
                .to_der()
                .map_err(|_| rustls::Error::General("公钥无法编码".into()))?;
            let actual = Sha256::digest(spki);
            if actual.as_slice() != expected {
                return Err(rustls::Error::General("服务器公钥指纹不匹配".into()));
            }
            return Ok(ServerCertVerified::assertion());
        }
        self.inner
            .verify_server_cert(end_entity, intermediates, server_name, ocsp_response, now)
    }

    fn verify_tls12_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        dss: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, rustls::Error> {
        self.inner.verify_tls12_signature(message, cert, dss)
    }

    fn verify_tls13_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        dss: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, rustls::Error> {
        self.inner.verify_tls13_signature(message, cert, dss)
    }

    fn supported_verify_schemes(&self) -> Vec<SignatureScheme> {
        self.inner.supported_verify_schemes()
    }
}

fn build_client(pin: Option<[u8; 32]>) -> Result<reqwest::blocking::Client, String> {
    let roots: RootCertStore = webpki_roots::TLS_SERVER_ROOTS.iter().cloned().collect();
    let inner = WebPkiServerVerifier::builder(Arc::new(roots))
        .build()
        .map_err(|_| "初始化 TLS 校验失败".to_string())?;
    let verifier = Arc::new(PinVerifier { inner, pin });
    let tls = rustls::ClientConfig::builder()
        .dangerous()
        .with_custom_certificate_verifier(verifier)
        .with_no_client_auth();
    reqwest::blocking::Client::builder()
        .timeout(Duration::from_secs(30))
        .redirect(reqwest::redirect::Policy::none())
        .use_preconfigured_tls(tls)
        .build()
        .map_err(|err| format!("创建 HTTPS 客户端失败：{err}"))
}

fn load_settings_with_retry(
    client: &reqwest::blocking::Client,
    base: &str,
    token: &str,
) -> Result<Settings, String> {
    let mut last = String::new();
    for attempt in 0..3 {
        match load_settings(client, base, token) {
            Ok(value) => return Ok(value),
            Err(err) => {
                last = err;
                if attempt < 2 {
                    thread::sleep(Duration::from_secs(2));
                }
            }
        }
    }
    Err(last)
}

fn load_settings(
    client: &reqwest::blocking::Client,
    base: &str,
    token: &str,
) -> Result<Settings, String> {
    let response = client
        .get(format!("{base}/api/probe-agent/targets"))
        .header("Authorization", format!("Bearer {token}"))
        .header("Accept", "application/json")
        .header("X-Probe-Features", "tcp-ping-v1")
        .send()
        .map_err(|err| format!("请求探针任务失败：{err}"))?;
    let status = response.status();
    if !status.is_success() {
        let mut body = String::new();
        let _ = response.take(4096).read_to_string(&mut body);
        return Err(format!("HTTP {}: {}", status.as_u16(), body.trim()));
    }
    response
        .json::<Settings>()
        .map_err(|err| format!("探针任务无法解析：{err}"))
}

fn report(
    client: &reqwest::blocking::Client,
    base: &str,
    token: &str,
    results: &[ResultItem],
    settings: &Settings,
) -> Result<(), String> {
    let mode = if settings.mode.is_empty() {
        "icmp"
    } else {
        settings.mode.as_str()
    };
    let port = if mode == "tcp" { settings.tcp_port } else { 0 };
    let body = serde_json::json!({"results": results, "mode": mode, "tcp_port": port});
    let response = client
        .post(format!("{base}/api/probe-agent/results"))
        .header("Authorization", format!("Bearer {token}"))
        .header("Accept", "application/json")
        .header("X-Probe-Features", "tcp-ping-v1")
        .json(&body)
        .send()
        .map_err(|err| format!("上报检测结果失败：{err}"))?;
    let status = response.status();
    if !status.is_success() {
        let mut body = String::new();
        let _ = response.take(4096).read_to_string(&mut body);
        return Err(format!("HTTP {}: {}", status.as_u16(), body.trim()));
    }
    Ok(())
}

fn heartbeat_loop(client: &reqwest::blocking::Client, base: &str, token: &str) {
    loop {
        let result = client
            .post(format!("{base}/api/probe-agent/heartbeat"))
            .header("Authorization", format!("Bearer {token}"))
            .send();
        if let Err(err) = result {
            log(&format!("心跳失败: {err}"));
        }
        thread::sleep(Duration::from_secs(20));
    }
}

struct Interface {
    name: String,
    v4: Vec<IpAddr>,
    v6: Vec<IpAddr>,
}

fn measurement_interface(configured: &str) -> Result<Interface, String> {
    let candidates = physical_interfaces();
    let configured = configured.trim();
    if !configured.is_empty() {
        return candidates
            .into_iter()
            .find(|iface| iface.name == configured)
            .ok_or_else(|| {
                format!("出口网卡 \"{configured}\" 不存在、未连接或属于虚拟隧道；请填写已连接的物理网卡")
            });
    }
    match candidates.len() {
        1 => Ok(candidates.into_iter().next().unwrap()),
        0 => Err("未找到可用物理网卡，拒绝使用系统默认路由；请在探针编辑中配置出口网卡".into()),
        _ => Err(format!(
            "存在多个物理出口（{}），请在探针编辑中明确选择出口网卡",
            candidates
                .iter()
                .map(|iface| iface.name.as_str())
                .collect::<Vec<_>>()
                .join(", ")
        )),
    }
}

fn physical_interfaces() -> Vec<Interface> {
    let mut grouped: BTreeMap<String, Vec<IpAddr>> = BTreeMap::new();
    if let Ok(list) =
        <network_interface::NetworkInterface as network_interface::NetworkInterfaceConfig>::show()
    {
        for iface in list {
            for addr in iface.addr {
                grouped.entry(iface.name.clone()).or_default().push(addr.ip());
            }
        }
    }
    let mut result = Vec::new();
    for (name, addrs) in grouped {
        let lower = name.to_lowercase();
        if lower.starts_with("lo") || lower.contains("loopback") {
            continue;
        }
        for prefix in [
            "utun", "tun", "tap", "wg", "tailscale", "docker", "veth", "br-", "bridge", "virbr",
            "vmnet", "awdl", "llw", "anpi", "gif", "stf", "ipsec", "zt",
        ] {
            if lower.starts_with(prefix) {
                continue;
            }
        }
        for marker in [
            "clash",
            "mihomo",
            "sing-box",
            "wintun",
            "wireguard",
            "vpn",
        ] {
            if lower.contains(marker) {
                continue;
            }
        }
        let mut v4 = Vec::new();
        let mut v6 = Vec::new();
        for addr in addrs {
            match addr {
                IpAddr::V4(value) if !value.is_loopback() && !value.is_unspecified() => {
                    v4.push(addr)
                }
                IpAddr::V6(value) if !value.is_loopback() && !value.is_unspecified() => {
                    v6.push(addr)
                }
                _ => {}
            }
        }
        if v4.is_empty() && v6.is_empty() {
            continue;
        }
        result.push(Interface {
            name,
            v4,
            v6,
        });
    }
    result
}

fn log_measurement_interface(settings: &Settings, iface: &Interface) {
    let engine = if settings.mode == "tcp" {
        "TCP socket"
    } else {
        "系统 ping"
    };
    let label = if settings.mode == "tcp" {
        format!("TCP Ping :{}", settings.tcp_port)
    } else {
        "ICMP Ping".to_string()
    };
    let v4 = iface.v4.first().map(|ip| ip.to_string()).unwrap_or_default();
    let v6 = iface.v6.first().map(|ip| ip.to_string()).unwrap_or_default();
    log(&format!(
        "{label} 检测绑定出口网卡：{}（IPv4={}，IPv6={}，{}）",
        iface.name, v4, v6, engine
    ));
}

fn run_measurements(settings: &Settings, iface: &Interface) -> Vec<ResultItem> {
    let mut concurrency = settings.concurrency;
    if concurrency < 1 {
        concurrency = 64;
    }
    if concurrency > 512 {
        concurrency = 512;
    }
    let timeout = if settings.timeout_ms < 250 {
        1000
    } else {
        settings.timeout_ms
    } as u64;
    let total = settings.ips.len();
    if total == 0 {
        return Vec::new();
    }
    let workers = (concurrency as usize).min(total);
    let next = Arc::new(AtomicUsize::new(0));
    let (tx, rx) = mpsc::channel::<ResultItem>();
    let failures = Arc::new(AtomicUsize::new(0));
    let ips = Arc::new(settings.ips.clone());
    let mode = settings.mode.clone();
    let port = settings.tcp_port;
    let iface_name = iface.name.clone();
    let source4 = iface.v4.first().copied();
    let source6 = iface.v6.first().copied();
    let handles: Vec<_> = (0..workers)
        .map(|_| {
            let next = Arc::clone(&next);
            let ips = Arc::clone(&ips);
            let tx = tx.clone();
            let failures = Arc::clone(&failures);
            let mode = mode.clone();
            let iface_name = iface_name.clone();
            thread::spawn(move || loop {
                let index = next.fetch_add(1, Ordering::Relaxed);
                if index >= ips.len() {
                    return;
                }
                let raw = &ips[index];
                let ip: IpAddr = match raw.parse() {
                    Ok(value) => value,
                    Err(_) => continue,
                };
                let started = SystemTime::now();
                let checked_at: DateTime<Utc> = started.into();
                let result = if mode == "tcp" {
                    tcp_ping(ip, port, timeout, source4, source6)
                } else {
                    icmp_ping(ip, timeout, &iface_name, source4, source6)
                };
                match result {
                    Some(delay) => {
                        let _ = tx.send(ResultItem {
                            ip: ip.to_string(),
                            delay_ms: delay,
                            success: true,
                            checked_at,
                        });
                    }
                    None => {
                        failures.fetch_add(1, Ordering::Relaxed);
                        let _ = tx.send(ResultItem {
                            ip: ip.to_string(),
                            delay_ms: 0.0,
                            success: false,
                            checked_at,
                        });
                    }
                }
            })
        })
        .collect();
    drop(tx);
    let mut items = Vec::with_capacity(total);
    for item in rx {
        items.push(item);
    }
    for handle in handles {
        let _ = handle.join();
    }
    items
}

fn tcp_ping(ip: IpAddr, port: i32, timeout_ms: u64, source4: Option<IpAddr>, source6: Option<IpAddr>) -> Option<f64> {
    if !(1..=65535).contains(&port) {
        return None;
    }
    let domain = if ip.is_ipv4() { Domain::IPV4 } else { Domain::IPV6 };
    let socket = Socket::new(domain, Type::STREAM, Some(Protocol::TCP)).ok()?;
    let source = if ip.is_ipv4() { source4 } else { source6 };
    if let Some(source) = source {
        let _ = socket.bind(&SocketAddr::new(source, 0).into());
    }
    let started = Instant::now();
    socket
        .connect_timeout(&SocketAddr::new(ip, port as u16).into(), Duration::from_millis(timeout_ms))
        .ok()?;
    let elapsed = started.elapsed().as_secs_f64() * 1000.0;
    Some(elapsed.max(0.01))
}

fn icmp_ping(
    ip: IpAddr,
    timeout_ms: u64,
    iface_name: &str,
    source4: Option<IpAddr>,
    source6: Option<IpAddr>,
) -> Option<f64> {
    let mut command = Command::new(if cfg!(target_os = "macos") && ip.is_ipv6() {
        "ping6"
    } else {
        "ping"
    });
    let timeout = Duration::from_millis(timeout_ms);
    if cfg!(windows) {
        command.arg(if ip.is_ipv4() { "-4" } else { "-6" });
        command.args(["-n", "1", "-w", &timeout_ms.to_string()]);
        if let Some(source) = if ip.is_ipv4() { source4 } else { source6 } {
            command.args(["-S", &source.to_string()]);
        }
    } else if cfg!(target_os = "macos") {
        command.args(["-n", "-c", "1", "-W", &timeout_ms.to_string()]);
        if !iface_name.is_empty() {
            command.args(["-b", iface_name]);
        }
    } else {
        let seconds = ((timeout.as_millis() as f64) / 1000.0).ceil() as u64;
        command.args(["-n", "-c", "1", "-W", &seconds.max(1).to_string()]);
        if !iface_name.is_empty() {
            command.args(["-I", iface_name]);
        }
    }
    command.arg(ip.to_string());
    command.env("LC_ALL", "C").env("LANG", "C");
    let output = command.output().ok()?;
    let text = String::from_utf8_lossy(&output.stdout);
    let fallback = String::from_utf8_lossy(&output.stderr);
    let combined = format!("{text}\n{fallback}");
    parse_delay(&combined)
}

fn parse_delay(output: &str) -> Option<f64> {
    for marker in ["time=", "时间=", "time<", "时间<"] {
        if let Some(position) = output.find(marker) {
            let rest = &output[position + marker.len()..];
            let digits: String = rest
                .chars()
                .skip_while(|c| c.is_whitespace() || *c == '<' || *c == '=')
                .take_while(|c| c.is_ascii_digit() || *c == '.')
                .collect();
            if let Ok(value) = digits.parse::<f64>() {
                if value.is_finite() {
                    return Some(if value <= 0.0 { 0.01 } else { value });
                }
            }
        }
    }
    None
}
