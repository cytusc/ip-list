"use client";

import type { PageResult } from "@/lib/pagination";
import type { ScanRecord, ScanRecordList } from "@/lib/types";
import { ProbeList } from "@/components/probe-manager";

const initial: ScanRecordList & PageResult<ScanRecord> = {
  items: [],
  active: null,
  total: 0,
  page: 1,
  pageSize: 50,
};

export default function ProbesPage() {
  return <ProbeList nodes={[]} networks={[]} initial={initial} />;
}
