import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { ApiError, setApiBase } from "./api/client";
import { App } from "./App";
import { Auth } from "./auth";
import { loadConfig } from "./config";
import "./index.css";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // Don't hammer the API on 4xx; retry transient failures twice.
      retry: (count, err) => !(err instanceof ApiError && err.status < 500) && count < 2,
      refetchOnWindowFocus: true,
    },
  },
});

const config = await loadConfig();
if (config) setApiBase(config.apiBaseUrl);

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <Auth config={config}>
      <QueryClientProvider client={queryClient}>
        <BrowserRouter>
          <App />
        </BrowserRouter>
      </QueryClientProvider>
    </Auth>
  </StrictMode>,
);
