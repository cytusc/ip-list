export const protocolLabels: Record<string, string> = {
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
