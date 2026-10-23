import { api } from "@/lib/api";
import { CFTargetList } from "@/components/cf-manager";
import type { CFTarget } from "@/lib/types";
export default async function Page() {
  return <CFTargetList items={(await api<CFTarget[]>("/cf-targets")).data} />;
}
