import { api } from "@/lib/api";
import { CFProgressView } from "@/components/cf-progress";
import type { CFIP, ScanJob } from "@/lib/types";

export const dynamic = "force-dynamic";

export default async function Page() {
  const [job, pool] = await Promise.all([
    api<ScanJob | null>("/cf-scan"),
    api<CFIP[]>("/cf-pool"),
  ]);
  return <CFProgressView initial={job.data} pool={pool.data} />;
}
