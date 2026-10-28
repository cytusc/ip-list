import { pagedQuery } from "@/lib/paged-query";
import type { AvailableIP } from "@/lib/types";
import { AvailableIPList } from "@/components/available-ip-list";

export default async function AvailableIPsPage() {
  return <AvailableIPList initialItems={await pagedQuery<AvailableIP>("/available-ips")} />;
}
