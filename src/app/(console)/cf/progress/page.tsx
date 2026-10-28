import { api } from "@/lib/api";
import { pagedQuery } from "@/lib/paged-query";
import { CFProgressView } from "@/components/cf-progress";
import type { CFIP, ScanJob } from "@/lib/types";

export const dynamic = "force-dynamic";

export default async function Page() {
  const [job, pool] = await Promise.all([
    api<ScanJob | null>("/cf-scan?summary=1"),
    pagedQuery<CFIP>("/cf-pool"),
  ]);
  return <CFProgressView initial={job.data} pool={pool} />;
}
