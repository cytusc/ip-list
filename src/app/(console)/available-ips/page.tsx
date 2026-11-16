import { pagedQuery } from "@/lib/paged-query";
import type { AvailableIP } from "@/lib/types";
import { AvailableIPList } from "@/components/available-ip-list";

export default async function AvailableIPsPage() {
  // This list aggregates the whole shared pool and probe history; it needs more
  // head room than the default request budget.
  return (
    <AvailableIPList
      initialItems={await pagedQuery<AvailableIP>("/available-ips", undefined, 32000)}
    />
  );
}
