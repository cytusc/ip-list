import { getDNSStatus } from "@/app/dns-actions";
import { getLatencyProbes } from "@/lib/queries";
import { DNSManager } from "@/components/dns-manager";

export default async function DNSPage() {
  const [status, probes] = await Promise.all([getDNSStatus(), getLatencyProbes()]);
  return <DNSManager initialStatus={status} probes={probes} />;
}
