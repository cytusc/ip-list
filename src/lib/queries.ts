import "server-only";
import { api } from "@/lib/api";
import { requireUser } from "@/lib/auth";
import type { Node, Network } from "@/lib/types";
export async function getNodes(): Promise<Node[]> {
  await requireUser();
  return (await api<Node[]>("/nodes")).data;
}
export async function getNetworks(): Promise<Network[]> {
  await requireUser();
  return (await api<Network[]>("/networks")).data;
}
