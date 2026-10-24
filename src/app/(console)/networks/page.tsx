import { getNetworks } from "@/lib/queries";
import { ConfigManager } from "@/components/config-manager";
export const metadata = { title: "网段配置" };
export default async function NetworksPage() {
  return (
    <ConfigManager kind="networks" nodes={[]} networks={await getNetworks()} />
  );
}
