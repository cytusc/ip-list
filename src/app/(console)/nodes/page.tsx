import { getNodes } from "@/lib/queries";
import { ConfigManager } from "@/components/config-manager";
export const metadata = { title: "节点管理" };
export default async function NodesPage() {
  const nodes = await getNodes();
  return <ConfigManager kind="nodes" nodes={nodes} networks={[]} />;
}
