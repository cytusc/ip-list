"use client";

import { useState } from "react";
import { toast } from "sonner";
import type { IPLocation } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

const countryNames = new Intl.DisplayNames(["zh-CN"], { type: "region" });

export function countryName(code: string) {
  return countryNames.of(code) || code;
}

export function IPLocationCell({ ip, location, onChange }: {
  ip: string;
  location: IPLocation | null | undefined;
  onChange: (location: IPLocation) => void;
}) {
  const [open, setOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const [countryCode, setCountryCode] = useState("");
  const [region, setRegion] = useState("");
  const [city, setCity] = useState("");
  const [error, setError] = useState("");
  const code = location?.country || "";
  const valid = /^[A-Z]{2}$/.test(code);
  const country = valid ? countryName(code) : "";
  const flag = valid ? String.fromCodePoint(...[...code].map(letter => 0x1f1e6 + letter.charCodeAt(0) - 65)) : "";
  const detail = valid ? [...new Set([location?.region, location?.city].filter(Boolean))] : [];
  const status = location?.status;

  async function save(automatic = false) {
    setSaving(true);
    setError("");
    try {
      const response = await fetch(`/api/available-ips/location?ip=${encodeURIComponent(ip)}`, {
        method: automatic ? "DELETE" : "PUT",
        headers: { "Content-Type": "application/json" },
        ...(automatic ? {} : { body: JSON.stringify({ country: countryCode, region, city }) }),
      });
      const data = await response.json();
      if (!response.ok) throw new Error(data.error || "保存失败，请重试");
      onChange(data);
      setOpen(false);
      toast.success(automatic ? "已恢复自动判断，下次 DNS 同步时重新分组" : "已保存人工纠正，下次 DNS 同步时生效");
    } catch (e) {
      setError(e instanceof Error ? e.message : "保存失败，请重试");
    } finally { setSaving(false); }
  }

  return <div className="min-h-12 space-y-1">
    {!valid ? <span className={status === "conflict" ? "text-amber-700" : "text-muted-foreground"}>
      {location === undefined ? "查询中…" : status === "conflict" ? "定位冲突 · 待确认" : status === "insufficient" ? "定位来源不足" : status === "unavailable" || location === null ? "定位服务暂不可用 · 稍后重试" : "未知归属地"}
    </span> : <>
      <p className="flex items-center gap-2"><span role="img" aria-label={`${country}国旗`} className="text-base">{flag}</span>{country}</p>
      {detail.length > 0 && <p className="text-muted-foreground">{detail.join(" · ")}</p>}
    </>}
    <Dialog open={open} onOpenChange={value => {
      if (saving) return;
      setOpen(value);
      if (value) { setCountryCode(code); setRegion(location?.region || ""); setCity(location?.city || ""); setError(""); }
    }}>
      <DialogTrigger asChild><Button variant="link" size="sm" className="h-auto p-0 text-xs" aria-label={`查看或纠正 ${ip} 的定位`}>
        {status === "manual" ? "人工纠正 · 查看" : status === "consensus" ? "多库一致 · 查看" : "查看 / 纠正"}
      </Button></DialogTrigger>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-lg">
        <DialogHeader><DialogTitle>IP 定位 · {ip}</DialogTitle><DialogDescription>至少两家定位库一致且超过半数才自动采用。无多数结果时保留已有解析、暂停新增分组；人工纠正优先于自动结果。</DialogDescription></DialogHeader>
        {location?.sources?.length ? <div className="space-y-2 rounded-lg border p-3 text-sm" aria-label="定位数据库结果">
          {location.sources.map(source => <div key={source.name} className="flex justify-between gap-3"><span className="shrink-0 text-muted-foreground">{source.name}</span><span className="text-right">{countryName(source.country)}{[source.region, source.city].filter(Boolean).length > 0 && <span className="block text-xs text-muted-foreground">{[...new Set([source.region, source.city].filter(Boolean))].join(" · ")}</span>}</span></div>)}
        </div> : <p className="text-sm text-muted-foreground">{status === "manual" ? "当前使用人工纠正结果，可恢复自动判断。" : "暂时没有可用的自动定位结果，可手动确认。"}</p>}
        <form onSubmit={event => { event.preventDefault(); void save(); }} className="space-y-4">
          <div className="space-y-2"><Label htmlFor={`location-country-${ip}`}>国家或地区代码</Label><Input id={`location-country-${ip}`} value={countryCode} onChange={event => { setCountryCode(event.target.value.toUpperCase()); setRegion(""); setCity(""); }} maxLength={2} pattern="[A-Z]{2}" placeholder="例如 SG、HK、US" required disabled={saving} /><p className="text-xs text-muted-foreground">{ /^[A-Z]{2}$/.test(countryCode) ? countryName(countryCode) : "填写两位国家或地区代码，例如 SG 表示新加坡。"}</p></div>
          <div className="grid grid-cols-2 gap-3"><div className="space-y-2"><Label htmlFor={`location-region-${ip}`}>省 / 州（可选）</Label><Input id={`location-region-${ip}`} value={region} onChange={e => setRegion(e.target.value)} maxLength={100} disabled={saving} /></div><div className="space-y-2"><Label htmlFor={`location-city-${ip}`}>城市（可选）</Label><Input id={`location-city-${ip}`} value={city} onChange={e => setCity(e.target.value)} maxLength={100} disabled={saving} /></div></div>
          {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
          <p className="text-xs leading-5 text-muted-foreground">保存后列表立即更新，DNS 在下次同步时按新国家分组；延迟、丢包和稳定性筛选仍然有效。</p>
          <div className="flex flex-wrap justify-end gap-2">{status === "manual" && <Button type="button" variant="outline" disabled={saving} onClick={() => void save(true)}>恢复自动判断</Button>}<Button type="submit" disabled={saving}>{saving ? "保存中…" : "保存人工纠正"}</Button></div>
        </form>
      </DialogContent>
    </Dialog>
  </div>;
}
