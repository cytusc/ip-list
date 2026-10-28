import { pagedQuery } from "@/lib/paged-query";
import { CFTargetList } from "@/components/cf-manager";
import type { CFTarget } from "@/lib/types";

export default async function Page() {
  return <CFTargetList initial={await pagedQuery<CFTarget>("/cf-targets")} networks={[]} />;
}
