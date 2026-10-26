// Quality targets for probes in mainland China, not universal reachability limits.
// Reference: https://wondernetwork.com/pings/Shanghai (route-specific RTTs).
export function latencyThresholds(country?: string, region?: string, city?: string) {
  const code = country?.trim().toUpperCase();
  if (code && ["HK", "MO"].includes(code)) return { name: "港澳（中国大陆探针参考）", green: 50, yellow: 80 };
  if (code === "TW") return { name: "台湾（中国大陆探针参考）", green: 60, yellow: 100 };
  if (code && ["JP", "KR"].includes(code)) return { name: "日韩（中国大陆探针参考）", green: 70, yellow: 110 };
  if (code === "SG") return { name: "新加坡（中国大陆探针参考）", green: 80, yellow: 120 };
  if (code && ["MY", "TH", "VN", "PH", "ID", "KH", "LA", "MM", "BN", "TL"].includes(code)) {
    return { name: "东南亚（中国大陆探针参考）", green: 100, yellow: 150 };
  }
  if (code === "US") {
    const place = `${region || ""} ${city || ""}`.toLowerCase();
    if (/\b(california|oregon|nevada|arizona|los angeles|san jose|san francisco|seattle|portland|fremont|las vegas|phoenix|san diego|sacramento)\b|加利福尼亚|加州|俄勒冈|内华达|亚利桑那|洛杉矶|圣何塞|旧金山|西雅图/.test(place) || /^(CA|WA|OR|NV|AZ|Washington|华盛顿州)$/i.test(region?.trim() || "")) {
      return { name: "美国西部（中国大陆探针参考）", green: 160, yellow: 200 };
    }
    if (/\b(new york|new jersey|virginia|florida|georgia|massachusetts|pennsylvania|maryland|carolina|boston|miami|atlanta|ashburn|secaucus|newark)\b|纽约|新泽西|弗吉尼亚|佛罗里达|佐治亚|马萨诸塞|宾夕法尼亚|马里兰|卡罗来纳|波士顿|迈阿密/.test(place) || /^(NY|NJ|VA|WV|FL|GA|MA|PA|MD|NC|SC|DC|CT|RI|DE|ME|NH|VT)$/i.test(region?.trim() || "")) {
      return { name: "美国东部（中国大陆探针参考）", green: 200, yellow: 240 };
    }
    if (/\b(texas|illinois|colorado|dallas|houston|chicago|denver)\b|得克萨斯|德克萨斯|伊利诺伊|科罗拉多|达拉斯|芝加哥|丹佛/.test(place) || /^(TX|IL|CO|KS|MO|NE|IA|OK|MN|WI|IN|MI|OH|TN|KY|AR|LA|MS|AL|SD|ND)$/i.test(region?.trim() || "")) {
      return { name: "美国中部（中国大陆探针参考）", green: 180, yellow: 220 };
    }
    return { name: "美国（地区未识别，采用美西阈值）", green: 160, yellow: 200 };
  }
  return { name: code ? "其他地区（中国大陆探针参考）" : "未知地区（默认参考）", green: 200, yellow: 250 };
}

export function latencyQuality(delay: number | null, success: boolean | null, country?: string, region?: string, city?: string) {
  if (success == null || (success && (delay == null || !Number.isFinite(delay) || delay < 0))) return "unknown";
  if (!success) return "red";
  const thresholds = latencyThresholds(country, region, city);
  return delay! <= thresholds.green ? "green" : delay! <= thresholds.yellow ? "yellow" : "red";
}
