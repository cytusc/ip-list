import { getLatencyProbes } from "@/lib/queries";
import { LatencyProbeManager } from "@/components/latency-probe-manager";

export default async function LatencyProbesPage() {
  return <LatencyProbeManager initial={await getLatencyProbes()} />;
}
