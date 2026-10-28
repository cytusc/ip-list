import { pagedQuery } from "@/lib/paged-query";
import type { LatencyProbe } from "@/lib/types";
import { LatencyProbeManager } from "@/components/latency-probe-manager";

export default async function LatencyProbesPage() {
  return <LatencyProbeManager initial={await pagedQuery<LatencyProbe>("/latency-probes")} tlsPin={process.env.PROBE_TLS_PIN || ""} />;
}
