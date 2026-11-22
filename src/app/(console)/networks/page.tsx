"use client";

import { emptyPage } from "@/lib/paged-query";
import type { Network } from "@/lib/types";
import { ConfigManager } from "@/components/config-manager";

const initial = emptyPage<Network>();

export default function NetworksPage() {
  return <ConfigManager kind="networks" initial={initial} />;
}
