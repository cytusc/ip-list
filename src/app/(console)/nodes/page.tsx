import { pagedQuery } from "@/lib/paged-query";
import type { Node } from "@/lib/types";
import { ConfigManager } from "@/components/config-manager";
export const metadata = { title: "节点管理" };
export default async function NodesPage() {
  return <ConfigManager kind="nodes" initial={await pagedQuery<Node>("/nodes")} />;
}
