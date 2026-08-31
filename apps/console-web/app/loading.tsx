import { AppShell, Loading } from "@/components/Surface";

/**
 * What every surface shows while its source is being read.
 *
 * The App Router's own convention rather than a spinner library or client state: each
 * surface is a dynamic server component that awaits an HTTP read, and Next renders this
 * for that interval. One file covers all six because the shell is identical and the
 * honest thing to say is identical too.
 *
 * The shell renders around it, so the navigation and the read-only statement do not
 * blink out while a surface loads. A console whose brand disappears during every read
 * looks broken at exactly the moment an operator is waiting on it.
 */
export default function LoadingSurface() {
  return (
    <AppShell context="Reading">
      <Loading what="the platform's record" />
    </AppShell>
  );
}
