"use client";

import { useEffect, useState } from "react";
import { getUser } from "@/lib/auth";
import { ConsoleShell } from "@/components/console-shell";
import type { User } from "@/lib/types";

export default function ConsoleLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  const [user, setUser] = useState<User | null>(null);
  const [ready, setReady] = useState(false);

  useEffect(() => {
    let alive = true;
    getUser()
      .then((value) => {
        if (!alive) return;
        if (!value) {
          window.location.replace("/login/");
          return;
        }
        setUser(value);
        setReady(true);
      })
      .catch(() => {
        if (alive) window.location.replace("/login/");
      });
    return () => {
      alive = false;
    };
  }, []);

  if (!ready || !user) return null;
  return <ConsoleShell username={user.username}>{children}</ConsoleShell>;
}
