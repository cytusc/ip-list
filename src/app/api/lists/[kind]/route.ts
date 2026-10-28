import { ApiError } from "@/lib/api";
import { pagedQuery } from "@/lib/paged-query";

const paths: Record<string,string> = {
  nodes: "/nodes", networks: "/networks", blacklist: "/blacklist",
  "available-ips": "/available-ips", "latency-probes": "/latency-probes",
  "scan-records": "/scan-records", "cf-targets": "/cf-targets", "cf-pool": "/cf-pool",
  "dns-records":"/dns/records", "dns-candidates":"/dns/candidates", "dns-history":"/dns/history",
};
export async function GET(request: Request, { params }: { params: Promise<{kind:string}> }) {
  const {kind} = await params;
  const query=new URL(request.url).searchParams;
  let path=Object.hasOwn(paths,kind) ? paths[kind] : "";
  if(kind==="cf-detail" && /^[\da-f-]{36}$/i.test(query.get("id") || "")) path=`/cf-targets/${query.get("id")}`;
  if(kind==="scan-detail" && /^\d+$/.test(query.get("id") || "")) {path=`/scan-records/${query.get("id")}`;query.set("paged","1");}
  if (!path) return Response.json({error:"列表不存在"},{status:404});
  try {
    return Response.json(await pagedQuery(`${path}?${query}`,request.signal), {headers:{"Cache-Control":"no-store"}});
  } catch(error) {
    return Response.json({error:error instanceof ApiError ? error.message : "列表加载失败，请重试"}, {status:error instanceof ApiError ? error.status : 503});
  }
}
