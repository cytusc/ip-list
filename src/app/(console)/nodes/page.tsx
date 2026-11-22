"use client";

import { emptyPage } from "@/lib/paged-query";
import type { Node } from "@/lib/types";
import { ConfigManager } from "@/components/config-manager";

const initial = emptyPage<Node>();

export default function NodesPage() {
  return <ConfigManager kind="nodes" initial={initial} />;
}
