import { getBlacklist } from "@/lib/queries";
import { BlacklistManager } from "@/components/blacklist-manager";

export default async function BlacklistPage() {
  return <BlacklistManager initialItems={await getBlacklist()} />;
}
