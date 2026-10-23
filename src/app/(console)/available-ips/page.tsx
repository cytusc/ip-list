import { getAvailableIPs } from "@/lib/queries";
import { AvailableIPList } from "@/components/available-ip-list";

export default async function AvailableIPsPage() {
  return <AvailableIPList initialItems={await getAvailableIPs()} />;
}
