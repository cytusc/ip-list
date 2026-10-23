import { Skeleton } from "@/components/ui/skeleton";
export default function Loading() {
  return (
    <div aria-label="正在加载配置" role="status" className="space-y-8">
      <Skeleton className="h-9 w-48" />
      <div className="grid gap-5 sm:grid-cols-3">
        {[1, 2, 3].map((i) => (
          <Skeleton key={i} className="h-28 rounded-xl" />
        ))}
      </div>
      <Skeleton className="h-96 w-full rounded-xl" />
    </div>
  );
}
