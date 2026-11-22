"use client";

import { emptyPage } from "@/lib/paged-query";
import type { AvailableIP } from "@/lib/types";
import { AvailableIPList } from "@/components/available-ip-list";

const initial = emptyPage<AvailableIP>();

export default function AvailableIPsPage() {
  return <AvailableIPList initialItems={initial} />;
}
