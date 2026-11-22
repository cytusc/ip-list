"use client";

import { useEffect, useState } from "react";
import { emptyPage } from "@/lib/paged-query";
import type { LatencyProbe } from "@/lib/types";
import { LatencyProbeManager } from "@/components/latency-probe-manager";

const initial = emptyPage<LatencyProbe>();

export default function LatencyProbesPage() {
  const [tlsPin, setTLSPin] = useState("");
  useEffect(() => {
    fetch("/api/probe-config", { credentials: "include" })
      .then((response) => (response.ok ? response.json() : null))
      .then((data) => setTLSPin(data?.tls_pin || ""))
      .catch(() => {});
  }, []);
  return <LatencyProbeManager initial={initial} tlsPin={tlsPin} />;
}
