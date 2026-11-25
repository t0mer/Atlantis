import { useEffect, type ReactNode } from "react";
import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import { setCsrfToken, setUnauthorizedHandler } from "./api/client";
import { useAuth, useDeco } from "./api/hooks";
import { useLiveStream } from "./api/live";
import { Shell } from "./components/Shell";
import { Auth } from "./pages/Auth";
import { Connect } from "./pages/Connect";
import { Overview } from "./pages/Overview";
import { Clients } from "./pages/Clients";
import { ClientDetail } from "./pages/ClientDetail";
import { Nodes } from "./pages/Nodes";
import { NodeDetail } from "./pages/NodeDetail";
import { WiFi } from "./pages/WiFi";
import { Events } from "./pages/Events";
import { Settings } from "./pages/Settings";

function Gate({ children }: { children: (user?: string) => ReactNode }) {
  const { data, isLoading } = useAuth();
  const qc = useQueryClient();
  const signedIn = !!data?.user;
  setCsrfToken(data?.csrf_token);
  useEffect(() => setUnauthorizedHandler(() => qc.invalidateQueries({ queryKey: ["auth"] })), [qc]);
  useLiveStream(signedIn);
  if (isLoading) return null;
  if (!signedIn) return <Auth setup={!!data?.setup_required} />;
  return <DecoGate>{children(data?.user)}</DecoGate>;
}

/** Until a Deco is connected, the only page is the one that connects it. */
function DecoGate({ children }: { children: ReactNode }) {
  const { data, isLoading } = useDeco();
  if (isLoading) return null;
  // While a stored Deco's connection is being changed it is briefly not
  // running; keep the app (and the dialog doing the change) on screen.
  if (data && !data.running && !(data.configured && data.changing)) return <Connect deco={data} />;
  return <>{children}</>;
}

export function App() {
  return (
    <BrowserRouter>
      <Gate>
        {(user) => (
          <Shell user={user}>
            <Routes>
              <Route path="/" element={<Overview />} />
              <Route path="/clients" element={<Clients />} />
              <Route path="/clients/:mac" element={<ClientDetail />} />
              <Route path="/nodes" element={<Nodes />} />
              <Route path="/nodes/:id" element={<NodeDetail />} />
              <Route path="/wifi" element={<WiFi />} />
              <Route path="/events" element={<Events />} />
              <Route path="/settings" element={<Settings />} />
              <Route path="*" element={<Navigate to="/" replace />} />
            </Routes>
          </Shell>
        )}
      </Gate>
    </BrowserRouter>
  );
}
