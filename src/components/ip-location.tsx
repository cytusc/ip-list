"use client";

import type { IPLocation } from "@/lib/types";

const countryNames = new Intl.DisplayNames(["zh-CN"], { type: "region" });

export function countryName(code: string) {
  return countryNames.of(code) || code;
}

export function IPLocationCell({ location }: { location: IPLocation | null | undefined }) {
  const code = location?.country || "";
  const valid = /^[A-Z]{2}$/.test(code);
  const country = valid ? countryName(code) : "";
  const flag = valid
    ? String.fromCodePoint(...[...code].map((letter) => 0x1f1e6 + letter.charCodeAt(0) - 65))
    : "";
  const detail = valid ? [...new Set([location?.region, location?.city].filter(Boolean))] : [];

  return (
    <div title="归属地来源：IPPure" className="min-h-12 space-y-1">
      {!valid ? (
        <span className="text-muted-foreground">
          {location === undefined ? "查询中…" : "未知归属地"}
        </span>
      ) : (
        <>
          <p className="flex items-center gap-2">
            <span role="img" aria-label={`${country}国旗`} className="text-base">
              {flag}
            </span>
            {country}
          </p>
          {detail.length > 0 && (
            <p className="text-muted-foreground">{detail.join(" · ")}</p>
          )}
        </>
      )}
    </div>
  );
}
