"use client";

import { emptyPage } from "@/lib/paged-query";
import { CFProgressView } from "@/components/cf-progress";
import type { CFIP } from "@/lib/types";

const pool = emptyPage<CFIP>();

export default function Page() {
  return <CFProgressView initial={null} pool={pool} />;
}
