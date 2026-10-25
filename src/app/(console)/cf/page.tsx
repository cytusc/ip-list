import { api } from "@/lib/api";
import { getNetworks } from "@/lib/queries";
import { CFTargetList } from "@/components/cf-manager";
import type { CFTarget, CFDetail } from "@/lib/types";
export default async function Page() {
  const [targets, networks] = await Promise.all([
    api<CFTarget[]>("/cf-targets"),
    getNetworks(),
  ]);
  const items = targets.data;
  const initial = items.length
    ? (await api<CFDetail>(`/cf-targets/${encodeURIComponent(items[0].id)}`))
        .data
    : null;
  return <CFTargetList items={items} networks={networks} initial={initial} />;
}
