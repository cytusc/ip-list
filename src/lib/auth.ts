import "server-only";
import { redirect } from "next/navigation";
import { cookies } from "next/headers";
import { api, ApiError } from "@/lib/api";
import type { User } from "@/lib/types";
export async function getUser(): Promise<User | null> {
  if (!(await cookies()).get("ipfilter_session")?.value) return null;
  try {
    return (await api<User>("/auth/me")).data;
  } catch (error) {
    if (error instanceof ApiError && error.status === 401) return null;
    throw error;
  }
}
export async function requireUser() {
  const user = await getUser();
  if (!user) redirect("/login");
  return user;
}
