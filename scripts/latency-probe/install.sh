#!/bin/sh
# Usage: sh install.sh SERVER TOKEN [TLS_PUBLIC_KEY_SHA256_BASE64]
set -eu
umask 077
server=${1:?缺少网站地址}
token=${2:?缺少探针密钥}
pin=${3:-}
case "$server" in https://*) ;; *) echo '必须使用 HTTPS 网站地址' >&2; exit 1;; esac
# These values are written into JSON and service definitions; reject unsafe input.
case "$server" in *[!a-zA-Z0-9.:/_-]*) echo '网站地址格式无效' >&2; exit 1;; esac
case "$token" in ''|*[!a-zA-Z0-9_-]*) echo '密钥格式无效' >&2; exit 1;; esac
case "$pin" in *[!a-zA-Z0-9+/=]*) echo '公钥指纹格式无效' >&2; exit 1;; esac
server=${server%/}
case "$(uname -s)" in
  Darwin) os=darwin; root="$HOME/Library/Application Support/ip-list-probe" ;;
  Linux) os=linux; root=/opt/ip-list-probe
    [ "$(id -u)" = 0 ] || { echo 'Linux 请使用 sudo sh 运行安装命令' >&2; exit 1; }
    command -v systemctl >/dev/null || { echo '需要 systemd' >&2; exit 1; } ;;
  *) echo '此安装脚本支持 macOS 和 Linux；Windows 请下载独立程序' >&2; exit 1;;
esac
case "$(uname -m)" in x86_64|amd64) arch=amd64;; aarch64|arm64) arch=arm64;; *) echo '暂不支持此 CPU 架构' >&2; exit 1;; esac
command -v curl >/dev/null || { echo '请先安装 curl' >&2; exit 1; }
command -v ping >/dev/null || { echo '请先安装系统 ping（Debian/Ubuntu：apt install iputils-ping）' >&2; exit 1; }
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
url="$server/probe-downloads/latency-probe-$os-$arch"
if [ -n "$pin" ]; then
  curl --fail --silent --show-error --connect-timeout 15 --max-time 180 --insecure --pinnedpubkey "sha256//$pin" "$url" -o "$tmp/probe"
else
  curl --fail --silent --show-error --connect-timeout 15 --max-time 180 "$url" -o "$tmp/probe"
fi
chmod 700 "$tmp/probe"
printf '{"Server":"%s","Token":"%s","TLSPin":"%s"}\n' "$server" "$token" "$pin" > "$tmp/config.json"
# Test credentials and TLS before replacing any running installation.
"$tmp/probe" -config "$tmp/config.json" -check
mkdir -p "$root"
chmod 700 "$root"
cp "$tmp/probe" "$root/latency-probe.new"
mv "$root/latency-probe.new" "$root/latency-probe"
cp "$tmp/config.json" "$root/config.json"
chmod 600 "$root/config.json"
if [ "$os" = linux ]; then
  cat > /etc/systemd/system/ip-list-probe.service <<UNIT
[Unit]
Description=IP List latency probe
After=network-online.target
Wants=network-online.target
[Service]
ExecStart="$root/latency-probe" -config "$root/config.json"
Restart=always
RestartSec=15
UMask=0077
[Install]
WantedBy=multi-user.target
UNIT
  systemctl daemon-reload
  systemctl enable ip-list-probe.service
  systemctl restart ip-list-probe.service
  echo '安装完成。状态：systemctl status ip-list-probe；日志：journalctl -u ip-list-probe -f'
  echo '停止：sudo systemctl disable --now ip-list-probe'
else
  # XML-escape home paths before embedding them in a launchd plist.
  xmlroot=$(printf '%s' "$root" | sed 's/\&/\&amp;/g; s/</\&lt;/g; s/>/\&gt;/g; s/"/\&quot;/g')
  mkdir -p "$HOME/Library/LaunchAgents"
  plist="$HOME/Library/LaunchAgents/com.ip-list.latency-probe.plist"
  launchctl bootout "gui/$(id -u)" "$plist" 2>/dev/null || true
  cat > "$plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>com.ip-list.latency-probe</string>
<key>ProgramArguments</key><array><string>$xmlroot/latency-probe</string><string>-config</string><string>$xmlroot/config.json</string></array>
<key>RunAtLoad</key><true/><key>KeepAlive</key><true/>
<key>ThrottleInterval</key><integer>15</integer>
<key>StandardOutPath</key><string>$xmlroot/probe.log</string>
<key>StandardErrorPath</key><string>$xmlroot/probe.log</string>
</dict></plist>
PLIST
  plutil -lint "$plist"
  launchctl bootstrap "gui/$(id -u)" "$plist"
  echo "安装完成，登录后自动运行。日志：$root/probe.log"
  echo "停止：launchctl bootout gui/$(id -u) '$plist'"
fi
