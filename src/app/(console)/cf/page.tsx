import { api } from "@/lib/api";
import { getNetworks } from "@/lib/queries";
import { CFTargetList } from "@/components/cf-manager";
import type { CFTarget } from "@/lib/types";

export default async function Page() {
  const [targets, networks] = await Promise.all([
    api<CFTarget[]>("/cf-targets"),
    getNetworks(),
  ]);
  return <CFTargetList items={targets.data} networks={networks} />;
}
