"use client";

import type { IPLocation } from "@/lib/types";

const countryNames = new Intl.DisplayNames(["zh-CN"], { type: "region" });

export function countryName(code: string) {
  return countryNames.of(code) || code;
}

export function IPLocationCell({ location }: { location: IPLocation | null | undefined }) {
  if (location === undefined) {
    return <span className="text-muted-foreground">查询中…</span>;
  }
  const code = location?.country || "";
  if (!/^[A-Z]{2}$/.test(code)) {
    return <span className="text-muted-foreground">未知归属地</span>;
  }
  const country = countryName(code);
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
