import { getLatencyProbes } from "@/lib/queries";
import { LatencyProbeManager } from "@/components/latency-probe-manager";

export default async function LatencyProbesPage() {
  return <LatencyProbeManager initial={await getLatencyProbes()} tlsPin={process.env.PROBE_TLS_PIN || ""} />;
}
