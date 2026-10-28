import { pagedQuery } from "@/lib/paged-query";
import type { Network } from "@/lib/types";
import { ConfigManager } from "@/components/config-manager";
export const metadata = { title: "网段配置" };
export default async function NetworksPage() {
  return (
    <ConfigManager kind="networks" initial={await pagedQuery<Network>("/networks")} />
  );
}
