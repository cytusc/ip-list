"use client";

import { useEffect, useState } from "react";
import { getDNSStatus } from "@/app/dns-actions";
import { DNSManager } from "@/components/dns-manager";
import type { DNSStatus } from "@/lib/dns";

export default function DNSPage() {
  const [status, setStatus] = useState<DNSStatus | null>(null);
  useEffect(() => {
    getDNSStatus()
      .then(setStatus)
      .catch(() => setStatus(null));
  }, []);
  if (!status)
    return <p className="py-16 text-center text-sm text-muted-foreground">正在加载解析状态…</p>;
  return <DNSManager initialStatus={status} />;
}
