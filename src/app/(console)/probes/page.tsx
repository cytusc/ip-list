import { getNodes, getNetworks, getScanRecords } from "@/lib/queries";
import { ProbeList } from "@/components/probe-manager";
export const dynamic = "force-dynamic";
export default async function ProbesPage() {
  const [nodes, networks, records] = await Promise.all([
    getNodes(),
    getNetworks(),
    getScanRecords(),
  ]);
  return (
    <ProbeList nodes={nodes} networks={networks} initial={records} />
  );
}
