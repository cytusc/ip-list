"use client";

import { emptyPage } from "@/lib/paged-query";
import { CFTargetList } from "@/components/cf-manager";
import type { CFTarget } from "@/lib/types";

const initial = emptyPage<CFTarget>();

export default function Page() {
  return <CFTargetList initial={initial} networks={[]} />;
}
