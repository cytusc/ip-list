"use client";

import { emptyPage } from "@/lib/paged-query";
import type { BlacklistedIP } from "@/lib/types";
import { BlacklistManager } from "@/components/blacklist-manager";

const initial = emptyPage<BlacklistedIP>();

export default function BlacklistPage() {
  return <BlacklistManager initialItems={initial} />;
}
