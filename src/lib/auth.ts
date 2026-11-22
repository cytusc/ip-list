"use client";

import { api, ApiError } from "@/lib/api";
import type { User } from "@/lib/types";

export async function getUser(): Promise<User | null> {
  try {
    return (await api<User>("/auth/me")).data;
  } catch (error) {
    if (error instanceof ApiError && error.status === 401) return null;
    throw error;
  }
}

export async function requireUser(): Promise<User | null> {
  const user = await getUser();
  if (!user) window.location.replace("/login/");
  return user;
}
