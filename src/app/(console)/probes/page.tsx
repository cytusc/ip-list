import { pagedQuery } from "@/lib/paged-query";
import type { ScanRecord, ScanRecordList } from "@/lib/types";
import { ProbeList } from "@/components/probe-manager";
export const dynamic = "force-dynamic";
export default async function ProbesPage() {
  const records=await pagedQuery<ScanRecord, ScanRecordList>("/scan-records");
  return (
    <ProbeList nodes={[]} networks={[]} initial={records} />
  );
}
