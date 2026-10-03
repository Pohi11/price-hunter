import { WebStorageStateStore } from "oidc-client-ts";
import { createContext, useContext, useEffect, type ReactNode } from "react";
import { AuthProvider, useAuth } from "react-oidc-context";
import { setTokenProvider } from "./api/client";
import { Button, Spinner } from "./components/ui";
import type { AppConfig } from "./config";

type Session = { email?: string; signOut?: () => void };
const SessionContext = createContext<Session>({});
export const useSession = () => useContext(SessionContext);

/**
 * Cognito sign-in via the managed login page (authorization code + PKCE).
 * Tokens live in sessionStorage, not localStorage, so they don't outlive
 * the tab. The API receives the ID token, which carries the verified email
 * and has the app client as its audience (what the JWT authorizer checks).
 */
export function Auth({ config, children }: { config: AppConfig | null; children: ReactNode }) {
  if (!config) {
    return <SessionContext.Provider value={{ email: "local dev user" }}>{children}</SessionContext.Provider>;
  }
  const redirect = window.location.origin + "/";
  return (
    <AuthProvider
      authority={config.authority}
      client_id={config.clientId}
      redirect_uri={redirect}
      post_logout_redirect_uri={redirect}
      response_type="code"
      scope="openid email profile"
      automaticSilentRenew
      userStore={new WebStorageStateStore({ store: window.sessionStorage })}
      onSigninCallback={() => window.history.replaceState({}, document.title, window.location.pathname)}
    >
      <Gate config={config}>{children}</Gate>
    </AuthProvider>
  );
}

function Gate({ config, children }: { config: AppConfig; children: ReactNode }) {
  const auth = useAuth();

  useEffect(() => {
    setTokenProvider(() => auth.user?.id_token ?? null);
  }, [auth.user]);

  if (auth.isLoading || auth.activeNavigator) {
    return <div className="grid min-h-dvh place-items-center"><Spinner label="Signing in" /></div>;
  }
  if (auth.error) {
    return <SignIn onClick={() => auth.signinRedirect()} message={`Sign-in failed: ${auth.error.message}`} />;
  }
  if (!auth.isAuthenticated) {
    return <SignIn onClick={() => auth.signinRedirect()} />;
  }

  // Cognito doesn't implement OIDC end_session; use its /logout endpoint.
  const signOut = () => {
    auth.removeUser();
    const url = new URL("/logout", config.cognitoDomain);
    url.searchParams.set("client_id", config.clientId);
    url.searchParams.set("logout_uri", window.location.origin + "/");
    window.location.assign(url.toString());
  };
  const email = auth.user?.profile.email;
  return <SessionContext.Provider value={{ email, signOut }}>{children}</SessionContext.Provider>;
}

function SignIn({ onClick, message }: { onClick: () => void; message?: string }) {
  return (
    <div className="grid min-h-dvh place-items-center px-4">
      <div className="w-full max-w-sm space-y-5 text-center">
        <img src="/favicon.svg" alt="" className="mx-auto size-12" />
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Price Hunter</h1>
          <p className="mt-1 text-sm text-muted">Track prices and get an email when they drop to your target.</p>
        </div>
        {message && <p className="rounded-lg bg-bad-soft px-3 py-2 text-sm text-bad">{message}</p>}
        <Button variant="primary" className="w-full" onClick={onClick}>Sign in</Button>
      </div>
    </div>
  );
}
