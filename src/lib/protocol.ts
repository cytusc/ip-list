export const protocolLabels: Record<string, string> = {
  "trojan/ws": "Trojan · WebSocket",
  "trojan/grpc": "Trojan · gRPC",
  "trojan/tcp": "Trojan · TCP",
  "vless/httpupgrade": "VLESS · HTTPUpgrade",
  "vless/xhttp": "VLESS · XHTTP",
  "vless/grpc": "VLESS · gRPC",
  "vless/ws": "VLESS · WebSocket",
  "vless/tcp": "VLESS · TCP",
};
export function nodeProtocol(raw: string): string {
  try {
    const uri = new URL(raw);
    return `${uri.protocol.slice(0, -1)}/${uri.searchParams.get("type") || "tcp"}`;
  } catch {
    return "";
  }
}

export const connectivityLabel = "gRPC · 所有协议共用";
