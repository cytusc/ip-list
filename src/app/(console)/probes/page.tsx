import { getNodes, getNetworks } from "@/lib/queries";
import { ProbeManager } from "@/components/probe-manager";
export default async function ProbesPage() {
  const [nodes, networks] = await Promise.all([getNodes(), getNetworks()]);
  return <ProbeManager nodes={nodes} networks={networks} />;
}
