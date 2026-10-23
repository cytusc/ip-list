import { requireUser } from "@/lib/auth";
import { ConsoleShell } from "@/components/console-shell";

export const dynamic = "force-dynamic";
export default async function ConsoleLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  const user = await requireUser();
  return <ConsoleShell username={user.username}>{children}</ConsoleShell>;
}
