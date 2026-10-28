import { pagedQuery } from "@/lib/paged-query";
import type { BlacklistedIP } from "@/lib/types";
import { BlacklistManager } from "@/components/blacklist-manager";

export default async function BlacklistPage() {
  return <BlacklistManager initialItems={await pagedQuery<BlacklistedIP>("/blacklist")} />;
}
