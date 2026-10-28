import { getDNSStatus } from "@/app/dns-actions";
import { DNSManager } from "@/components/dns-manager";

export default async function DNSPage() {
  const status = await getDNSStatus();
  return <DNSManager initialStatus={status} />;
}
