import { getNodes, getNetworks } from "@/lib/queries";
import { ConfigManager } from "@/components/config-manager";
export const metadata = { title: "网段配置" };
export default async function NetworksPage({
  searchParams,
}: {
  searchParams: Promise<{ node?: string }>;
}) {
  const [nodes, networks] = await Promise.all([getNodes(), getNetworks()]);
  const { node } = await searchParams;
  const initialNodeFilter = nodes.some((item) => item.id === node)
    ? node
    : "all";
  return (
    <ConfigManager
      key={initialNodeFilter}
      kind="networks"
      nodes={nodes}
      networks={networks}
      initialNodeFilter={initialNodeFilter}
    />
  );
}
