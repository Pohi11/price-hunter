import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, type Check, type CreateProductRequest, type Product, type UpdateProductRequest } from "./client";

export const keys = {
  products: ["products"] as const,
  product: (id: string) => ["products", id] as const,
  prices: (id: string, days: number) => ["products", id, "prices", days] as const,
  checks: (id: string) => ["products", id, "checks"] as const,
  check: (id: string, checkId: string) => ["products", id, "checks", checkId] as const,
  me: ["me"] as const,
};

export function useProducts() {
  return useQuery({ queryKey: keys.products, queryFn: api.listProducts, select: (d) => d.items, refetchInterval: 30_000 });
}

export function useProduct(id: string) {
  return useQuery({ queryKey: keys.product(id), queryFn: () => api.getProduct(id), refetchInterval: 15_000 });
}

/** days = 0 means "all time" (capped by the API at 2 years). */
export function usePrices(id: string, days: number, maxPoints = 400) {
  return useQuery({
    queryKey: [...keys.prices(id, days), maxPoints],
    queryFn: () => {
      const span = days > 0 ? days : 730;
      return api.prices(id, new Date(Date.now() - span * 86_400_000), maxPoints);
    },
    staleTime: 60_000,
  });
}

export function useChecks(id: string) {
  return useQuery({ queryKey: keys.checks(id), queryFn: () => api.listChecks(id), select: (d) => d.items, refetchInterval: 15_000 });
}

const terminal = new Set(["SUCCEEDED", "FAILED", "SKIPPED"]);

/** Polls a check every second until it finishes. */
export function useCheckStatus(productId: string, checkId: string | null) {
  return useQuery({
    queryKey: keys.check(productId, checkId ?? "none"),
    queryFn: () => api.getCheck(productId, checkId!),
    enabled: !!checkId,
    refetchInterval: (q) => (q.state.data && terminal.has(q.state.data.status) ? false : 1000),
  });
}

export function isTerminal(c?: Check) {
  return !!c && terminal.has(c.status);
}

export function useCreateProduct() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: CreateProductRequest) => api.createProduct(req),
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.products }),
  });
}

export function useUpdateProduct(p: Product) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: UpdateProductRequest) => api.updateProduct(p.id, req, p.version),
    onSuccess: (updated) => {
      qc.setQueryData(keys.product(p.id), updated);
      qc.invalidateQueries({ queryKey: keys.products });
    },
  });
}

export function useDeleteProduct() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.deleteProduct(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.products }),
  });
}

export function useTriggerCheck(id: string) {
  return useMutation({ mutationFn: () => api.triggerCheck(id) });
}

export function useMe() {
  return useQuery({ queryKey: keys.me, queryFn: api.me });
}

export function useUpdateMe() {
  const qc = useQueryClient();
  return useMutation({ mutationFn: (enabled: boolean) => api.updateMe(enabled), onSuccess: (p) => qc.setQueryData(keys.me, p) });
}
