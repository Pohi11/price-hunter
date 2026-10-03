import { NavLink, Outlet, Route, Routes } from "react-router-dom";
import { useSession } from "./auth";
import { ProductDetail } from "./pages/ProductDetail";
import { Settings } from "./pages/Settings";
import { Watchlist } from "./pages/Watchlist";

function Layout() {
  const session = useSession();
  const link = ({ isActive }: { isActive: boolean }) => `text-sm ${isActive ? "font-medium text-ink" : "text-muted hover:text-ink"}`;
  return (
    <div className="min-h-dvh">
      <header className="border-b border-line bg-surface">
        <div className="mx-auto flex max-w-5xl items-center justify-between gap-4 px-4 py-3 sm:px-6">
          <NavLink to="/" className="flex items-center gap-2 font-semibold">
            <img src="/favicon.svg" alt="" className="size-6" /> Price Hunter
          </NavLink>
          <nav className="flex items-center gap-5">
            <NavLink to="/" end className={link}>Watchlist</NavLink>
            <NavLink to="/settings" className={link}>Settings</NavLink>
            {session.signOut && (
              <button onClick={session.signOut} className="text-sm text-muted hover:text-ink" title={session.email}>Sign out</button>
            )}
          </nav>
        </div>
      </header>
      <main className="mx-auto max-w-5xl px-4 py-8 sm:px-6">
        <Outlet />
      </main>
    </div>
  );
}

export function App() {
  return (
    <Routes>
      <Route element={<Layout />}>
        <Route index element={<Watchlist />} />
        <Route path="products/:id" element={<ProductDetail />} />
        <Route path="settings" element={<Settings />} />
        <Route path="*" element={<p className="text-muted">Page not found.</p>} />
      </Route>
    </Routes>
  );
}
