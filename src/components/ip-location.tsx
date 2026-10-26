"use client";

import { useEffect, useState } from "react";
import type { IPLocation } from "@/lib/types";

const countryNames = new Intl.DisplayNames(["zh-CN"], { type: "region" });

export function IPLocationCell({ ip, onResolved }: { ip: string; onResolved?: (ip: string, location: IPLocation | null) => void }) {
  const [location, setLocation] = useState<IPLocation | null>();

  useEffect(() => {
    const controller = new AbortController();
    fetch(`/api/available-ips/location?ip=${encodeURIComponent(ip)}`, {
      signal: controller.signal,
    })
      .then(async (response) => {
        if (!response.ok) throw new Error("查询失败");
        const data: IPLocation = await response.json();
        setLocation(data);
        onResolved?.(ip, data);
      })
      .catch(() => {
        if (!controller.signal.aborted) { setLocation(null); onResolved?.(ip, null); }
      });
    return () => controller.abort();
  }, [ip, onResolved]);

  if (location === undefined) {
    return <span className="text-muted-foreground">查询中…</span>;
  }
  const code = location?.country || "";
  if (!/^[A-Z]{2}$/.test(code)) {
    return <span className="text-muted-foreground">未知归属地</span>;
  }
  const country = countryNames.of(code) || code;
  const flag = String.fromCodePoint(
    ...[...code].map((letter) => 0x1f1e6 + letter.charCodeAt(0) - 65),
  );
  const detail = [...new Set([location?.region, location?.city].filter(Boolean))];

  return (
    <div title="归属地来源：IPPure" className="space-y-1">
      <p className="flex items-center gap-2">
        <span role="img" aria-label={`${country}国旗`} className="text-base">
          {flag}
        </span>
        {country}
      </p>
      {detail.length > 0 && (
        <p className="text-muted-foreground">{detail.join(" · ")}</p>
      )}
    </div>
  );
}
